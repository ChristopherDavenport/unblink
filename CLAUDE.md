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

The `Makefile` is the canonical task runner (there is no CI and no linter config;
`gofmt` + `go vet` are the only static tooling).

```sh
make build     # go build -o bin/unblink ./cmd/unblink
make test      # go test ./...
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
- `internal/reduce` — readability extraction + bluemonday sanitize (`Article` vs `Full`).
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
  integration-level test of the whole pipeline.
- `reference/` and `docs/decisions/` are mostly-empty **scaffolding** today:
  `reference/README.md` describes an intended curated library (every artifact
  pinned in `PINNED.md`, summaries over vendored source) but the subdirs and
  most files don't exist yet; `docs/decisions/` has no ADRs. Don't assume they
  exist.
- **Framework rendering (flat-DOM model)**: under `--js` the engine renders
  mainstream SPA frameworks (React/Vue/Preact/Svelte/Lit) — a real Node/Element
  prototype chain, MutationObserver, custom-element upgrade, and a *flattened*
  (non-encapsulating) Shadow DOM whose content is visible to extraction. **Still
  permanent non-goals** (no layout engine): real layout/geometry (constant-stubbed
  to zeros/empty, never computed), canvas/WebGL, Workers/WebSocket/IndexedDB, and
  true Shadow-DOM encapsulation. See `docs/architecture.md` (Phase 8).
