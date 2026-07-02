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
runs the gofmt check plus `make vet test eval` on every push/PR; there is no
separate linter config — `gofmt` + `go vet` are the static tooling.

```sh
make build     # go build (version stamped from the nearest v* tag)
make test      # go test ./... (includes every fuzz target's seed corpus)
make eval      # offline in-process MCP eval gate (build tag `eval`; scorecard on stderr)
make fuzz      # coverage-guided fuzzing of the untrusted-input parsers (FUZZTIME=15s each)
make bench     # perf benchmarks over the hot-path packages (narrow: BENCH=regexp, repeat: BENCHCOUNT=N)
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
Include the benchstat table in the PR. JS render benchmarks each pay the
~60ms settle floor by design — engine wins appear as absolute deltas.

**Go-version gotcha:** the MCP SDK needs Go ≥ 1.25. The Makefile exports
`GOTOOLCHAIN=auto` so `go` downloads the toolchain pinned in `go.mod`
automatically. If you invoke `go` directly instead of via `make`, prefix it with
`GOTOOLCHAIN=auto`.

Drive the server by hand (MCP over stdio) — see the JSON-RPC snippet in
`README.md`. `./bin/unblink --js` enables opt-in JavaScript rendering; `--version`
prints the version.

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
- `internal/fetch` — HTTP-client-as-browser: cookies, redirects, brotli/gzip/deflate, charset→UTF-8, rate limit, retries, optional utls TLS mimic.
- `internal/dom` — **the only importer of `x/net/html`/`cascadia`**: parse, extraction, find.
- `internal/reduce` — readability extraction + bluemonday sanitize (`Article` vs `Full`); `Full` also suppresses repeated link-dense blocks (desktop nav + mobile-drawer twin) via `dom.StripDuplicateBlocks`.
- `internal/emit` — serialize reduced content to Markdown + heading outline.
- `internal/tokens` — token estimation + Markdown cursor pagination.
- `internal/session` — per-session cookie jar + navigation history + a live `js.LiveContext` (persistent JS runtime) bound to the current page; navigation/eviction/close tears it down (manager `onEvict` hook).
- `internal/js` — quarantined goja + eventloop DOM bridge (scripts, ESM, fetch/XHR, events, cookies). One-shot `Render` *and* persistent `Context`/`LiveContext` (true sessions). Opt-in. Defines its own `Transport`/`CookieJar` interfaces and **never imports `internal/fetch`**; the browser supplies adapters (`internal/browser/transport.go`).
- `internal/robots` — exposure-grade robots.txt (REP) parser: groups, `*`, Allow/Disallow (`*`/`$`), Crawl-delay, Sitemaps. Pure stdlib, **no enforcement** — unblink surfaces rules as context, never gates a fetch.
- `internal/browser` — the orchestrator that wires the pipeline + sessions. **The only package `mcpserver` calls.** Owns the host-scoped robots.txt/llms.txt cache (`sitecache.go`, `site.go`).
- `internal/mcpserver` — thin MCP adapter: tool registration + handlers + transport.

**No MCP types appear below `internal/mcpserver`**, and no browser logic lives in
it — that keeps the engine transport-agnostic and the SDK swappable.

## Conventions & gotchas

- **stdout is reserved for MCP JSON-RPC.** All logging goes to stderr via `slog`
  (`--log-level`). Never print to stdout outside the MCP transport.
- **Never return an unbounded blob.** `read` defaults to article-mode extraction
  and paginates via an opaque cursor; whole-page Markdown can be huge.
- reduce/emit run on a **copy** of the page so shared (cached or session) pages
  are never mutated — see `Browser.Read`.
- New capability code goes in the relevant `internal/<pkg>`; route everything to
  the MCP layer through `internal/browser`, not directly.
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
  `dslipak/pdf` but **vendors it at `third_party/pdf`** (a `replace` directive)
  with three loop-bounding patches after fuzzing found an infinite-loop DoS
  that recover+ctx couldn't contain — sync upstream manually, re-fuzz before
  adopting; 0002 is the dependency pinning policy (goja/goja_nodejs
  pseudo-version pins are deliberate — bumping goja is its own reviewed
  change); 0003 is the JS memory guard (process-level heap watchdog +
  `debug.SetMemoryLimit`, because goja has no per-runtime accounting). Add a new
  ADR when a decision would otherwise live only in a PR description.
  (`reference/` and `internal/config/` were empty scaffolding, deleted in
  Phase 20.)
- **Framework rendering (flat-DOM model)**: under `--js` the engine renders
  mainstream SPA frameworks (React/Vue/Preact/Svelte 4/Lit) — a real Node/Element
  prototype chain, MutationObserver, custom-element upgrade, and a *flattened*
  (non-encapsulating) Shadow DOM whose content is visible to extraction. **Still
  permanent non-goals** (no layout engine): real *element* layout/geometry and
  CSSOM (constant-stubbed to zeros/empty, never computed), canvas/WebGL,
  Workers/WebSocket/IndexedDB, and true Shadow-DOM encapsulation. The **viewport
  environment** is the one exception (Phase 21): `innerWidth`/`screen`/
  `devicePixelRatio` are a truthful constant 1280×720@1x and `matchMedia`
  evaluates against it, so responsive code takes its real branch. Untrusted page
  JS is also bounded on heap (`--js-memory-limit`, ADR 0003), time, network, and
  live-runtime count. When a bundle trips a goja *interpreter* bug (a Go panic,
  not a JS exception) — e.g. **Svelte 5**'s Boundary class hits goja's
  `definePrivateProp` assertion — `bridge.runProgram` recovers it into a
  `js_errors` diagnostic and keeps rendering, so it degrades visibly instead of a
  silent blank. See `docs/architecture.md` (Phases 8 and 21).
