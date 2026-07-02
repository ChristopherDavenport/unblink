# Changelog

All notable changes are recorded here. Earlier history lives in the phase log of
[docs/architecture.md](docs/architecture.md).

## v0.17.0 — Publication readiness (Phase 21)

The pre-first-public-release pass: close the highest-frequency browser-API gaps,
bound the last unbounded resource, make the failure modes visible and gated, and
back the footprint pitch with measured numbers.

### Added

- **Browser API tier-1** (a new prelude layer): `TextEncoder`/`TextDecoder`,
  `atob`/`btoa`, `performance.now`/`mark`/`measure`; a truthful constant viewport
  (1280×720@1x: `innerWidth`/`screen`/`devicePixelRatio`/`visualViewport`) and a
  real `matchMedia` evaluator (replacing the always-false stub); constructible
  `Headers`/`Request`/`Response`/`Blob`/`File`/`FormData` with a real `fetch`
  `Response` (arrayBuffer/blob/clone/redirected) and FormData multipart bodies;
  `postMessage` + functional `MessageChannel`; `DOMParser`/`XMLSerializer`/`Range`;
  constructable `CSSStyleSheet` + `adoptedStyleSheets` (Lit's feature-detect);
  `WeakRef`/`FinalizationRegistry` and an `Intl` crash-avoidance shim;
  `document.title` setter, `document.URL`/`referrer`/`currentScript`/
  `getElementsByName`; a fuller `navigator` (`webdriver`, `sendBeacon`,
  `userAgentData`, …).
- **`interact` keyboard support**: `event=keydown`/`keyup`/`keypress` now dispatch
  a real `KeyboardEvent` (via a new optional `key` param), fire the full
  keydown→input→keypress→keyup sequence for printable keys, and submit a form on
  `Enter` inside it — so `{event:"keydown", value:"query", key:"Enter"}` drives a
  search box end to end.
- **JS memory guard** (`--js-memory-limit`, default 1024 MiB): a process-heap
  watchdog interrupts runaway page JS before it can OOM the server
  ([ADR-0003](docs/decisions/0003-js-memory-guard.md)).
- **Timer clamp + `timers_pending`**: a one-shot `setTimeout` past the render
  budget is clamped to fire in-budget so its deferred content still materializes;
  any timer still pending at snapshot is reported.
- **Measured footprint**: `make membench` (a separate module) benchmarks unblink
  vs. headless Chromium; `docs/comparison.md` gains real numbers (≈15× lighter and
  faster to start on identical pages).
- `SECURITY.md`, `CONTRIBUTING.md`, this changelog, a README MCP-client-config
  section, and a consolidated flags table.

### Changed

- `navigator.webdriver` is `true` by default (unblink is automation and
  self-identifies); `--tls-mimic` flips it to `false` for fingerprint parity.
- The eval gate grew from 40 to 47 must-pass cases, now covering the
  starved-render diagnostics, `net_denied`, keydown, Svelte, Lit, and a tier-1 API
  smoke page.

See the [Phase 21 section of docs/architecture.md](docs/architecture.md) for the
full design notes.
