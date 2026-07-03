# ADR 0004: Settle closes on provable idleness, not only a quiet window

Date: 2026-07-03
Status: accepted

## Context

A `Start()`ed goja event loop never drains its job queue (a background
keepalive holds +1, and any `setInterval` pins it forever), so "the page
settled" cannot be observed — it has to be inferred. Since Phase 12 the
inference was a quiet-window heuristic: no DOM mutation, no in-flight network,
and no clamped one-shot timers for 4 consecutive 15 ms ticks (≈60 ms), bounded
by the render budget. That window is a correctness constant — 30 ms proved too
aggressive (a framework idling between microtask batches, or an XHR scheduled
but not yet dispatched, could trip it and return a half-hydrated page).

The cost: **every render with any script paid the full ~60 ms even when the
page was finished at the first check.** Measured on the render benchmarks,
`RenderMinimal` was 62.8 ms of which ~60 ms was the settle wait; the floor was
75–95 % of per-render latency once caches were warm, and the dominant reason
unblink's SPA latency trailed warm-browser MCP tools (~15–22 ms/page).

The key observation is that the sandbox already instruments every mechanism
that can run more JavaScript:

- **Network** — fetch/XHR (`fetchPromise`) and dynamically-inserted
  `<script src>` chunks (`loadExternalScript`) bracket the off-loop request
  with `bridge.pending`, incremented *synchronously on-loop* before the fetch
  goroutine spawns — no race window.
- **Macrotasks** — the prelude wraps `setTimeout`/`setInterval` into a handle
  table (`live`) for zone.js-style numeric-id compatibility; rAF,
  requestIdleCallback, `AbortSignal.timeout`, `window.postMessage`, and
  MessagePort delivery all route through the wrapped `setTimeout`. Go-side
  keepalives captured the natives *before* the wrapper, so they are excluded
  by construction.
- **Microtasks** — goja drains the promise-job queue when the outermost JS
  frame returns; settle checks run in Go between loop jobs, after any drain.
- **Modules** — `runModules`/dynamic `import()` execute synchronously on-loop.

One mechanism escaped: goja's event loop exposes a **native `setImmediate`**
that the prelude did not wrap, so immediates were invisible to any audit —
and React's scheduler prefers `setImmediate` over MessageChannel when it
exists.

## Decision

1. The prelude re-routes `setImmediate`/`clearImmediate` through the wrapped
   `setTimeout`, closing the leak; the timer audit now reports
   `{c: clamped one-shots unfired, t: all live wrapped timers}`.
2. `settlePoll` gains a first tier above the quiet-window heuristic:
   **provable idleness**. When the wait condition is satisfied,
   `pending == 0`, and the live-timer count is 0, no mechanism exists to run
   more JavaScript — the page cannot change again, so the poll closes after a
   single ~1 ms confirmation tick instead of waiting out the window. The
   confirmation tick is defense-in-depth against a future work source that
   escapes the audit; it is documented as droppable with field evidence.
3. Anything armed (an interval, a pending timer, in-flight network) falls back
   to the quiet-window heuristic **unchanged at 60 ms of wall clock**, now
   detected at 5 ms granularity (`settleTick` 15 ms → 5 ms) with the first
   check running synchronously at poll entry. Clamped one-shot timers still
   hold the window open; unclamped live timers still do not (an interval must
   not pin the settle to the budget).
4. Renders that close via the fast path report `SettledIdle: true` in the
   render diagnostics, so a field regression is attributable.

## The load-bearing invariant

Provable idleness is a *proof* only while **every** source of future JS work
is visible to the settle check: network through the `pending` bracket,
macrotasks through the audited wrapped-timer table. **Adding a new async
primitive to the sandbox (a real WebSocket, a Worker, a native scheduler API,
a second timer surface) requires routing it through one of those two channels
or extending the audit** — otherwise the fast path can snapshot between that
primitive's callbacks. `TestRenderSetImmediateChainContentLands` is the
template regression test for this class of leak.

## Consequences

- Idle pages settle in ~1–3 ms instead of ~60 ms; `RenderMinimal` drops from
  ~63 ms to ~3 ms and framework renders land in warm-browser territory. Live
  `interact` dispatches whose handlers finish synchronously no longer pay the
  window per click.
- Pages with real deferred work (timers, network, intervals) behave exactly as
  before: the 60 ms-quiet-after-last-activity semantics and the budget bound
  are unchanged, as are the saturation diagnostics
  (`net_pending`/`dom_busy`/`render_budget_hit`/`timers_pending`).
- `wait_for`/`wait_text` remain the caller-side correctness tool: a non-empty
  unmet condition disables the fast path and holds the poll to the budget,
  exactly as before.
- The settle constants stay compile-time (no flags): the fast path is safe by
  construction, and the heuristic window remains a measured correctness
  constant, not a tuning knob.
