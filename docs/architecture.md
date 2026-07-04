# unblink architecture

unblink is a pure-Go (no cgo, no Chromium) tool that exposes the web to AI agents
over MCP. Its job is semantic reduction, not rendering: turn a page into the
meaning an agent can reason over, and throw away everything that exists only for
human eyes.

## Spine: three load-bearing decisions

1. **The canonical document is always the `*html.Node` tree** from
   `golang.org/x/net/html`. Every stage reads or mutates that one tree;
   goquery/cascadia are *views* over it, not a second model. When the JS phase
   lands it mutates the *same* tree, so `reduce`/`emit` never need to know
   whether JS ran.
2. **Hybrid statefulness.** Every tool will accept an optional `session`. With a
   session: cookies + history + current page persist. Without: a throwaway
   one-shot fetch. This keeps `read(url)` trivial while supporting
   `click`/`submit_form` flows.
3. **goja is quarantined** (one `*goja.Runtime` per goroutine, never shared). A
   one-shot `Render` builds and discards a runtime per call (bounded by a semaphore
   + prewarm pool); a persistent `js.Context` keeps one alive for a whole session
   (true sessions) on its own `eventloop` goroutine — every operation, *including
   DOM reads* (via on-loop `Snapshot` → bytes), is marshalled through `RunOnLoop`,
   and the loop-owned live tree is never touched off-loop. The JS boundary is the
   only single-threaded island; everything else is normal concurrent Go.

## Pipeline

```
fetch ──▶ dom.Parse ──▶ reduce ──▶ emit ──▶ (mcpserver)
  │           │            │          │
  ▼           ▼            ▼          ▼
transport   Doc         Article    Markdown      (all fields of one *page.Page)
fields +   (*html.Node)
Raw (UTF-8)
```

One `page.Page` flows through and is progressively enriched; stages never
re-fetch or re-parse. See `internal/page/page.go`.

## Packages

| Package              | Responsibility                                                        |
| -------------------- | --------------------------------------------------------------------- |
| `internal/page`      | Core data model (`Page`, `Article`, `Link`, `Form`, …). Pure types.   |
| `internal/fetch`     | HTTP client-as-browser: cookies, redirects, gzip, charset → UTF-8, UA.|
| `internal/dom`       | The only importer of `x/net/html`/`cascadia`: parse + extraction + find.|
| `internal/reduce`    | Semantic reduction: readability extraction + bluemonday sanitize. Full mode also drops repeated link-dense blocks (`dom.StripDuplicateBlocks` — desktop nav + its mobile-drawer twin emit once). |
| `internal/emit`      | Serialize reduced content to Markdown + heading outline.              |
| `internal/session`   | Per-session cookie jar (own `fetch.Client`) + navigation history + per-page interaction replay log (imports `js` for `js.Action`). |
| `internal/robots`    | Exposure-grade robots.txt (REP) parser: groups, `*`, Allow/Disallow (`*`/`$`), Crawl-delay, Sitemaps. No external deps; **never enforces**. |
| `internal/sitemap`   | Pure-stdlib sitemaps.org XML decoder (`<urlset>`/`<sitemapindex>`, gzip-aware). No fetch, no `x/net/html`. |
| `internal/search`    | Optional web-search behind a `Provider` interface (SearXNG + Brave JSON adapters). Off by default; injected into `browser`. |
| `internal/browser`   | Orchestrator. Wires the pipeline + sessions; the only thing `mcpserver` calls. Holds a host-scoped robots.txt/llms.txt cache (`sitecache.go`) and the sitemap/crawl `map` + `search` surface (`discover.go`).|
| `internal/mcpserver` | Thin MCP adapter: tool registration + handlers + transport.           |
| `internal/js`        | goja + eventloop DOM bridge over the `*html.Node` tree: scripts/ESM, fetch/XHR, events, a real prototype chain + MutationObserver + custom elements / composed Shadow DOM (slots + cross-boundary events) so React/Vue/Lit render (Phases 8, 23). Quarantined. |
| `internal/tokens`    | Token estimation + Markdown cursor pagination.                        |

Dependency direction: `page` → capability packages (`fetch`/`dom`/`reduce`/`emit`/`tokens`/`session`/
`js`/`robots`/`sitemap`/`search`) → `browser` → `mcpserver`. No MCP type leaks below `mcpserver`. The `js` engine sits behind a
`browser.Renderer` interface, so it stays optional and swappable (e.g. for `gost-dom` later).

## Key dependencies (all pure Go)

- Parse/extract: `golang.org/x/net/html` + `github.com/andybalholm/cascadia` (selectors)
- Reduce: `codeberg.org/readeck/go-readability/v2`, `github.com/microcosm-cc/bluemonday`
- Emit: `github.com/JohannesKaufmann/html-to-markdown/v2`
- MCP: `github.com/modelcontextprotocol/go-sdk` (official; requires Go ≥ 1.25)
- JS: `github.com/dop251/goja` + `github.com/dop251/goja_nodejs` (eventloop) — pure Go, no cgo
- JS modules: `github.com/evanw/esbuild/pkg/api` (bundles ESM → classic IIFE) — pure Go, no cgo
- Robustness: `github.com/andybalholm/brotli` (decode), `golang.org/x/time/rate` (per-host limit),
  `github.com/refraction-networking/utls` (TLS mimic, opt-in) — pure Go, no cgo

## Roadmap

- **Phase 0 — Scaffold.** ✅ Module, layout, MCP server, `reference/`.
- **Phase 1 — Static read path.** ✅ `fetch → parse → reduce → emit`, one `read`
  tool over stdio.
- **Phase 2 — Structure & navigation.** ✅ `browse`, `links`, `find`, `forms`;
  metadata/link/form/heading extraction (`internal/dom`); a short-TTL page cache
  (`internal/browser/cache.go`); token estimation + cursor pagination
  (`internal/tokens`) on `read` (`mode=article|full`).
- **Phase 3 — Sessions & forms.** ✅ Lazy client-named sessions with per-session
  cookie jar + navigation history (`internal/session`); `session` threaded through
  all page tools (`session`/`use_current`); new `click`, `submit_form`, `session`
  tools. Eight tools total.
- **Phase 4a — JS / DOM bridge (first slice).** ✅ Hand-rolled goja +
  `goja_nodejs/eventloop` DOM bridge over the live `*html.Node` tree (mutated in
  place); runs inline scripts, settles async, wall-clock interrupt guard.
  On by default — toggled per request (`render`, default on) and per server
  (`--disable-js`).
- **Phase 4b — Real JS networking & lifecycle.** ✅ External `<script src>`
  (document order); `window.fetch` + `XMLHttpRequest` routed through a guarded
  transport (one `__unblinkFetch` Go primitive with the race-free inline-keepalive
  pattern); `DOMContentLoaded`/`load` event dispatch. SSRF guard (blocks private/
  loopback/metadata IPs) + per-render **byte** budget (`--js-max-bytes`, ADR 0009;
  `--js-max-requests` is an off-by-default count backstop); `--js-no-network`,
  `--js-allow-private`.
- **Phase 4c — ES modules.** ✅ `<script type=module>` (inline + src), `import`/
  `export`, dynamic `import()`, and import maps — bundled per module graph with
  esbuild (pure Go) via a network-resolver plugin routed through the guarded
  transport, then run as a classic IIFE on the goja path.
- **Phase 4d — Event propagation & cookies.** ✅ Full capture → target → bubble
  event dispatch (delegated listeners fire; `stopPropagation`/`preventDefault`/
  real `removeEventListener`), and `document.cookie` read/write backed by the
  session jar.
- **Phase 4e — Pre-warm runtime pool.** ✅ A background pool of fresh, single-use
  goja event loops keeps runtime-creation latency off the render critical path
  (no reuse → isolation unchanged; goja has no safe reset). Opt-in/tunable via
  `--js-prewarm`.
- **Phase 4f — Listener options & typed Events.** ✅ `once`/`passive`/`{signal}`
  addEventListener options, `AbortController`/`AbortSignal` (+ `fetch {signal}`),
  and typed Event constructors (`MouseEvent`/`KeyboardEvent`/…). **Phase 4 (the
  JS/DOM bridge) is feature-complete to its planned scope.**
- **Phase 5 — Robustness.** ✅ Brotli/gzip/deflate decode (`internal/fetch`);
  per-host rate limiting (`internal/ratelimit`) + retry-with-backoff (429/5xx,
  honoring `Retry-After`); structured `slog` logging to stderr; opt-in utls TLS
  fingerprint mimicry (`--tls-mimic`, fixes JA3). Flags: `--log-level`,
  `--rate-limit`/`--rate-burst`, `--retries`, `--tls-mimic`.
- **Phase 6 — Agent-facing site metadata.** ✅ robots.txt + llms.txt exposed as
  *context, never enforced* (`internal/robots`; `internal/browser/site.go`). A
  combined `site` tool returns a host's robots.txt summary (allow/disallow,
  crawl-delay, sitemaps, and `allowed_for_us` for the path) and its llms.txt guide
  inline, plus whether llms-full.txt exists; `browse` folds in lightweight presence
  hints. Both share a host-scoped TTL cache (`sitecache.go`) so repeat lookups are
  free. Because unblink presents a standard browser User-Agent (no bot token), the
  governing robots group is always `*`. `--no-site-hints` disables the browse probe.
- **Phase 7 — Generic DOM interaction + true sessions.** ✅ An `interact` tool
  dispatches a real DOM event (`click`/`input`/`change`/`keydown`/`submit`) at a
  CSS-selector-addressed element and runs the page's JavaScript so handlers fire and
  mutate the tree; a `controls` tool + `dom` extraction surface the non-anchor
  interactable elements (buttons, `[role=button]`, `[onclick]`, `[tabindex]`,
  submit/reset inputs, tabs, summaries) with stable selectors. Each session holds a
  **persistent JS runtime** (`js.Context`, opened lazily on first `interact`): the
  runtime, listeners, timers, and JS heap survive across calls — a true browser-tab
  session, not replay — so background timers/fetch keep running between calls and
  non-reproducible state (RNG seeds, timestamps, streamed data) is preserved. Reads
  of a live session refresh from an on-loop DOM snapshot. Settle on the never-draining
  loop is a network-idle + quiet-period heuristic (a pending-request counter); a
  runaway op is stopped with `vm.Interrupt` (not loop teardown) and the runtime
  reused after `ClearInterrupt`. Navigation/eviction/close/shutdown tear the runtime
  down. Dispatch drives plain-JS / event-delegation pages **and** framework-hydrated
  pages: the flat-DOM model (Phase 8) lets React/Vue/Lit actually mount, so the
  handlers they attach during hydration fire.
- **Phase 8 — Framework rendering (flat-DOM model).** ✅ Client-side rendering and
  hydration for the mainstream SPA frameworks (React, Vue, Preact, Svelte, and
  Lit / web components), on by default (opt out with `--disable-js`). The goja↔DOM bridge gained a real
  `Node`/`Element`/`HTMLElement`/`Text`/`Comment`/`DocumentFragment`/`Document`
  prototype chain (so `instanceof` and prototype-patching work; `internal/js/proto.go`),
  the DOM-tree APIs frameworks call at mount (`createComment`/`createDocumentFragment`/
  `createElementNS`/`cloneNode`/`replaceChild`/`insertAdjacent*`/`dataset`/`importNode`/
  `createTreeWalker`/`getAttributeNames`, and the `HTML*Element` interface globals;
  `domapi.go`/`treewalker.go`), a real `MutationObserver` + `queueMicrotask`, real
  `history.pushState`/`popstate` for client-side routing, custom-element upgrade plus
  an **encapsulating, composed** Shadow DOM — a detached shadow subtree flattened with
  `<slot>` distribution into the light tree for extraction (Phase 23, ADR 0005;
  `customelements.go`/`slots.go`) — and render diagnostics — detected framework + uncaught/async
  errors surfaced on `page.Page.RenderDiag` and `slog` debug (`diagnostics.go`).
  Validated against pinned real bundles (`testdata/frameworks`,
  `internal/js/frameworks_test.go`).

- **Phase 9 — raw_html escape hatch + structured data.** ✅ `read` gained a
  `format` arg — `markdown` (default) | `raw_html` | `text` — plus an optional CSS
  `selector` for `raw_html`. `raw_html` bypasses reduction and returns the page
  source: a `selector`'s matched subtrees, else the post-JS DOM when JavaScript ran
  (`page.Rendered`), else the exact fetched bytes (best fidelity for SSR-embedded
  JSON); relative URLs are left untouched. `text` returns visible plain text. All
  three serialize/select through `internal/dom` (`render.go`), the sole
  `x/net/html`/cascadia importer; binary kinds (image/pdf/binary) are refused. A new
  **`data`** tool extracts machine-readable structure — JSON-LD
  (`dom.JSONLD`, `@graph` flattened, malformed blocks skipped), HTML tables
  (`dom.Tables`, colspan expanded / rowspan ignored / nested folded), and microdata
  (`dom.Microdata`, nested itemscopes, `itemref` unsupported) — all read-only over
  `page.Doc`, bounded by size/count/depth caps. Twelve tools total.

- **Phase 10 — Auth, headers & cookies.** ✅ Credentialed traversal of gated pages
  and JSON APIs. `fetch` gained `WithHeaders`/`WithBearer`/`WithBasicAuth` +
  `WithCredentialScope`: injected credentials are **pinned to one origin**
  ("scheme://host") — added by `setHeaders` only to same-origin requests (never
  overriding `Accept-Encoding`), and stripped by a new `CheckRedirect` on any
  cross-origin hop (stdlib drops `Authorization`/`Cookie` cross-*domain* but not
  custom headers or cross-*port*). The `session` tool's `new` action takes
  `url` + `headers`/`cookies`/`auth` (bearer/basic; secrets literal or via
  `token_env`/`password_env` so they stay out of the transcript), configuring a
  per-session credentialed `fetch.Client` (cookies seeded into its jar);
  `read`/`browse` also take one-shot `headers`/`auth` for stateless gated GETs
  (fetched through a throwaway origin-scoped client, never cached). Session state
  reports only a redacted `auth_type`/`auth_scope`. In-page JS subrequests inherit
  the page client's origin-scoped credentials (`Client.CredentialOptions()` → the
  guarded transport). **Prerequisite funded here:** the SSRF dial guard
  (`ssrfControl`) now covers the primary/session/one-shot page clients, not just
  JS subrequests — direct fetches to private/loopback/metadata IPs are blocked by
  default, with `--allow-private` as the escape hatch (independent of
  `--js-allow-private`). A secret-redaction pass masks known credential query
  params in `slog` URLs. Still twelve tools (auth rides `session`/`read`/`browse`).

- **Phase 11 — Search & discovery (the cold-start capstone).** ✅ Two new tools
  close the "browser with no address bar" gap. **`map`** discovers a site's URLs:
  it harvests sitemap.xml (from robots.txt `Sitemap:` directives + the
  `/sitemap.xml` convention, following sitemap indexes) and crawls same-origin
  links breadth-first from a seed, returning a bounded, de-duplicated list tagged
  `source=sitemap|crawl` with crawl depth (`Browser.Map` in
  `internal/browser/discover.go`; a new pure-stdlib `internal/sitemap` XML decoder
  that gunzips a static `.xml.gz` itself). It is **exposure-grade** — it surfaces
  robots.txt as context but never skips disallowed paths — and same-origin is
  strict scheme+host (`originOfURL`, *not* `Link.Internal`'s registrable-domain).
  Bounds (`max_urls`/`max_depth`, sitemap fetch/depth caps, a 60s wall-clock) yield
  a partial result with `Truncated` set rather than an error; an off-origin
  redirect is recorded but not expanded (open-redirector guard). **`search`** runs
  a web query behind an opt-in `internal/search` `Provider` interface (self-hosted
  **SearXNG** + **Brave** JSON adapters, no HTML scraping), injected via
  `browser.WithSearchProvider` and configured with `--search-provider`/
  `--search-endpoint` + `UNBLINK_SEARCH_API_KEY` (key env-only, header-only, never
  logged). The tool is always registered and errors cleanly until a provider is
  set (the `interact`/`--js` precedent). The search client never fetches result
  URLs — the agent `read`s them through the SSRF-guarded page path. Discovery
  fetches ride the default client, inheriting the SSRF dial guard + per-host rate
  limiter for free. Fourteen tools total; version 0.14.0. *(completes the
  four-gap traversal roadmap.)*

- **Phase 12 — Dynamic-content reliability.** ✅ Makes JS renders deterministic and
  complete so the existing tools stop returning half-hydrated pages. (a) The one-shot
  `Engine.Render` was converged onto the persistent live `Context`'s model — a
  `Start()`ed loop driven by the shared `settlePoll` quiet-period heuristic (network
  idle + DOM-quiet) rather than blocking on a natural drain, so a page with a
  persistent `setInterval` early-exits instead of burning the full budget, and the
  loop is always `Terminate()`d before the caller reads `doc` (no background timer
  races extraction). (b) A new agent-facing **`wait_for`** — `read`/`interact` take
  `wait_for` (CSS selector) / `wait_text` (visible-text) + `wait_timeout`; the settle
  holds open until the condition appears in the live DOM or the (hard-capped, ~30s)
  budget elapses, and `wait_met` on the result tells the agent whether the content it
  asked for actually arrived vs. is missing. A waited render bypasses the stateless
  cache and raises the per-fetch timeout so a slow in-page fetch isn't cut short.
  (c) **JS-driven navigation** is no longer a silent no-op: `location.href`/`assign`/
  `replace` to a *different* document record a `pendingNav`, surfaced as
  `pending_navigation` on the read (`RenderDiag`) and `interact` results so the agent
  can follow it with `read`/`click` (SSRF guard + origin-scoped creds apply on the
  follow). SPA client-side routing (`history.pushState` + in-place re-render) was
  already captured by the live snapshot and is *not* reported as a navigation. All
  new inputs are optional; default behavior only gains correctness. Still fourteen
  tools (the new inputs ride `read`/`interact`). (d) **Saturation diagnostics** —
  a settle can also end because the budget ran out mid-hydration, which previously
  looked identical to a finished page. `settlePoll` now reports *how* it closed
  (deadline vs. quiescence, in-flight request count, DOM-still-mutating) and a
  `countingTransport` in the bridge tallies every subrequest (fetch/XHR, scripts,
  modules, dynamic import); the browser adds the download-budget guard's denial
  count. `read` surfaces these as `net_requests`/`net_failed`/`net_bytes`/
  `net_pending`/`net_denied`/`render_budget_hit`/`dom_busy` plus a footer when the
  snapshot was taken while the page was still working ("budget elapsed with N
  request(s) in flight / the DOM still mutating") or when the per-render download
  budget blocked page requests — so an agent can tell a complete snapshot from a
  starved one and knows which knob (wait_timeout vs. --js-max-bytes) would capture
  more.

- **Phase 13 — Agent-browsing security hardening.** ✅ Closes the exploit classes an
  "agent's browser" inherits (indirect prompt injection, data exfiltration, untrusted
  page JS). Two tiers. **Concrete exploit chains:** (a) page JS could read host files —
  goja_nodejs's default `require` loader is `os.Open`, so `require('/x.json')` returned
  parsed file contents; loops are now built with a rejecting `require.Registry` and the
  built-in console (whose printer writes to stdout, reserved for MCP) is replaced by a
  no-op console in the prelude (`internal/js/engine.go`, `globals.go`). (b) HttpOnly
  session cookies were readable via `document.cookie` because the stdlib jar drops the
  flag on read — a tracking jar records HttpOnly names at set-time and the bridge hides
  them (`internal/fetch/jar.go`, `internal/browser/transport.go`). (c) SSRF now also
  blocks CGNAT `100.64.0.0/10` (Alibaba metadata `100.100.100.200`) and broadcast; (d)
  `Retry-After` is clamped so a hostile 429 can't hang the client; (e) JS gets a
  call-stack cap, a bounded custom-element-upgrade count, and a rolling-window
  subrequest cap for live sessions. **Content trust boundary (on by default,
  `--no-safe-output` opts out):** returned web content is (a) wrapped in a provenance
  banner + per-call randomized fence so the host model can treat it as data, not
  instructions (spotlighting/datamarking; `internal/mcpserver` `frame`); (b) stripped
  of human-hidden text and comments before reduction (`dom.StripHidden`, `dom.Text`
  skip-hidden); and (c) image beacons `![](url)` are defanged to inert text so an
  auto-rendering host can't be turned into a zero-click exfiltration channel
  (`emit.DefangImages`). `llms.txt` is reframed as untrusted host-authored content, not
  a "curated AI guide." Framing is **defense-in-depth**: it makes the trust boundary
  explicit but cannot force a host model to honor it, and unblink is not the
  decision-maker for `click`/`submit_form`/`interact` — human-in-the-loop on sensitive
  actions is the host's responsibility. Color-based hiding (white-on-white) needs the
  full CSS cascade and is deliberately not detected. Still fourteen tools.
- **Phase 14 — Gesture-accurate interaction.** ✅ Makes `interact` activate the
  press/pointer-based widgets that dominate modern component libraries (react-aria,
  React-Spectrum, Radix, most design systems), which ignore a bare synthetic click.
  A default `click` now replays the full primary-button gesture a real mouse fires —
  `pointerdown`→`mousedown`→focus→`pointerup`→`mouseup`→`click` — with realistic,
  non-"virtual" event fields (`button`/`buttons`/`detail`/`pointerType:'mouse'`/
  `pointerId`/`isPrimary` and non-zero `width`/`height`/`pressure`, plus
  `isTrusted:true` on the engine-synthesized events) so `usePress` treats it as a
  genuine press (`internal/js/events.go` `newUIEvent`, `actions.go` `dispatchPress`).
  `element.focus()`/`blur()` and `document.activeElement` became real (mutable
  `bridge.activeEl` + `focus`/`focusin`/`blur`/`focusout` events; `proto.go`/
  `bridge.go`), and press moves focus to the nearest focusable ancestor on mousedown,
  so focus-triggered reveals work. No-op `setPointerCapture`/`releasePointerCapture`/
  `hasPointerCapture` stubs keep `usePress`'s pointerdown handler from throwing. Two
  new `interact` events — `hover` (`pointerover`→`mouseover`→`mouseenter`→`mousemove`)
  and `focus` — drive hover-reveal menus/tooltips and focus-triggered dropdowns.
  `element.click()` (the JS method) stays a spec-correct single bare click; only the
  agent-driven `interact` path emulates a gesture.
- **Phase 15 — `crypto` global.** ✅ Adds `crypto.getRandomValues` + `crypto.randomUUID`
  to `preludeJS` (`internal/js/globals.go`), which `uuid`/`nanoid`/react-aria's `useId`
  need at import time — their absence threw and blocked hydration. Backed by
  `Math.random` (collision-resistant ids are all these consumers need; unblink is a
  read-only extractor, not a security context); `crypto.subtle` is left undefined for
  feature-detection. A companion investigation (see
  `docs/specs/js-classic-script-downleveling-and-crypto.md`) planned to also downlevel
  classic `<script>` bundles through esbuild, but **dropped it**: the pinned goja now
  parses modern syntax (private fields, `??=`, `?.`, static blocks) natively, and
  esbuild's ES2017 downleveling routes private fields through goja's **buggy `WeakMap`**
  (a repeated `set` on the same key does not overwrite), silently corrupting code goja
  runs correctly. `TestGojaParsesModernSyntaxNatively` is the canary guarding that.
- **Phase 16 — Dynamic `import()` + lazy chunk loading.** ✅ Makes modern (Vite/Rollup/
  ESM) and classic-webpack SPAs that code-split actually run under `--js`.
  - **Parse fix (`internal/js/scripts.go`, `modules.go`).** goja has no `ImportCall` node,
    so a literal `import()` in a **classic** `<script>` is a hard parse error that
    silently skipped the whole bundle (React never mounted). On a compile failure,
    `compileAndRun` now falls back to `lowerDynamicImport` — an `esbuild.Transform` at
    Target ESNext with `Supported{dynamic-import:false}` that lowers **only** dynamic
    import (private fields et al. stay native, so goja's buggy `WeakMap` is never
    involved — the Phase 15 constraint holds). Taken only on failure, so the native
    fast path is unchanged.
  - **Runtime loader (`internal/js/dynimport.go`).** The lowering's stable
    `__toESM(require(` output is redirected to a `__unblinkImportSync` global that
    fetches+bundles+runs the chunk (reusing `runModules`' esbuild+`modulePlugin`+guarded
    transport) and returns its namespace, so `(await import(spec)).default` resolves to
    the real export. Runs synchronously on the loop (like `runModules`); the disabled
    global `require` is left untouched (the require-off security control holds). If the
    loader is absent/errors (no transport, blocked fetch), it throws and the import
    rejects gracefully — the shell still mounts.
  - **Inserted-`<script src>` chunks (webpack "JSONP").** A dynamically inserted
    `<script src>` (`createElement`+`appendChild`) is now fetched, run (via the shared
    `compileAndRun`), and fires `load`/`error` — including the `.onload`/`.onerror`
    property handlers webpack sets directly — so `__webpack_require__.e` resolves.
    Inline inserted scripts and innerHTML-nested scripts still never execute.

- **Phase 17 — Agent trust: visible failure modes.** ✅ An agent-facing browser must
  never return plausible-but-wrong results silently; this phase makes every known
  silent failure speak.
  - **Classified errors (`internal/browser/errors.go`).** Every tool error now renders
    as `error [code]: message` with a stable code (`bad_input`, `unknown_session`,
    `session_expired`, `no_current_page`, `js_required`, `not_configured`, `blocked`,
    `cursor_expired`, `timeout`, `fetch_failed`, `internal`) plus a transient-retry
    hint. `browser.Classify` maps session/cursor/SSRF/net causes; messages tell the
    agent what to do next.
  - **Session lifecycle (`internal/session`).** Evicted ids (idle TTL / capacity) are
    tombstoned (bounded: 24h/4096) so *every* tool reports `session_expired` — a
    credentialed session is never silently re-created anonymous (the old
    `GetOrCreate(empty Config)` credential-drop). Explicit `close` frees the id;
    `action=new` on a live id with credentials errors (`ExistsError`) instead of
    silently keeping the old config; `session action=list` lists live sessions; TTL
    and cap are tunable (`--session-ttl`, `--session-cap`).
  - **Render diagnostics surfaced.** `read` results carry `framework` and capped
    `js_errors` (one-shot render); `interact` returns per-dispatch uncaught errors
    (`js.DispatchResult.Errors`, captured on-loop around the settle window). The
    article→full readability fallback is reported (`article_fallback:true`, mode says
    `full`) via `page.Article.Source` instead of mislabeling the mode.
  - **Bounded outputs everywhere.** `read` `max_tokens` is capped (`MaxReadTokens`
    24k); cursors fingerprint the *whole* content (fnv64a) and a stale/malformed
    cursor errors (`cursor_expired`/`bad_input`) instead of silently restarting at
    page 1; `links` takes `limit` (default 200, cap 1000) and reports
    `total`/`truncated`; `forms`/`controls` cap at 100/300 with `truncated`; `find`
    `max_hits` caps at 100; browse outlines truncate at 8 KiB with a marker; table
    extraction sets `Truncated` when the row cap drops data. No silent caps.
  - **Tool annotations (`internal/mcpserver/server.go`).** All 14 tools carry MCP
    `ToolAnnotations` — read-only+open-world for the ten read tools, non-destructive
    nav for `click`, destructive+open-world for `submit_form`/`interact`, local for
    `session` — so hosts can gate state-changing actions (the Phase 13
    human-in-the-loop contract now has a machine-readable signal).

- **Phase 18 — Resource hygiene + engine robustness.** ✅ Bounds every remaining
  unbounded growth path and closes the hydration blockers real bundles hit.
  - **Live-runtime cap (`browser.WithJSMaxLive`, `--js-max-live`, default 16).**
    Live contexts bypass the render semaphore, so the only prior bound was the
    256-session cap — up to 256 goja heaps. Opening a new live context now tears
    down the most-idle runtimes over the cap (session + page survive; the next
    interact reopens; `session action=list` shows `live_js`).
  - **Bounded state**: per-session history capped at 50 entries (each held a full
    DOM + raw body); JS history stack capped at 100; per-context diag errors at
    64; rate-limiter host map idle-pruned past 1024 entries; session localStorage
    capped (1024 keys / 64KiB values).
  - **Settle/timeout retuned**: default JS budget 2s → **5s** (code-split SPAs
    need it; settled pages return early regardless) and the settle quiet window
    ~30ms → **~60ms** (2→4 ticks) — 30ms could declare a framework idling between
    microtask batches "settled" and return a half-hydrated page.
  - **Rowspan-aware tables (`dom.expandGrid`)**: cells now lay out on the real
    table grid — rowspan carries down (colspan interplay + trailing-gap padding),
    ending the silent column misalignment on merged-cell data tables.
  - **`click`/`submit_form` accept `render:true`** (parity with read): the
    destination/result page's JS runs before summarizing, and BrowseResult now
    carries `framework`/`js_errors`. Submit shares fetchPage's post-fetch stages
    (`processFetched`).
  - **Benign globals** (prelude): real recursive `structuredClone` (cycles,
    Date/RegExp/Map/Set/typed arrays), connection-less `WebSocket` stub (async
    error→close(1006) so reconnect logic degrades instead of crashing), inert
    `Worker`/`SharedWorker`, append-mode `document.write`/`writeln`, and
    `hashchange` on fragment navigation. `href`/`src` are now real reflected
    properties (getter returns the absolute URL) instead of wrapper-only.
  - **Session-persistent `localStorage` + `sessionStorage`** (`js.Storage`/
    `MemStorage`, wired via `Env.Storage`/`Env.SessionStorage`): a session is a
    tab, so both areas survive navigations within it (separate keyspaces) and die
    with it — a session's renders, live runtime, and click/submit renders share
    the same mutex-guarded stores, so SPA auth/state flows survive across calls.
    One-shot stateless renders keep the fresh per-render maps.
  - *Deliberately deferred*: pruning the live-context DOM wrapper maps on node
    removal — JS legitimately holds detached nodes (React vnodes), so pruning
    risks correctness; the live-runtime cap + session TTL now bound that memory.

- **Phase 19 — Capability roadmap: fetch efficiency + upload + progress.** ✅
  - **HTTP conditional revalidation** (`fetch.Client.GetConditional`,
    `browser.revalidate`): the stateless page cache retains expired entries
    (up to `ttl×10`) and revalidates them with `If-None-Match`/
    `If-Modified-Since` — a 304 reuses the already-parsed page (and re-dates the
    entry) instead of refetching and re-parsing; a changed body replaces it. The
    previously dead 304 path in `fetch.decodeBody` is now live.
  - **HTTP/2 connection pooling under `--tls-mimic`**: the utls RoundTripper
    pools one h2 `ClientConn` per host:port and reuses it while the server keeps
    it open (dead conns are detected via `CanTakeNewRequest`/round-trip failure
    and redialed, with a single safe retry) — repeat fetches no longer pay a
    TCP+TLS handshake each. HTTP/1.1 stays one-conn-per-request (rare on this
    path).
  - **multipart/form-data submission** (`fetch.Client.SubmitMultipart`,
    `submit_form` `files`): forms declaring `enctype=multipart/form-data`
    switch encoding automatically; file uploads take inline `content`/
    `content_base64` (capped: 8 files / 4 MiB total) — file bytes are supplied
    by the agent, **never read from local disk**, so a hostile page cannot turn
    submit into local-file exfiltration. Multipart requires a POST form.
    `page.Form` gains `Enctype`.
  - **MCP progress notifications for `map`** (`browser.MapProgress`): a client
    that sends `_meta.progressToken` receives throttled (500ms) progress
    notifications (`done/total` + phase message) while the up-to-60s walk runs,
    ending with a completion update. The bridge lives in `mcpserver.handleMap`;
    the browser layer stays MCP-free (a plain callback type).
  - **Eval hardening**: the JS-render suite (plain + Preact/React/Vue bundles),
    `read-wait-for`, `interact-navigation`, and `read-pdf` are promoted to
    must-pass; a `Safe` case variant runs the production safe-output pipeline
    end-to-end (untrusted framing + hidden-strip + image defang, scored by
    `FramedUntrusted`), and a multipart-upload case gates `submit_form`'s new
    path.
  - *Deferred*: a streamable-HTTP MCP transport (would force multi-tenant
    session namespacing; stdio remains the deployment model for now).

- **Phase 20 — Quality infrastructure.** ✅ The final evaluation-roadmap tier:
  test the security-sensitive glue, fuzz every untrusted-input parser, and
  make the repo's promises match reality.
  - **Fuzz targets + `make fuzz`** (seed corpora run in `make test`): dom
    parse/extract/tables/microdata/find, reduce+emit (both modes, hidden-strip
    on), PDF convert, robots.txt parse+match, pagination cursor round-trip.
  - **Two real bugs on the first fuzz runs**: (1) `dom.Find` panicked when
    lowercasing shifted byte offsets (invalid UTF-8 folds to 3-byte U+FFFD) —
    match offsets are now mapped back through a fold-offset table
    (`findLower`), with an ASCII fast path; (2) a malformed-xref PDF sent
    `dslipak/pdf` into an **infinite in-memory loop** that recover+context
    could not stop (a per-request CPU-burn DoS). Per ADR 0002's
    vendor-on-failure rule the library is now vendored at `third_party/pdf`
    (`replace` directive) with three loop bounds (page-tree walk, synthetic
    newlines, Extends chain), and `pdf.Convert` enforces its own hard
    wall-clock (`maxExtractTime`) as defense in depth. Both crashers live on
    as committed fuzz-corpus regressions. See ADR 0001.
  - **Credential-plumbing unit tests** (`internal/mcpserver/auth_test.go`,
    `tools_test.go`): env-var secret indirection, credentials-require-origin,
    one-shot auth, upload caps/decoding, untrusted-content framing
    (unpredictable per-call fence).
  - **ADRs in `docs/decisions/`**: 0001 (PDF library: contained + vendored),
    0002 (dependency pinning policy — goja pseudo-versions are deliberate).
  - **Scaffolding removed**: empty `internal/config/` and the aspirational
    `reference/` library; CLAUDE.md updated to match (CI + eval + fuzz are
    documented commands now). reduce/emit test tables broadened (hidden-strip
    variant matrix, article-vs-fallback source reporting, outline/markdown).

- **Phase 21 — Publication readiness.** ✅ The pre-first-public-release pass:
  close the highest-frequency browser-API gaps, bound the last unbounded
  resource, gate the failure modes, and back the footprint pitch with numbers.
  - **Browser API tier-1** (`internal/js/prelude_api.go`, a second compiled
    prelude run after `preludeJS` in both the one-shot and live paths):
    `TextEncoder`/`TextDecoder` (pure-JS UTF-8), `atob`/`btoa`, `performance`;
    a truthful **constant viewport** (1280×720@1x — `innerWidth`/`screen`/
    `devicePixelRatio`/`visualViewport`) and a real `matchMedia` evaluator;
    the fetch-ecosystem classes (`Headers`/`Request`/`Response`/`Blob`/`File`/
    `FormData`) with a real `Response` and FormData→multipart bodies (serialized
    in JS, ArrayBuffer transport — `__unblinkFetch`'s signature is untouched);
    `postMessage` + functional `MessageChannel`; `DOMParser` (Go-side, sharing
    the refactored `documentFacade` with `createHTMLDocument`)/`XMLSerializer`/
    `Range`; constructable `CSSStyleSheet` + `Document.prototype.adoptedStyleSheets`
    (Lit's feature-detect); `WeakRef`/`FinalizationRegistry` strong-ref shims and
    an `Intl` crash-avoidance shim; `document.title` setter + `URL`/`referrer`/
    `currentScript`/`getElementsByName`; a fuller `navigator`.
  - **`navigator.webdriver` is `true` by default** — unblink *is* automation and
    the UA string already carries "unblink", so honesty is the default; the
    operator's `--tls-mimic` opt-in (fingerprint parity) flips it to `false` via
    `js.WithWebdriver`. Recording the decision here so it isn't relitigated: the
    one dishonest field belongs behind the same opt-in as the rest of the
    anti-bot persona, not in the default posture.
  - **`interact` keyboard support**: `keydown`/`keyup`/`keypress` dispatch a real
    `KeyboardEvent` (`internal/js/keys.go` + `events.go newKeyEvent`) via a new
    optional `key` param; `keydown` fires the full keydown→input→keypress→keyup
    sequence and submits an enclosing form on `Enter` (unless canceled).
  - **JS memory guard** (`internal/js/memguard.go`, `--js-memory-limit`): a
    process-heap watchdog interrupts every live runtime over the limit — the last
    unbounded resource under the untrusted-JS threat model. Process-level, not
    per-runtime, because goja has no heap accounting; see ADR 0003.
  - **Timer clamp + `timers_pending`**: a one-shot `setTimeout` past the render
    budget is clamped to fire in-budget (intervals never clamped) and the settle
    poll holds open until it does, so deferred content lands instead of silently
    vanishing; timers still pending at snapshot surface as `timers_pending`.
  - **Eval grew 40 → 47 must-pass cases**: starved-render diagnostics,
    `net_denied`, keydown search, Svelte + Lit render, a tier-1 API smoke page.
  - **Measured footprint**: `scripts/membench` (a separate module with chromedp,
    invisible to the root `./...`) benchmarks unblink vs. headless Chromium on
    identical fixtures; `docs/comparison.md` carries the numbers (≈15× lighter and
    faster to start). ADR 0003 added; README gains an MCP-client-config section, a
    flags table, `SECURITY.md`, `CONTRIBUTING.md`, and `CHANGELOG.md`.

- **Phase 22 — Throughput.** ✅ Closes the render-latency and aggregate-throughput
  gap against warm-browser MCP tools without touching the politeness defaults or
  resource bounds.
  - **Provable-idle settle (ADR 0004)**: the quiet-window heuristic gains a fast
    tier above it. Every JS work source was already instrumented (network via
    the `pending` bracket, macrotasks via the prelude's wrapped-timer table) —
    the one leak, goja's native `setImmediate`, is now routed through the
    wrapped `setTimeout`. When nothing is in flight and no live timers remain,
    no mechanism exists to run more JS, so `settlePoll` closes after one ~1ms
    confirmation tick instead of waiting out the 60ms window; anything armed
    falls back to the unchanged 60ms-quiet-after-last-activity heuristic (now
    polled at 5ms granularity, first check synchronous at poll entry). Idle
    renders drop ~63ms → ~3ms; synchronous-only `interact` dispatches stop
    paying the window per click. `SettledIdle` in the render diagnostics
    records which tier closed the settle. The invariant this rests on — every
    new async primitive must route through the audit — is recorded in ADR 0004.
  - **Crossbench measured the limiter, not the engine — and the limiter is
    now opt-in**: the single loopback fixture host + cache-busted URLs meant
    the then-default 5 req/s politeness limiter paced every published render
    at ~200ms (no other benchmarked tool ships one). `--rate-limit` now
    defaults to **off** so unblink runs like-for-like out of the box; set it
    (e.g. `--rate-limit 5`) to crawl politely. Live-session JS subrequests
    stay bounded regardless: the per-dispatch **byte** budget (`--js-max-bytes`,
    ADR 0009) caps each agent action, on top of the SSRF guard, session TTL/cap,
    and the memory guard. (The old 300/min rolling window was removed in ADR 0009.)
  - **`--js-concurrency`**: the one-shot render semaphore (previously pinned at
    4 with no knob) now defaults to GOMAXPROCS clamped to [4, 16] — renders are
    CPU-bound goja interpretation, so it scales with cores while the ceiling
    bounds worst-case transient heap (the ADR-0003 guard bounds the total
    regardless). `--js-prewarm` deliberately stays at 4: an idle prewarmed loop
    costs a runtime's worth of heap, and a burst past the pool only pays ~1.5ms
    inline creation. `MaxIdleConnsPerHost` rises 8 → 16 to match, so a full
    concurrency burst's connections stay reusable. Same-host fetch pacing
    remains `--rate-limit`'s job (opt-in, see below).
  - **Concurrent asset warming** (`internal/js/prefetch.go`): the initial
    external `<script src>` bodies previously fetched synchronously on the loop
    goroutine, one round trip after another. They now warm through the same
    counting/budgeted/SSRF-guarded transport with 16 bounded workers
    (`warmConcurrent`) while `runScripts` consumes them in strict document order —
    sum(RTT) collapses to max(RTT), and since runScripts executes exactly the
    collected snapshot the prefetch is not speculative. The same worker pool also
    warms the page's `<link rel=modulepreload>` / script-`rel=preload` chunk graph
    (`startModulePreload`), so a code-split SPA's otherwise-serial runtime
    `import()` path (dynimport) hits the asset cache — or joins an in-flight warm
    via `prefetched()` in the module `OnLoad` — instead of fetching each chunk one
    at a time. This adds no async primitive, so ADR 0004's settle proof is
    untouched (ADR 0009). Applies to one-shot renders and live session opens.
  - **Content-only compile caches**: `progKey` dropped the script name (page
    position / chunk URL) — identical bytes now share one `goja.Program`
    regardless of which URL or position delivered them, with the first-seen
    name embedded (diagnostic-only; external scripts now compile under their
    absolute URL, better than the old positional `script-N.js`). `bundleKey`
    clears the base URL's query/fragment before hashing — relative specifiers
    resolve against scheme/host/path only — so `?utm=`/cache-busted variants
    of a module page stop re-running the whole esbuild build.
  - **Markdown memo on the page cache** (`internal/browser/cache.go`): each
    stateless cache entry carries the reduced+emitted Markdown keyed
    {mode, safeOutput}; repeat reads and pagination cursor pages skip
    reduce+emit+defang (warm read 657µs → 11µs, allocs −99%). Invalidation is
    structural — the memo dies with its entry, survives a 304 touch, and
    lookups require pointer identity with the resolved page. Sessions, waited
    renders, and credentialed one-shots bypass it entirely.

- **Phase 23 — Shadow DOM composition (ADR 0005).** ✅ Replaces the flat,
  non-encapsulating Shadow DOM with a **composed, encapsulating** one, fixing real
  content loss (a component setting `shadowRoot.innerHTML` after light children
  existed used to destroy them) and spec-incorrect event flow.
  - **Detached shadow subtree**: `attachShadow` now backs the root with its own
    detached `DocumentNode` (`shadowRoots[host]` + reverse `shadowHostOf`), so page-JS
    `document.querySelector` provably stops at the boundary while a shadow-internal
    `querySelector` still works. `isConnected`/`getRootNode` bridge the boundary.
  - **Compose = the browser flattened tree** (`internal/js/slots.go`): one
    clone-producing walk replaces each host with its shadow subtree and resolves every
    `<slot>` to the host's assigned light children (by name, with fallback), recursing
    through nested hosts. Destructive into `doc` at the end of one-shot `Render`;
    clone-only at live `Snapshot` (the live session keeps its separate subtrees across
    `Dispatch`). `assignedNodes`/`assignedElements`/`slot` are exposed to component code.
  - **Cross-boundary events**: `elementTargetPath` builds the shadow-including composed
    path with per-hop `target` retargeting; `composedPath()` was added and the
    `composed` flag is plumbed/honored. `bubbles` and `composed` stay orthogonal —
    `composed` gates crossing the boundary, `bubbles` gates only the bubble phase, and
    anchors are retargeted in every phase.
  - **unblink pierces, page JS does not**: interact and `wait_for` resolve selectors
    through shadow subtrees (`queryPierce`), so an agent can target a shadow-rendered
    control; page code still sees a real boundary.
  - **Declarative Shadow DOM** (`internal/dom/shadow.go`): `<template shadowrootmode>`
    is flattened into composed light content on the static no-JS path (SSR web
    components render without `--js`); under `--js` the imperative path owns it.
  - **Closed mode**: `.shadowRoot` is `null` for a closed root, but its content is
    still composed into extraction output (mission: see everything).

- **Phase 24 — Web API tier-1 misses (see `docs/web-api-priorities.md`).** ✅ Closes the
  eight highest-frequency browser-API gaps that crash hydration on real/common sites, each
  by the cheapest treatment that lets content materialize (the FULL / STUB / leave-undefined
  rubric in the priorities doc). All in `internal/js/prelude_api.go` except the element gaps
  (`proto.go`/`domapi.go`): **FULL** — `CSS.escape`/`supports` (WHATWG identifier escape),
  `FileReader` and `ReadableStream`/`WritableStream`/`TransformStream` over the existing
  `Blob.__bytes` plumbing, and `Element` `lastElementChild`/`getAttributeNode`/`namespaceURI`.
  **STUB** (boot-survival, not the feature) — an inert `canvas.getContext('2d')` on the shared
  `HTMLElement.prototype` (instances don't carry `HTMLCanvasElement.prototype`), an in-memory
  non-persistent `indexedDB`, `EventSource` (error→closed, like `WebSocket`), and the
  `navigator` `serviceWorker`/`clipboard`/`permissions`/`geolocation`/`mediaDevices` surfaces
  (`serviceWorker.ready` resolves so `await` never hangs). The async ones honor the ADR 0004
  settle audit by routing completions through the **wrapped** `window.setTimeout` (indexedDB
  runs its data op synchronously and defers only the success event, so operation order is
  race-free) or a microtask (Streams). Regression nets: `internal/js/webapi_tier1_test.go`
  (each async API's content is written only from its completion, so a broken settle route
  fails the test) and the extended `js-api-smoke` eval case (markers `api-11`…`api-17`).

- **Phase 25 — Web API tier-2 (see `docs/web-api-priorities.md`).** ✅ The situational
  cluster, once Tier 1 shipped. All in `internal/js/prelude_api.go` except the custom-element
  lifecycle hook (`mutationobserver.go`/`customelements.go`): **FULL** — `DOMMatrix`/`DOMPoint`/
  `DOMRect`/`DOMQuad`/`Path2D` (real pure-JS matrix math), `TextEncoderStream`/`TextDecoderStream`
  (over the Phase-24 Streams), and `disconnectedCallback` (fired from a new `onMutate`→
  `disconnectTree` removal hook). **STUB** — `document.fonts`/`FontFace` (`ready` resolves),
  `Element.animate`/`Animation`/`KeyframeEffect` (finished animation, `onfinish` via the wrapped
  timer), `attachInternals`/`ElementInternals`, the Navigation API (`navigate` fires a `navigate`
  event whose `intercept({handler})` runs the router's view update; same-document only),
  `cookieStore` (over `document.cookie`), and `reportError`. Regression nets:
  `internal/js/webapi_tier2_test.go` + `js-api-smoke` markers `api-18`…`api-25`. Also **broad
  `crypto.subtle`** (ADR 0006, `internal/js/subtle.go`): Go byte-primitives (`crypto/*` +
  `crypto/rand`) under a JS WebCrypto model — digest, HMAC, AES-GCM/CBC/CTR, PBKDF2/HKDF, and
  symmetric key gen/import/export; RSA/ECDSA reject (never a sync throw), and `getRandomValues`
  is re-pointed at `crypto/rand`. **Held open**: the full-WHATWG `URL` upgrade (only on a
  demonstrated break). **Deferred**: `adoptedCallback` (cross-document adoption) and
  `CompressionStream` (needs a Go codec).

- **Phase 26 — Tier 3 crash-avoidance stubs (see `docs/web-api-priorities.md`).** ✅ Flips the
  niche surface from "leave undefined until a page crashes" to **proactive inert stubs**, all in
  `preludeAPIJS`. None of these gate textual content, but a page touching one unconditionally at
  boot (`video.play()`, `new AudioContext()`, `new Notification()`, a WebGPU/WebRTC probe) would
  abort hydration without them. Every stub is inert — construction/access never throws, promises
  resolve empty or reject *catchably*, no device/media work happens: media playback + the full
  Web Audio node graph + `MediaSource`; the `navigator` device family (`bluetooth`/`usb`/`serial`/
  `hid`/`xr`/`gpu`/`getGamepads`/`getBattery`/`requestMIDIAccess`/`wakeLock`/`locks`/`credentials`/
  `share`/…); niche constructors (`RTCPeerConnection`, `PaymentRequest`, Web Speech, the sensor
  family, `Notification` at `permission:'denied'`, `WebTransport`, `BarcodeDetector`, `EyeDropper`,
  `IdleDetector`, `CloseWatcher`, `ToggleEvent`); and element/document interaction (Popover,
  Fullscreen, Picture-in-Picture, Pointer Lock, background sync/push on the serviceWorker
  registration, and **View Transitions** — `document.startViewTransition` runs the update callback
  synchronously so a router's new view materializes). Regression nets:
  `internal/js/webapi_tier3_test.go` + `js-api-smoke` markers `api-26`/`api-27`. **Still not
  stubbed** (reactive): EME, Web NFC, File System Access, and the Privacy Sandbox proposals.

- **`extract` tool — caller-directed CSS-schema extraction.** ✅ Complements the auto-discovery
  `data` tool (JSON-LD/tables/microdata) with a schema the *agent* supplies: `fields` maps each
  output name to a CSS selector (a string takes the first match's collapsed text; `{selector, attr}`
  takes an attribute value instead), an optional `root` selector emits one record per matching
  container, and the result is an array of records (`limit` default 50, hard cap 200; `truncated`
  when more matched). The selection primitive is `dom.Records` (`internal/dom/schema.go`) — all
  cascadia/selector work stays inside `internal/dom`, the enforced DOM boundary; `Browser.Extract`
  orchestrates and `mcpserver` adapts (a `map[string]any` field arg lets the MCP schema accept both
  the string and object forms). Read-only over the parsed tree, no page-JS injection (unlike a browser
  `evaluate`), HTML only, reusing the existing `collapsedText`/`attr`/`truncate` helpers. Closes the
  one Obscura/Lightpanda differentiator ("CSS-schema extraction") that fit unblink's reduce-to-meaning
  identity rather than its automation turf — see `docs/comparison.md`.

- **`collections` — repeating-structure discovery in `browse`.** ✅ Closes the "how does the agent know
  the selectors" gap for `extract`: `browse` now auto-detects a page's dominant repeating record-sets
  and hands back a ready-to-use `{root, fields}` schema for each. Detection (`internal/dom/collections.go`,
  `detectCollections`) runs inside the always-on `dom.Extract` structural pass and is surfaced via
  `summarize`, terse and two-tier capped (`maxCollections`/`maxCollectionsOut`) like regions/headings.
  It groups a parent's direct children by a structural signature (tag + sorted classes — the same key as
  `selIndex.sigCount`, now built once in `Extract` and shared with `extractInteractive` — or a bounded
  shape hash for class-less repeats), gates out thin/pagination/nav-chrome lists, and **ranks by the
  enclosing landmark region** (content lists in `main`/`article` win; `nav`/`footer` are demoted or
  dropped) — the region synergy is why this lives alongside `browse` rather than in a standalone tool.
  Every synthesized `root` is re-resolved against the group and the whole schema is proven by one
  `dom.Records` round-trip, so a surfaced collection is always valid `extract` input. Schema-only (no
  sample values) to keep `browse` cheap; `extract` executes it.

- **WebExtensions runtime loading — network filtering (Phase 1, ADR 0010).** ✅ unblink can load
  user-supplied browser extensions at runtime (`--extension <dir|.xpi|.crx|.zip>`, `--extensions-dir`)
  so best-in-class GPL tooling (uBlock Origin Lite, AdGuard MV3) extends what unblink does without
  entering its MIT tree — the extension is a separate artifact, like a browser add-on. A new pure-Go
  `internal/webext` package (manifest MV2/MV3, match patterns, a tokenized Adblock-Plus `urlFilter`
  matcher — no regexp, no ReDoS — and dir/archive loaders with traversal + zip-bomb guards) owns the
  model; the dependency direction stays `browser → js → webext`. This phase honors an extension's
  static `declarativeNetRequest` rules: an engine-lifetime `ExtensionHost` (shared read-only across
  every render/session, like one browser process' extensions across tabs) is consulted by a
  `blockingTransport` decorator wrapped *inside* `countingTransport` at the one choke point every
  page-JS subrequest shares (`internal/js/bridge.go` `newBridge`), so a request matching a block rule
  is cancelled before the socket — faithfully, as a request failure (`net::ERR_BLOCKED_BY_CLIENT`) —
  and still shows in the `requests` tool. Resource type (`$script`/`$xmlhttprequest`) is threaded via
  a request-context value at each origin site. Because unblink never fetches passive subresources,
  this targets JS-initiated ad/tracker scripts + beacons; cosmetic DOM removal, the `chrome`/`browser`
  API, the background worker + messaging (respecting the ADR-0004 settle via the `pending` bracket),
  and full uBlock Origin are the phases that follow. Regression nets: `internal/webext/*_test.go` (unit
  + `FuzzParseManifest`/`FuzzParseRules`/`FuzzMatchPattern`), `internal/js/extension_test.go` (an
  end-to-end blocked fetch), `internal/browser/extensions_test.go` (wiring + the `--disable-js`
  rejection).

- **WebExtensions — content scripts, `chrome` API, cosmetic filtering (Phase 2, ADR 0010).** ✅
  Adds the surface that lets an extension *modify the page*. `content_scripts` JS/CSS matching the
  page are injected at their `run_at` (document_start/end/idle) around the page's own scripts, in
  one-shot renders and live sessions (`internal/js/contentscript.go`, wired in `engine.go`/`context.go`).
  The `chrome`/`browser` namespace (`internal/js/extapi.go`) is installed as Go closures — `runtime`
  (getURL/id/getManifest + message/port stubs), `i18n.getMessage` from `_locales`, in-memory
  `storage`, `scripting.insertCSS`, a synthetic `tabs`, and accept-and-ignore UI/eventing stubs —
  supporting both the MV2-callback and MV3-promise forms (synchronous resolve keeps the ADR-0004
  settle audit intact). **Cosmetic filtering** (`internal/js/cosmetic.go`) is the key lever: with no
  CSSOM, element-hiding CSS is translated into *physical node removal* — matched ad markup is
  detached from the frozen tree post-Terminate (one-shot) / on the snapshot clone (live), invisible
  to page JS. Content scripts run **same-world** (one JS global + `chrome`; true isolated worlds are
  Phase 5), and a script-less page now still renders when an extension is loaded. Nets:
  `internal/webext/i18n_test.go`, `internal/js/cosmetic_internal_test.go` (+ `FuzzHidingSelectors`),
  `internal/js/contentscript_test.go` (end-to-end cosmetic strip + content-script i18n/DOM edit).
  Deferred to Phase 3+ (what uBlock Origin's *dynamic* cosmetics need): the background service worker
  + messaging, storage persistence + onChanged, and MV2 webRequest.

- **WebExtensions — background worker + runtime messaging (Phase 3, ADR 0010).** ✅ Adds the
  persistent background context and the message path uBlock's *dynamic* cosmetic filtering uses.
  The extension's background scripts run on a dedicated engine-lifetime eventloop — a separate goja
  runtime from every page render, started once and kept warm (`internal/js/bgworker.go`), reusing the
  page bridge with a minimal empty document (a SW has no DOM — pragmatic simplification) and a
  `bundleTransport` that serves the extension's own files. `chrome.runtime.sendMessage`
  (`internal/js/broker.go`) round-trips a content script ↔ background, carrying plain Go values across
  the two runtimes via each loop's RunOnLoop (sync + async `sendResponse`). **The ADR-0004 crux is
  resolved**: the background's own eventloop means its timers can't hold a page open, and each
  cross-runtime round-trip is bracketed on the page's `pending` counter (like a network request), so a
  content-script reply's DOM effect lands before settle — proven by `TestBackgroundMessaging`. Storage
  (`chrome.storage.*`) is a shared in-memory store across contexts. Deferred to Phase 4 (needs
  `chrome-extension://` serving): storage disk persistence + onChanged, background→page messaging, real
  Port, external background fetch, and MV2 webRequest.

- **WebExtensions — extension resources, storage persistence, dynamic DNR (Phase 4, ADR 0010).** ✅
  Fills in what a stock MV3 build (uBlock Origin Lite) leans on. `chrome-extension://` resource
  serving (`internal/js/extresource.go`): `fetch(chrome.runtime.getURL(...))` resolves to the packaged
  file via an `extResourceTransport` decorator (inside counting, outside blocking — logged but never
  DNR-blocked), gated by `Bundle.ResourceAccessible` (content-script self-access + web_accessible_resources).
  Storage (`internal/js/extstore.go`): `chrome.storage.local`/`sync` persist to
  `<UserCacheDir>/unblink/ext/<id>/<area>.json` (so uBlock's compiled lists survive restarts) and
  `onChanged` fans real change records to per-loop listeners (with dead-loop pruning). Dynamic/session
  `declarativeNetRequest` rules (`updateDynamicRules`/`updateSessionRules`) compile into the RWMutex-guarded
  `RuleMatcher` and take effect immediately. Nets: `internal/js/{extdnr,extresource,extstore}_test.go`.
  Deferred to Phase 5: MV2 webRequest, background→page messaging + real Port, external background fetch,
  a validated module service worker, isolated content-script worlds, scriptlets, IndexedDB/cacheStorage shim.

- **WebExtensions — MV2 webRequest blocking (Phase 5, ADR 0010).** ✅ A Manifest-V2 extension's
  background can cancel/redirect requests from a blocking `webRequest.onBeforeRequest` listener — full
  uBlock Origin's model (its own JS network engine returns `{cancel:true}`). unblink delivers each
  subrequest to the background's listeners and honors the verdict (`internal/js/extwebrequest.go`).
  Since a request is decided on the page's off-loop fetch goroutine but the listeners live in the
  background runtime, the verdict is a **timeout-guarded round-trip onto the background loop** (2s cap →
  degrades to allow, never hangs); `blockingTransport` checks DNR first, then webRequest only when the
  background has listeners (atomic count read off-loop). Background `start()` now blocks until the
  background's synchronous setup finishes, so the first request sees the listeners (fixed a startup
  race). Net: `internal/js/extwebrequest_test.go`. Remaining Phase-5 hardening (background→page
  messaging, real Port, external background fetch, validated module SW, isolated worlds, scriptlets,
  IndexedDB shim) is still open.

Permanent JS non-goals (still no layout engine): a real layout/geometry engine,
canvas/WebGL, Workers/WebSocket/IndexedDB. **Element** geometry and CSSOM are
**honest constant stubs** — `getBoundingClientRect`/`offset*`/`getComputedStyle`
return zeros/empty so framework probes don't crash; no pixels are ever computed.
The **viewport environment**, by contrast, is now a truthful constant (Phase 21):
`window.innerWidth`/`screen`/`devicePixelRatio` report a fixed 1280×720@1x desktop
and `matchMedia` genuinely evaluates queries against it, so responsive code takes
its real branch instead of the always-false fallback. Shadow DOM is **encapsulated
and composed** (Phase 23, ADR 0005): each shadow root is a detached subtree — page-JS
`document.querySelector` respects the boundary — and a compose pass flattens it,
resolving `<slot>` distribution, into the light tree so extraction still sees
everything (unblink's own interact/wait selectors pierce the boundary). Events cross
the boundary correctly (composed path + `target` retargeting + `composedPath()`, with
`bubbles`/`composed` kept orthogonal), and declarative Shadow DOM
(`<template shadowrootmode>`) is flattened on the static no-JS path. Only CSS-level
scoping (`:host`/`::slotted`/`::part`) and slot reprojection stay out of scope.
`IntersectionObserver`/`ResizeObserver` report one synthetic "visible / zero-size"
entry so lazy content renders.

**Programmatic form submission** (`internal/js/forms.go`): `document.forms`,
`form.elements`, `.namedItem`, and `form.submit()`/`requestSubmit()` are modelled.
A render never performs the cross-document fetch, so a submit is treated like a
`location` navigation — a cancelable `submit` event fires first (an `onsubmit`
returning false or `preventDefault()` aborts), then the target lands in `pendingNav`
(surfaced as `pending_navigation`) for the caller to follow in-session. GET forms
serialize their successful controls into the query string; POST records the action
target best-effort (the body isn't carried on the follow). This is what clears a
**self-submitting JS bot-check interstitial** (e.g. Reddit's: on `DOMContentLoaded`
it computes a `solution`, sets a hidden field via `elements.namedItem`, and calls
`requestSubmit()` on a GET form). The engine runs that unchanged; the agent follows
the resulting `pending_navigation` in a session and the clearance cookie unlocks the
rest of the host (feed, `.json` API, etc.). Note this only handles a *page-computed*
solution — a server-side proof-of-work or interactive challenge still won't pass.

Anti-bot fingerprint (under `--tls-mimic`): the utls ClientHello already gives a
genuine Chrome JA3/JA4, and the HTTP/2 SETTINGS + request-header persona are now
**best-effort tuned to Chrome** — the h2 layer advertises Chrome's
`HEADER_TABLE_SIZE`/`MAX_HEADER_LIST_SIZE`/`MAX_FRAME_SIZE`
(`internal/fetch/tls.go`) and the request set gains Chrome's `Accept`, `sec-ch-ua`
client hints, and `Sec-Fetch-*` metadata (`fetch.setChromeMimicHeaders`), so a
TLS-says-Chrome / h2-says-Go mismatch no longer stands out. **Still deferred**
(needs a forked http2, out of scope for the no-new-deps slice): the SETTINGS frame
*order*, `INITIAL_WINDOW_SIZE`, `ENABLE_PUSH`, the connection `WINDOW_UPDATE`, and
the `:method :authority :scheme :path` pseudo-header order — stock
`x/net/http2` can't control these, and over HTTP/2 Go still owns header ordering
(only header presence/values align, not order). Cloudflare-Turnstile / interactive
JS challenges remain a non-goal (would need a `chromedp` escape hatch, which breaks
the pure-Go / no-Chromium constraint).

## Risks

- **JS/DOM gap**: most modern SPA content is client-rendered and the static path
  won't see it. Mitigation: JS renders by default; a caller can drop to the static
  path per read (`render=false`) or per server (`--disable-js`), and the engine
  degrades gracefully to static when a render fails. With JS on, the flat-DOM model
  (Phase 8), plus composed Shadow DOM (Phase 23), now renders mainstream
  React/Vue/Preact/Svelte/Lit apps and web components, so the remaining gap is
  layout-dependent behaviour (geometry, canvas, CSS-level Shadow-DOM style scoping),
  which stays out of scope.
- **goja goroutine-safety**: a runtime is single-goroutine; concurrency is
  handled by a pool, capped separately from fetch concurrency.
- **Output token budget**: whole-page Markdown can be huge; `read` will paginate
  and default to article-mode extraction. Never return an unbounded blob.
- **Anti-bot / TLS fingerprint**: realistic UA/headers first; utls opt-in later.
