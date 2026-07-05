# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

unblink is a pure-Go (no cgo, no Chromium) MCP server that exposes the web to AI
agents. It fetches a page, parses HTML5, optionally runs the page's JavaScript,
strips visual-only junk, and returns clean, token-budgeted Markdown over the
Model Context Protocol (stdio). The job is *semantic reduction*, not rendering.

The single binary is `cmd/unblink`. The authoritative design doc is
`docs/architecture.md` — read it before any non-trivial change.

## Commands

The `Makefile` is the canonical task runner. CI (`.github/workflows/ci.yml`)
runs the gofmt check, `make vet`, golangci-lint (`.golangci.yml` — default
linters, errcheck relaxed for idiomatic Close/test handlers, `third_party/`
exempt), then `make test eval` on every push/PR. `make lint` runs the same
lint locally (needs the golangci-lint binary).

```sh
make build     # go build (version stamped from the nearest v* tag)
make test      # go test ./... (includes every fuzz target's seed corpus)
make eval      # offline in-process MCP eval gate (build tag `eval`; scorecard on stderr)
make fuzz      # coverage-guided fuzzing of the untrusted-input parsers (FUZZTIME=15s each)
make bench     # perf benchmarks over the hot-path packages (narrow: BENCH=regexp, repeat: BENCHCOUNT=N)
make membench  # unblink vs. headless Chromium footprint/latency (scripts/membench, separate module)
make crossbench # membench across every tool in docs/comparison.md incl. token cost (TOOLS=, ARGS=)
make vet       # go vet ./...
make fmt       # gofmt -w .
make tidy      # go mod tidy
make run       # build, then serve MCP over stdio
```

Run a single test (not in the Makefile — standard Go):

```sh
GOTOOLCHAIN=auto go test ./internal/js -run TestName -v
GOTOOLCHAIN=auto go test ./internal/dom -run TestExtract/subtest -v
```

Proving a performance change: `make bench > /tmp/base.txt` on the baseline
commit, apply the change, `make bench > /tmp/new.txt`, then
`go run golang.org/x/perf/cmd/benchstat@latest /tmp/base.txt /tmp/new.txt`.
Include the benchstat table in the PR. JS renders settle on two tiers (ADR
0004): provably-idle pages (no in-flight network, no live timers) close in
~1-3ms; anything armed pays the ~60ms quiet window after its last activity.

**Go-version gotcha:** the MCP SDK needs Go ≥ 1.25. The Makefile exports
`GOTOOLCHAIN=auto` so `go` downloads the toolchain pinned in `go.mod`
automatically. If you invoke `go` directly instead of via `make`, prefix it with
`GOTOOLCHAIN=auto`.

Drive the server by hand (MCP over stdio) — see the JSON-RPC snippet in
`README.md`. JavaScript rendering is **on by default** (reads render unless the
caller passes `render=false`); `./bin/unblink --disable-js` turns it off for the
static-only path. `--version` prints the version.

## Architecture

One `page.Page` (`internal/page/page.go`) flows through the pipeline and is
enriched **in place** — stages never re-fetch or re-parse:

```
fetch ──▶ dom.Parse ──▶ [js render] ──▶ dom.Extract ──▶ reduce ──▶ emit
```

Three load-bearing decisions hold the design together:

1. **The `*html.Node` tree (`Page.Doc`) is the single source of truth.** Every
   stage reads or mutates that one tree; goquery/cascadia are *views* over it,
   not a second model. When JS runs it mutates the *same* tree, so reduce/emit
   never need to know whether JS ran (`Page.Rendered` records that it did).
2. **Hybrid statefulness.** Every page tool takes an optional `session`. With
   one: cookies + history + current page persist, and `interact` keeps a *live JS
   runtime* alive for the page (`internal/session`). Without: a throwaway one-shot
   fetch. Keep `read(url)` trivial while supporting `click`/`submit_form`/`interact`.
3. **goja is quarantined** behind the `browser.Renderer` interface, so the JS
   engine stays optional and swappable. A `*goja.Runtime` is single-goroutine and
   never shared: a one-shot `Render` builds and discards one per call (bounded by a
   semaphore + prewarm pool); a persistent `js.Context` (`Open`/`Dispatch`/
   `Snapshot`/`Close`, for true sessions) keeps one alive on its own event-loop
   goroutine — all access marshalled via `RunOnLoop`, reads via on-loop snapshots.
   It is the only single-threaded island.

### Package boundaries (enforced dependency direction)

`page` → capability packages → `browser` → `mcpserver`. Respect these:

- `internal/page` — core data model (`Page`, `Article`, `Link`, `Form`…). Pure types.
- `internal/fetch` — HTTP-client-as-browser: cookies, redirects, brotli/gzip/deflate, charset→UTF-8, opt-in per-host rate limit (`--rate-limit`, default off), retries, optional utls TLS mimic.
- `internal/ratelimit` — process-global, per-host token-bucket limiter behind `--rate-limit` (off by default); `fetch` clients share it.
- `internal/dom` — **the only importer of `x/net/html`/`cascadia`**: parse; extraction (regions/landmarks, interactive ARIA state, stable content-hash ids, heading outline); caller-directed schema extraction (`dom.Records`); repeating-collection discovery (`dom.detectCollections`); find.
- `internal/content` — converts non-HTML bodies to Markdown: JSON and plain text inline, plus the `feed`/`image`/`pdf` subpackages (RSS/Atom/JSON feeds, image manifests, PDF text via `third_party/pdf`).
- `internal/reduce` — readability extraction + bluemonday sanitize (`Article` vs `Full`); `Full` also suppresses repeated link-dense blocks (desktop nav + mobile-drawer twin) via `dom.StripDuplicateBlocks`.
- `internal/emit` — serialize reduced content to Markdown + heading outline.
- `internal/tokens` — token estimation + Markdown cursor pagination.
- `internal/session` — per-session cookie jar + navigation history + a live `js.LiveContext` (persistent JS runtime) bound to the current page; navigation/eviction/close tears it down (manager `onEvict` hook).
- `internal/js` — quarantined goja + eventloop DOM bridge (scripts, ESM, fetch/XHR, events, cookies). One-shot `Render` *and* persistent `Context`/`LiveContext` (true sessions). Opt-in. Defines its own `Transport`/`CookieJar` interfaces and **never imports `internal/fetch`**; the browser supplies adapters (`internal/browser/transport.go`).
- `internal/robots` — exposure-grade robots.txt (REP) parser: groups, `*`, Allow/Disallow (`*`/`$`), Crawl-delay, Sitemaps. Pure stdlib, **no enforcement** — unblink surfaces rules as context, never gates a fetch.
- `internal/sitemap` — pure-stdlib sitemaps.org XML decoder (`<urlset>`/`<sitemapindex>`, gzip-aware) behind the `map` tool. No fetch, no `x/net/html`.
- `internal/search` — optional web search behind a `Provider` interface (SearXNG + Brave adapters); off unless `--search-provider` is set, injected into `browser`.
- `internal/webext` — models MV2/MV3 WebExtensions (manifest, match patterns, a tokenized no-ReDoS Adblock `urlFilter` matcher, dir/archive loaders with traversal + zip-bomb guards) so unblink can runtime-load one (e.g. uBlock Origin Lite). Pure Go; imports neither `js` nor `browser` — the direction is `browser → js → webext`. The JS-side surface lives in `internal/js/ext*.go`.
- `internal/browser` — the orchestrator that wires the pipeline + sessions. **The only package `mcpserver` calls.** Owns the host-scoped robots.txt/llms.txt cache (`sitecache.go`, `site.go`), the `map`/`search` discovery surface, and the engine-lifetime `ExtensionHost` (shared read-only across renders/sessions).
- `internal/mcpserver` — thin MCP adapter: tool registration + handlers + transport.

**No MCP types appear below `internal/mcpserver`**, and no browser logic lives in
it — that keeps the engine transport-agnostic and the SDK swappable.

## Security posture

unblink **executes untrusted page JavaScript**, so it treats the page's own code as
hostile and applies the browser's security model as **defaults** over it. The
guiding principle mirrors a browser + its devtools: **gate the page, not the
operator** — a blocked cross-origin request is still *sent and logged* (visible to
the agent via the `requests` tool); only the page-JS *read* is denied. The layers
(each detailed in an ADR; escape hatches are opt-in-to-*less*-secure, default off):

- **SSRF guard** (default on): no fetch — primary, session, one-shot, or page-JS
  subrequest — reaches a private/loopback/metadata IP (checked on the *resolved*
  IP). Hatches `--allow-private`, `--js-allow-private`. (Detailed gotcha below.)
- **Same-Origin Policy** (ADR 0015): SOP is the default. Cross-origin *network
  reads* go through CORS; cookies are origin/domain-scoped (public-suffix jar,
  HttpOnly hidden from `document.cookie`); **Web Storage is origin-partitioned** per
  session (`internal/session`). The cross-*document* half (iframes/`contentWindow`,
  `window.open`/`opener`/`frames`, `postMessage`, `document.domain`, `window.name`)
  is **moot by architecture** — single-document, one runtime per render, no
  reachable foreign document — a deliberate non-goal.
- **CORS** (ADR 0011, default on): cross-origin `fetch`/XHR over page JS obeys
  `Access-Control-*` + preflight; non-credentialed cross-origin drops cookies;
  `no-cors` yields an opaque response. `internal/js/cors.go` (`doFetch`); hatch
  `--js-allow-cross-origin`.
- **CSP** (ADR 0014, default on): the document's `Content-Security-Policy` (header +
  `<meta>`) is enforced over page JS — `script-src` nonce/hash/host + `strict-dynamic`,
  `connect-src`, `unsafe-eval` (via `EvalError` shims); report-only surfaces without
  blocking. `internal/js/csp.go`; hatch `--no-csp`.
- **SRI** (ADR 0012, default on): `integrity`-pinned scripts/modules are hashed over
  the *raw pre-transcode* bytes and blocked on mismatch. `internal/js/sri.go`; hatch
  `--no-sri`.
- **Injected-credential origin scoping** (Phase 10): bearer/basic/custom headers go
  only to their configured origin, stripped on cross-origin redirect. (Gotcha below.)
- **Truthful request/isolation signals** (ADR 0013): per-context `Sec-Fetch-*` on
  every request; truthful `window.crossOriginIsolated`/`isSecureContext` from
  COOP/COEP (fidelity, not enforcement).
- **Resource bounds on untrusted JS**: process heap watchdog + `SetMemoryLimit`
  (ADR 0003, `--js-memory-limit`), per-render/dispatch download-**bytes** budget
  (ADR 0009, `--js-max-bytes`), wall-clock render budget + timer clamp, live-runtime
  LRU cap.

Shared implementation pattern for the JS-side policies: an `Env` field
(`AllowCrossOrigin`/`DisableSRI`/`DisableCSP`/`ResponseHeaders`) → a bridge field →
the enforcement hook, with violations recorded via `recordError` → render
diagnostics. **Gotcha: `recordError` appends without a lock — only call it on the
loop goroutine** (inside `RunOnLoop` or an on-loop native handler), never from an
off-loop fetch goroutine (this is why `connect-src` is checked in `fetchPromise`,
not `doFetch`).

## Conventions & gotchas

- **stdout is reserved for MCP JSON-RPC.** All logging goes to stderr via `slog`
  (`--log-level`). Never print to stdout outside the MCP transport.
- **Never return an unbounded blob.** `read` defaults to article-mode extraction
  and paginates via an opaque cursor; whole-page Markdown can be huge.
- reduce/emit run on a **copy** of the page so shared (cached or session) pages
  are never mutated — see `Browser.Read`.
- New capability code goes in the relevant `internal/<pkg>`; route everything to
  the MCP layer through `internal/browser`, not directly.
- **Tool surface** (all routed through `internal/browser`, registered in
  `internal/mcpserver`): **18 tools**. Beyond `read`/`browse`/`links`/`forms`/
  `find`/`site`/`click`/`submit_form`/`controls`/`interact`/`data`/`session`/
  `map`/`search`, the recent additions are `extract` (caller-directed CSS-schema
  extraction, `dom.Records`), `browse`'s `collections` (auto-proposed `extract`
  schemas, `dom.detectCollections`), and the inspection tools `requests`/
  `console`/`cookies`. Operators narrow the advertised set with `--tools`/
  `--disable-tools` (presets `core`/`read-only`/`full`), and capability-unusable
  tools auto-hide (`search` without a provider; `interact`/`requests`/`console`
  under `--disable-js`).
- **SSRF guard**: every page fetch — the primary, per-session, and one-shot
  clients *and* page-JS subrequests — is blocked from reaching
  private/loopback/metadata IPs (checked against the *resolved* IP). On by default
  (`ssrfControl` in `internal/browser/transport.go`, wired into `base` in
  `browser.New` + the JS transports); escape hatches `--allow-private` (page
  fetches) and `--js-allow-private` (JS subrequests, also has a per-render budget).
- **Injected credentials are origin-scoped** (Phase 10): `fetch`'s
  `WithBearer`/`WithBasicAuth`/`WithHeaders` apply only to requests matching
  `WithCredentialScope`'s origin and are stripped on any cross-origin redirect
  (`Client.checkRedirect`) — a bearer/API-key never leaks to another host. Session
  auth lives in `session.Config`; secrets resolve via `token_env`/`password_env`
  in `internal/mcpserver/auth.go` and are never logged or echoed in session state.
- Tests use external `_test` packages with fixtures in `testdata/`. `internal/js`
  carries the densest suite; `internal/browser/browser_test.go` is the
  integration-level test of the whole pipeline. Every parser that eats untrusted
  bytes (dom, reduce, pdf, robots, token cursors) has a fuzz target — `make fuzz`
  (FUZZTIME per target); seed corpora run in `make test`.
- `docs/decisions/` holds the ADRs (numbered `NNNN-slug.md`): 0001 keeps
  `dslipak/pdf` but **vendors it at `third_party/pdf`** (imported by its
  in-repo path `…/unblink/third_party/pdf` — never via a `replace` directive,
  which would break `go install <module>@version`) with three loop-bounding
  patches after fuzzing found an infinite-loop DoS that recover+ctx couldn't
  contain — sync upstream manually, re-fuzz before adopting; 0002 is the dependency pinning policy (goja/goja_nodejs
  pseudo-version pins are deliberate — bumping goja is its own reviewed
  change); 0003 is the JS memory guard (process-level heap watchdog +
  `debug.SetMemoryLimit`, because goja has no per-runtime accounting); 0004 is the
  provable-idle settle invariant (every new async primitive must route through the
  timer audit / `pending` bracket); 0005 is the composed Shadow DOM (flattened tree
  + cross-boundary events); 0006 is the broad Go-backed `crypto.subtle` (reversing
  the earlier "leave undefined", since real pages call it on first paint); 0007 is
  the semantic-only structured representation (computed from the node tree, never a
  layout — spatial/bounding-box geometry is a permanent non-goal); 0008 is the stable
  content-hash element ids (a reference/cache key, not an `interact` target — interact
  still uses CSS selectors); 0009 is the
  move to resource-based JS budgets — a per-render/per-dispatch **byte** budget
  (`--js-max-bytes`, default 64 MiB) replaces the fixed request-count cap (now an
  off-by-default backstop) and the removed 60s live-session rate window, plus
  concurrent `<link rel=modulepreload>` warming so code-split SPAs' serial
  `import()` graph loads as cache hits (adds no async primitive, so ADR 0004
  holds); 0010 is WebExtensions runtime loading (the operator supplies the
  extension; MIT/GPL separation, no vendored extension bytes — see the
  WebExtensions gotcha above); 0011 is CORS enforcement over untrusted page JS
  (default on, enforced in `internal/js/cors.go` `doFetch`; the request is still
  sent + logged in `requests`, only the page-JS *read* is gated — history
  preservation; `--js-allow-cross-origin` opts out); 0012 is Subresource Integrity
  (default on, `internal/js/sri.go`; hashes the raw pre-transcode bytes — hence the
  `Raw` field on `fetch.Result`/`js.Response`; `--no-sri` opts out); 0013 is
  Fetch-Metadata + cross-origin isolation fidelity (truthful per-context
  `Sec-Fetch-*` on primary + every subrequest via `internal/js/secfetch.go`,
  decoupled from `--tls-mimic`; truthful `window.crossOriginIsolated`/
  `isSecureContext` from COOP/COEP in `internal/js/isolation.go`, no COEP
  enforcement — the document response headers now reach the engine via
  `Env.ResponseHeaders`); 0014 is Content-Security-Policy enforcement over
  untrusted page JS (default on, `internal/js/csp.go`: header + `<meta>` parsing,
  script-src nonce/hash/host incl. `strict-dynamic`, connect-src on fetch/XHR,
  `unsafe-eval` via `EvalError` shims; report-only surfaces without blocking; a
  resource must satisfy every enforced policy; `--no-csp` opts out); 0015 is the
  Same-Origin-Policy posture (SOP is the default: network reads via CORS, cookies,
  and now origin-partitioned Web Storage are isolated; the DOM/window/frame half is
  moot-by-architecture — single-document, one runtime per render — so it's a
  deliberate non-goal. The one behavior change was origin-keying localStorage/
  sessionStorage in `internal/session`). Add a new ADR when a decision would
  otherwise live only in a PR description.
  (`reference/` and `internal/config/` were empty scaffolding, deleted in
  Phase 20.)
- **WebExtensions (opt-in, ADR 0010)**: `--extension <dir|.xpi|.crx|.zip>` /
  `--extensions-dir` runtime-load a real, unmodified extension — unblink ships
  **no** extension bytes, keeping GPL tooling (uBlock Origin is GPL-3) out of the
  MIT tree the way a browser loads a user add-on. The model lives in
  `internal/webext`; the engine surface (the `chrome`/`browser` API, content
  scripts, cosmetic filtering as physical node removal, background worker +
  runtime messaging, static/dynamic `declarativeNetRequest`, and MV2
  `webRequest` blocking) in `internal/js/ext*.go`. Extensions are **rejected
  under `--disable-js`** and run in the same heap/byte/SSRF sandbox as page JS.
  **uBlock Origin Lite (MV3/DNR) is verified working** (18,249 host-evaluated
  rules, blocks real trackers, ~0.1s, no service worker); *full* uBO (MV2) is
  not viable in-process (compiling its filter lists in goja is too slow).
- **Framework rendering (flat-DOM model)**: with its JavaScript engine (on by
  default; `--disable-js` opts out) the engine renders
  mainstream SPA frameworks (React/Vue/Preact/Svelte/Lit) — a real Node/Element
  prototype chain, MutationObserver, custom-element upgrade, and an
  **encapsulating, composed Shadow DOM** (Phase 23, ADR 0005): each shadow root is a
  detached subtree (page-JS `querySelector` respects the boundary), and a compose pass
  flattens it — resolving `<slot>` distribution — into the light tree for extraction.
  Events cross the boundary correctly (composed path + `target` retargeting +
  `composedPath()`); declarative Shadow DOM (`<template shadowrootmode>`) is flattened
  on the static no-JS path. **Still permanent non-goals** (no layout engine): real
  *element* layout/geometry and CSSOM (constant-stubbed to zeros/empty, never
  computed), canvas/WebGL, Workers/WebSocket/IndexedDB, and Shadow-DOM *style scoping*
  (`:host`/`::slotted`/`::part`) / slot reprojection / closed-mode privacy from
  extraction. The **viewport
  environment** is the one exception (Phase 21): `innerWidth`/`screen`/
  `devicePixelRatio` are a truthful constant 1280×720@1x and `matchMedia`
  evaluates against it, so responsive code takes its real branch. Untrusted page
  JS is also bounded on heap (`--js-memory-limit`, ADR 0003), time, network bytes
  (`--js-max-bytes`, ADR 0009), and live-runtime count. See `docs/architecture.md`
  (Phases 8, 21, and 23).
