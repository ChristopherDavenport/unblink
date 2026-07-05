# ADR 0015: Same-Origin Policy is unblink's default

Date: 2026-07-04
Status: accepted

## Context

The Same-Origin Policy (SOP) is the browser's foundational isolation model; CORS
(ADR 0011) is only its network-read *exception* mechanism. Having shipped CORS,
SRI, Fetch-Metadata, cross-origin isolation, and CSP, the question arose: what
would it take for unblink to claim SOP as its default posture, comprehensively?

An audit found the answer is **almost nothing** — SOP has three axes, and unblink
already satisfies two of them:

1. **Network read isolation** — cross-origin responses are unreadable without CORS
   consent. Enforced (ADR 0011: CORS + credential scoping + cross-origin cookie
   suppression).
2. **Cross-document DOM isolation** — no reading another origin's DOM/window/frame.
   **Moot by architecture:** unblink is single-document, single-window, one goja
   runtime per render, and a navigation tears the runtime down and rebuilds it.
   There is no reachable foreign document object.
3. **Client-side storage partitioning by origin** — the one genuine gap.

## Decision

Adopt SOP as an explicit, default posture, and close the one gap.

**Gap closed — Web Storage is partitioned by origin.** `localStorage` and
`sessionStorage` were one flat bag per session, reused for every origin, so within
a single session a page on origin B could read the storage a page on origin A
wrote (e.g. an SPA's auth token). Storage is now keyed by document origin
(`scheme://host[:port]`): a session holds `map[origin]*MemStorage` for each of
local/session storage (`internal/session/session.go`), selected by the rendered
page's final origin in `processFetched`/`ensureLive` via `originKey`
(`internal/browser/browser.go`). Same origin still persists across navigations
(the browser-tab model); a different origin gets a separate store. This is
correct-by-default — nothing legitimately relies on cross-origin storage sharing,
so there is no flag. One-shot (session-less) reads are unaffected: each render is a
single origin with fresh per-render storage.

**Enforced axes of SOP in unblink:**
- Network reads — CORS (ADR 0011); private/metadata IPs — SSRF guard.
- Cookies — public-suffix-aware jar with domain/path scoping, HttpOnly hidden from
  `document.cookie`, non-credentialed cross-origin cookies suppressed
  (`internal/fetch`, `internal/browser/transport.go`, `internal/js/cors.go`).
- Web Storage — origin-partitioned (this ADR).

**Moot by architecture (deliberate non-goals, no cross-origin object exists to
guard):**
- iframes/frames/embed/object: content is never fetched; `contentWindow`/
  `contentDocument` are absent; stripped by the sanitizer.
- `window.open`/`opener`/`parent`/`top`/`frames`/`frameElement`: null stubs or
  self-references (`internal/js/prelude_api.go`).
- `postMessage`/`MessageChannel`/`BroadcastChannel`: same-runtime only; a cross-
  origin sender can never appear (`MessageEvent.origin` is already truthful).
- `document.domain`: not implemented (nothing to relax).
- `window.name`: fresh `''` per render — the classic cross-origin leak channel
  does not exist (and must stay per-navigation if ever persisted).
- IndexedDB (in-memory per-runtime) / CacheStorage (absent), Resource Timing (no
  resource entries): nothing persists or leaks across origins.
- Canvas/WebGL, layout/CSSOM geometry: permanent non-goals (ADR 0007).

**Minor fidelity item, intentionally not changed:** errors from a cross-origin
`<script>` are surfaced with full detail in the operator-facing `JSErrors`
diagnostics (no `"Script error."` collapse). This is *not* a page-observable leak —
`window.onerror` is not wired for script errors, so page JS can read no other
script's errors at all. Full detail at the operator/agent tier matches the CORS
history-preservation posture (ADR 0011): gate the page, not the operator.

## Consequences

- unblink can state SOP as its default: cross-origin network reads, cookies, and
  Web Storage are all origin-isolated, and the DOM half is isolated by
  construction. This ADR is the single reference for that posture.
- The only behavior change is storage partitioning; every other axis was already
  enforced or architecturally impossible to violate.
