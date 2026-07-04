# ADR 0009: Resource-based JS budgets (bytes) and concurrent module-graph warming

Date: 2026-07-03
Status: accepted

## Context

unblink could not render modern **code-split SPAs** — pages (Astro islands,
Vite/webpack route-splitting) that ship hundreds of small content-hashed `.mjs`
chunks. Two independent walls, plus two blunt metrics, combined to defeat them:

1. **Throughput.** Runtime dynamic `import()` was the only serial load path.
   `lowerDynamicImport` rewrites `import()` to `__unblinkImportSync`
   (`internal/js/dynimport.go`), a *synchronous* goja function that fetches +
   bundles + runs each chunk inline on the single loop goroutine. N imports
   became N sequential blocking cycles even under `Promise.all`. Static import
   graphs were already fetched concurrently by esbuild, and classic `<script
   src>` was already prefetched — but `<link rel="modulepreload">` hints (which
   frameworks emit for exactly the dynamically-imported chunks) were read
   nowhere. Measured: a heavy page loaded ~21 chunks/s and hit the hard 30 s
   `maxRenderBudget` at ~637 requests, never settling.

2. **A fixed request-COUNT budget** (`DefaultJSMaxRequests = 50`). A poor proxy
   for the resource it guards: 250 tiny hashed modules (~1–2 MB total) are
   cheap, yet the count denied them. Raising the count just moved the failure
   to the time wall.

3. **A 60 s rolling rate-limiter** (300 requests/min) for live sessions — a
   second count-shaped cap that also throttled legitimate heavy hydration.

4. **An asset cache capped at 256 entries**, smaller than a big chunk graph, so
   it thrashed and re-fetched (re-counted) evicted chunks.

## Decision

Replace the count-shaped caps with **resource-based bounds**, and make the
module graph load concurrently.

- **Byte budget replaces the request-count budget.** `guardedTransport`
  (`internal/browser/transport.go`) now accumulates response-body bytes and
  denies once a per-render (one-shot) / per-dispatch (live) cumulative budget is
  reached. Default `--js-max-bytes` = 64 MiB — enough for a heavy graph plus
  data fetches, far below the 1 GiB heap guard (ADR 0003). Size isn't knowable
  before a fetch, so the crossing request completes and denial kicks in on the
  next one (a soft cap; overshoot ≤ the in-flight batch). The wall-clock render
  budget stays the time bound.

- **The request count becomes an off-by-default runaway backstop.**
  `--js-max-requests` defaults to 0 (disabled). It remains available for the one
  case bytes can't see — a page firing many tiny/zero-byte requests — but is no
  longer the operative limit.

- **The 60 s rolling window is removed entirely.** Live sessions are bounded by
  the per-dispatch byte budget instead: the transport is built once per session,
  but `ResetBudget()` is called at the top of each `Context.Dispatch`, so every
  agent action gets a fresh budget without starving a long session. The residual
  abuse surface is covered by the SSRF dial guard, the session idle TTL/cap and
  live-runtime LRU, the process memory guard, and the opt-in per-host
  `--rate-limit`.

- **The asset cache is bounded by bytes, not entries.** The 256-entry cap is
  gone; eviction is the existing two-generation swap keyed on summed bytes,
  raised to 64 MiB per generation (sized at/above the download budget so a heavy
  render's chunk graph stays in one generation without a mid-render swap).

- **Concurrent modulepreload warming (the throughput fix).** At render start,
  `startModulePreload` warms the `<link rel=modulepreload>` /
  script-`rel=preload` chunk graph concurrently into the asset cache
  (`warmConcurrent`, generalized from `startPrefetch`; workers raised 4 → 16).
  When the serial `__unblinkImportSync` path then runs, each chunk is a cache
  hit — or, if a warm is still in flight, the module `OnLoad` joins it via
  `prefetched()` instead of issuing a duplicate fetch. This turns the
  RTT-bound serial path into a CPU-bound one (esbuild bundle + `RunProgram`).

## Why warming, not async `import()`

The tempting alternative — make `__unblinkImportSync` return a real Promise and
fetch off-loop — would add a new async primitive. Per **ADR 0004**, every source
of future JS work must be visible to the settle check (the `pending` bracket or
the timer audit), so an async import would require pending-bracketing every
import continuation and would break the "sequential imports never race"
assumption behind the single `__unblinkChunkResult` global. Warming adds **no
async primitive**: the warmers are engine plumbing (like the existing classic-
script prefetch), consumers block on them synchronously before the settle poll
starts, and the import path stays exactly as synchronous as before. The settle
proof holds by construction.

Warmer goroutines never touch the `bridge.prefetch` map — `warmConcurrent`
captures each `(url, entry)` pair on-loop and hands it to workers via the queue —
so the map stays single-writer and race-free against a second `warmConcurrent`
call and against the off-loop `prefetched()` readers in esbuild's `OnLoad`
(verified under `go test -race`).

## Consequences

- Heavy code-split SPAs render within budget instead of timing out.
- The operative network bound is now bytes + wall-clock, which matches the real
  cost and is observable via the new `net_bytes` render diagnostic.
- Over-fetch risk: preloaded-but-unused chunks consume the byte budget. Mitigated
  by the 64 MiB default and framework-curated hint lists; a separate warming
  sub-budget was considered and deferred as unnecessary complexity.
- Removing the count cap's protection against a tiny-request bomb is backstopped
  by the wall-clock budget and the optional `--js-max-requests`.
