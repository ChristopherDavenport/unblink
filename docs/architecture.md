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
| `internal/reduce`    | Semantic reduction: readability extraction + bluemonday sanitize.     |
| `internal/emit`      | Serialize reduced content to Markdown + heading outline.              |
| `internal/session`   | Per-session cookie jar (own `fetch.Client`) + navigation history + per-page interaction replay log (imports `js` for `js.Action`). |
| `internal/robots`    | Exposure-grade robots.txt (REP) parser: groups, `*`, Allow/Disallow (`*`/`$`), Crawl-delay, Sitemaps. No external deps; **never enforces**. |
| `internal/sitemap`   | Pure-stdlib sitemaps.org XML decoder (`<urlset>`/`<sitemapindex>`, gzip-aware). No fetch, no `x/net/html`. |
| `internal/search`    | Optional web-search behind a `Provider` interface (SearXNG + Brave JSON adapters). Off by default; injected into `browser`. |
| `internal/browser`   | Orchestrator. Wires the pipeline + sessions; the only thing `mcpserver` calls. Holds a host-scoped robots.txt/llms.txt cache (`sitecache.go`) and the sitemap/crawl `map` + `search` surface (`discover.go`).|
| `internal/mcpserver` | Thin MCP adapter: tool registration + handlers + transport.           |
| `internal/js`        | goja + eventloop DOM bridge over the `*html.Node` tree: scripts/ESM, fetch/XHR, events, a real prototype chain + MutationObserver + custom elements / flat Shadow DOM so React/Vue/Lit render (Phase 8). Quarantined. |
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
  place); runs inline scripts, settles async, wall-clock interrupt guard. Opt-in
  per request (`render`) and per server (`--js`).
- **Phase 4b — Real JS networking & lifecycle.** ✅ External `<script src>`
  (document order); `window.fetch` + `XMLHttpRequest` routed through a guarded
  transport (one `__unblinkFetch` Go primitive with the race-free inline-keepalive
  pattern); `DOMContentLoaded`/`load` event dispatch. SSRF guard (blocks private/
  loopback/metadata IPs) + per-render request budget; `--js-no-network`,
  `--js-allow-private`, `--js-max-requests`.
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
  Lit / web components), always-on under `--js`. The goja↔DOM bridge gained a real
  `Node`/`Element`/`HTMLElement`/`Text`/`Comment`/`DocumentFragment`/`Document`
  prototype chain (so `instanceof` and prototype-patching work; `internal/js/proto.go`),
  the DOM-tree APIs frameworks call at mount (`createComment`/`createDocumentFragment`/
  `createElementNS`/`cloneNode`/`replaceChild`/`insertAdjacent*`/`dataset`/`importNode`/
  `createTreeWalker`/`getAttributeNames`, and the `HTML*Element` interface globals;
  `domapi.go`/`treewalker.go`), a real `MutationObserver` + `queueMicrotask`, real
  `history.pushState`/`popstate` for client-side routing, custom-element upgrade plus
  a **flat** (non-encapsulating) Shadow DOM that renders into the light tree
  (`customelements.go`), and render diagnostics — detected framework + uncaught/async
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
  tools (the new inputs ride `read`/`interact`).

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

Permanent JS non-goals (still no layout engine): a real layout/geometry engine,
canvas/WebGL, Workers/WebSocket/IndexedDB. Geometry and CSSOM are **honest constant
stubs** — `getBoundingClientRect`/`offset*`/`getComputedStyle` return zeros/empty so
framework probes don't crash; no pixels are ever computed. Shadow DOM is **flattened,
not encapsulated**: shadow content renders into the light tree (visible to extraction)
and `:host`/`<slot>`/style scoping are ignored. `IntersectionObserver`/`ResizeObserver`
report one synthetic "visible / zero-size" entry so lazy content renders.

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
  won't see it. Mitigation: ship static value first; treat JS as opt-in (`--js`)
  and degrade gracefully to the static path. With `--js` the flat-DOM model
  (Phase 8) now renders mainstream React/Vue/Preact/Svelte/Lit apps, so the
  remaining gap is layout-dependent behaviour (geometry, canvas, true Shadow-DOM
  encapsulation), which stays out of scope.
- **goja goroutine-safety**: a runtime is single-goroutine; concurrency is
  handled by a pool, capped separately from fetch concurrency.
- **Output token budget**: whole-page Markdown can be huge; `read` will paginate
  and default to article-mode extraction. Never return an unbounded blob.
- **Anti-bot / TLS fingerprint**: realistic UA/headers first; utls opt-in later.
