# Changelog

All notable changes are recorded here. Earlier history lives in the phase log of
[docs/architecture.md](docs/architecture.md).

## v0.20.0 — 2026-07-04

### Added

- **A semantic structured representation of the page, computed from the parsed node
  tree with no browser ([ADR 0007](docs/decisions/0007-semantic-structured-representation.md),
  [ADR 0008](docs/decisions/0008-stable-element-ids.md)).** `browse` now returns a
  landmark/region map (banner/navigation/main/… with per-region link/form/control/
  heading counts), a structured heading outline (level/text/id, deep-linkable), and a
  unified `metadata` object (canonical — finally surfaced — plus og:image, author,
  published/modified time, favicon, twitter). `controls`/`forms`/`interact` controls
  gain rich ARIA state (checked/expanded/pressed/selected/required/invalid, plus
  value/placeholder/href), and every control/region/heading carries a stable
  content-hash `id` — a mutation-resilient reference key alongside the CSS selector.
  Ports charlotte's accessibility-tree decomposition to unblink's no-layout model;
  geometry/bounds/CSSOM remain permanent non-goals.
- **`requests`, `console`, and `cookies` tools.** `requests` lists the network
  requests a page's JavaScript made while rendering (method/url/status) — render once,
  see the JSON endpoint the page fetched, then `read` it directly instead of scraping
  the hydrated DOM. `console` returns the page's captured console output
  (log/info/warn/error/debug), filterable by level, for debugging a render. `cookies`
  inspects, sets, or clears a session's cookies, scoped to an origin.
  `requests`/`console` require JavaScript.

### Changed

- **Configurable tool exposure.** The server now advertises only tools that can do
  something: `search` is hidden without `--search-provider`, and `interact`/
  `requests`/`console` are hidden under `--disable-js` — a tool that could only return
  an error is context cost with no value. Operators can narrow the surface further with
  `--tools` (comma-separated tool names and/or the presets `core`/`read-only`/`full`)
  and `--disable-tools`.

## v0.19.0 — 2026-07-03

### Added

- **A broad Web API surface so JS pages render instead of crashing at boot
  (Phases 24–26, [docs/web-api-priorities.md](docs/web-api-priorities.md)).** The
  flat-DOM engine now provides the browser APIs mainstream apps touch during
  hydration, each by the cheapest treatment that lets content materialize — a real
  implementation, an inert crash-avoidance stub, or a deliberate leave-undefined:
  - **Tier 1** — `CSS.escape`/`CSS.supports`, `FileReader`, `ReadableStream`/
    `WritableStream`/`TransformStream`, `EventSource`, an in-memory `indexedDB`, an
    inert canvas-2D context, the `navigator` serviceWorker/clipboard/permissions/
    geolocation/mediaDevices surfaces, and `Element` `lastElementChild`/
    `getAttributeNode`/`namespaceURI`.
  - **Tier 2** — `DOMMatrix`/`DOMPoint`/`DOMRect`/`DOMQuad`/`Path2D`, `document.fonts`/
    `FontFace`, `Element.animate` (Web Animations), custom-element `disconnectedCallback`
    + `attachInternals`/`ElementInternals`, the Navigation API, `TextEncoderStream`/
    `TextDecoderStream`, `cookieStore`, `reportError`.
  - **`crypto.subtle`** ([ADR 0006](docs/decisions/0006-crypto-subtle.md)) — a real,
    Go-backed WebCrypto: `digest` (SHA-1/256/384/512), HMAC sign/verify, AES-GCM/CBC/CTR
    encrypt/decrypt, PBKDF2/HKDF derive, and symmetric key generate/import/export, with
    `getRandomValues` re-pointed at `crypto/rand`. RSA/ECDSA and wrap/unwrap reject
    catchably rather than throwing. Reverses the earlier "leave `crypto.subtle`
    undefined" decision.
  - **Tier 3** — inert crash-avoidance stubs for media playback + the full Web Audio
    node graph, WebRTC, Payment Request, Web Speech, the sensor family, `Notification`
    (`permission:'denied'`), Popover, Fullscreen, Picture-in-Picture, View Transitions,
    and the WebHID/USB/Serial/MIDI/Bluetooth/WebXR/WebGPU device family.

  Async completions route through the provable-idle settle audit (ADR 0004). Still
  permanent non-goals: pixels/layout/geometry, real media/GPU rendering, real Workers/
  WebSocket/persistent storage, and Shadow-DOM style scoping.

### Changed

- **[docs/comparison.md](docs/comparison.md)** reframes the JavaScript "what it
  forfeits" ceiling: with the broad Web API surface, pages rarely crash at boot, so
  the forfeit is now specifically pixels/layout and second-thread features — not the
  API surface itself.

## v0.18.0 — 2026-07-03

### Changed

- **JavaScript rendering is on by default.** The opt-in `--js` flag is replaced by
  an opt-out **`--disable-js`**, and the `read`/`click`/`submit_form` `render` arg now
  **defaults to on** — a read runs the page's JavaScript unless the caller passes
  `render=false` (or the server was started with `--disable-js`). For a robustness-first
  tool, seeing SPA content by default beats making every agent opt in per call; renders
  are ~2–10 ms (ADR 0004) and pages with no scripts still pay ~nothing. The `render`
  MCP arg became tri-state (omitted = render, explicit `false` = skip). Migration:
  drop `--js` from your launch args (JS is already on); add `--disable-js` for the
  static-only path.

### Added

- **Composed, encapsulating Shadow DOM (Phase 23, ADR 0005).** Replaces the flat,
  non-encapsulating model. Each shadow root is a detached subtree (page-JS
  `document.querySelector` respects the boundary); a compose pass flattens it —
  resolving `<slot>` distribution — into the light tree for extraction, fixing content
  loss where a component's `shadowRoot.innerHTML` used to destroy slotted children.
  Events cross the boundary correctly (composed path + `target` retargeting +
  `composedPath()`, `bubbles`/`composed` kept orthogonal); interact and `wait_for`
  pierce the boundary to reach shadow-rendered controls; declarative Shadow DOM
  (`<template shadowrootmode>`) renders on the static no-JS path; closed roots are
  hidden from page JS but still composed into output.

### Performance

- **Provable-idle settle (ADR 0004).** Every JS work source was already
  instrumented (network via the in-flight bracket, macrotasks via the wrapped
  timer table) except goja's native `setImmediate` — now wrapped. With that
  leak closed, zero in-flight requests plus zero live timers *proves* the page
  cannot change again, so the settle closes after a ~1 ms confirmation tick
  instead of waiting out the 60 ms quiet window; pages with anything armed
  keep the unchanged 60 ms-quiet-after-last-activity heuristic (now polled at
  5 ms granularity). Render benchmarks: minimal 63.3 ms → 1.8 ms, React
  81.6 ms → 9.4 ms, Vue 65.7 ms → 3.5 ms; synchronous-only `interact`
  dispatches stop paying the window per click. `SettledIdle` is recorded in
  render diagnostics.
- **Concurrent script-body prefetch.** The initial external `<script src>`
  bodies fetch in parallel (4 bounded workers, same counting/budgeted/
  SSRF-guarded transport) while execution stays strictly document-ordered:
  N scripts cost max(RTT) instead of sum(RTT), with identical request
  accounting (nothing speculative is fetched).
- **Content-only compile caches.** Identical script bytes share one compiled
  `goja.Program` regardless of URL or page position (external scripts now
  compile under their absolute URL — better stack traces than the positional
  `script-N.js`), and the esbuild bundle key ignores the page URL's
  query/fragment, so `?utm=`-style variants stop re-running the whole
  module-graph build.
- **Markdown memo on the page cache.** Repeat reads and pagination cursor
  pages within the 60 s cache TTL skip reduce+emit+defang entirely (warm read
  657 µs → 11 µs, allocations −99%). The memo lives and dies with its cache
  entry; sessions, waited renders, and credentialed one-shots are unaffected.
- **`--js-concurrency`** (new flag, default auto = CPU count clamped to
  4..16): the one-shot render semaphore was silently pinned at 4; it now
  scales with cores. `--js-prewarm` stays at 4; `MaxIdleConnsPerHost` rises
  8 → 16 to match.
- **Crossbench was measuring unblink's politeness limiter, not its engine**:
  with the limiter at its old 5 req/s default, the single-host cache-busted
  corpus paced every render at ~200 ms and pinned sequential throughput at
  exactly 5 pages/s. `docs/comparison.md` now also publishes the
  concurrent-throughput row. Re-measured medians: SPA renders ~2–10 ms (was
  ~200 ms), ~249 pages/s sequential / ~827 pages/s at 8-way concurrent (was
  ~5 pages/s).

### Changed

- **The per-host politeness rate limiter is now opt-in** (`--rate-limit`
  defaults to `0`/off, was 5 req/s): out of the box unblink runs
  like-for-like with the other benchmarked tools, none of which ships a
  crawl limiter, and throughput is bounded by the site rather than by
  unblink. Set `--rate-limit 5` (with `--rate-burst`) to restore the old
  polite-crawl behavior. Live-session JS subrequests stay bounded either
  way: the rolling window (300/min) enforces the same 5/s average the old
  default did.

### Added

- **Cross-tool benchmark harness** (`make crossbench`): `scripts/membench`
  generalized from a chromedp-only baseline into a generic stdio MCP client +
  per-tool adapters that measure every tool in `docs/comparison.md` — unblink,
  raw headless Chromium, Playwright MCP, Charlotte (npx, pinned versions), and
  Obscura/Lightpanda (external binaries, skipped with a note when absent) — on
  identical local fixtures. New metric family: **token cost per task**
  (`read-article` / `read-full` / `orient`), counted with an offline
  o200k_base BPE tokenizer, bytes alongside, every output sentinel-validated
  so an error can never score as token efficiency — and whole-document reads
  tail-verified, so a silent truncation can't either (this caught Obscura's
  ~4 KB snapshot cap). Plus a **content-boundary probe** (`-safety`) that
  measures what an untrusted page smuggles into the model — hidden-instruction
  text, image-beacon exfiltration, PDF handling, untrusted-content fencing —
  across each tool's read surface. And `-probe` (dump a
  server's tool list), `-selfcheck` (directional sanity assertions),
  `-dump-dir`, `-debug-mem`, and a generated provenance header.
  `docs/comparison.md`'s measured sections are now produced by this harness.
- **golangci-lint** as the linter (`.golangci.yml`, `make lint`, CI step):
  default linter set, errcheck relaxed for idiomatic `Close` and test HTTP
  handlers, `third_party/` exempt. Its first run removed one piece of dead
  code (`js.Context.settleThenClose`).

### Fixed

- **Off-screen hidden text leaked through the default article read.** The
  safe-output hidden-strip ran *after* readability extraction, but readability
  drops inline `style` attributes while cleaning — erasing the
  off-screen-positioning signal (`left:-9999px`, `clip:rect(0…)`) the strip
  keys on, so injected "screen-reader" text survived into `read`'s default
  article Markdown (`display:none`/`aria-hidden`/`hidden` were already stripped,
  and `mode:"full"` stripped all channels). The strip now also runs *before*
  readability, on a clone, guarded by a cheap `dom.HasHidden` pre-scan so pages
  with no hidden nodes skip the copy (article hot path unchanged: +0.0% B/op
  on hidden-free pages). Surfaced by the cross-tool content-boundary probe.

### Changed

- Removed the Go Report Card badge — the service is retired (it recommends
  golangci-lint, adopted above). GitHub Actions bumped to current majors
  (checkout v7, goreleaser-action v7, docker actions v4; all Node 24).

## v0.17.1 — go install fix

### Fixed

- `go install github.com/christopherdavenport/unblink/cmd/unblink@latest`
  works: the vendored PDF fork is now imported by its in-repo path
  (`…/unblink/third_party/pdf`) instead of a `replace` directive, which
  `go install <module>@version` refuses
  ([ADR-0001](docs/decisions/0001-pdf-extraction-library.md), Amendment 2).

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
- **Docker images**: multi-arch (`linux/amd64` + `linux/arm64`, distroless
  static, nonroot) images published to `ghcr.io/christopherdavenport/unblink`
  (`:X.Y.Z` and `:latest`) by goreleaser on every tag.
- **MCP registry listing**: `io.github.ChristopherDavenport/unblink` —
  `server.json` plus a publish workflow that stamps the version from the tag,
  waits for the ghcr image to be pullable, and publishes via `mcp-publisher`
  with GitHub OIDC.
- **Claude Code plugin manifest** (`.claude-plugin/plugin.json`): a docker-run
  MCP server entry whose image tag is auto-synced to each release by a bot
  commit on `main`.
- **Release pipeline hardening**: the test suite + eval gate now block
  goreleaser on tag pushes; GitHub Releases get auto-generated notes; README
  badges; issue/PR templates; Dependabot for Go modules and Actions (goja pins
  excluded per [ADR-0002](docs/decisions/0002-dependency-pinning-policy.md)).

### Changed

- `navigator.webdriver` is `true` by default (unblink is automation and
  self-identifies); `--tls-mimic` flips it to `false` for fingerprint parity.
- The eval gate grew from 40 to 47 must-pass cases, now covering the
  starved-render diagnostics, `net_denied`, keydown, Svelte, Lit, and a tier-1 API
  smoke page.

See the [Phase 21 section of docs/architecture.md](docs/architecture.md) for the
full design notes.
