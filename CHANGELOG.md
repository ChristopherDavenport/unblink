# Changelog

All notable changes are recorded here. Earlier history lives in the phase log of
[docs/architecture.md](docs/architecture.md).

## Unreleased

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
  8 → 16 to match. Politeness defaults (`--rate-limit`) are untouched.
- **Crossbench was measuring unblink's politeness limiter, not its engine**:
  the adapter never passed `--rate-limit`, so the single-host cache-busted
  corpus paced every render at ~200 ms and pinned sequential throughput at
  exactly 5 pages/s. The adapter now runs `--rate-limit 0` (recorded as the
  one deviation from tool defaults in the fairness rules — no other
  benchmarked tool ships a crawl-politeness limiter), and
  `docs/comparison.md` now also publishes the concurrent-throughput row.
  Re-measured medians: SPA renders ~2–10 ms (was ~200 ms), ~249 pages/s
  sequential / ~827 pages/s at 8-way concurrent (was ~5 pages/s).

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
