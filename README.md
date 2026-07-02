# unblink

A pure-Go (no cgo, no Chromium) "web browser" whose purpose is **not** visual
rendering but **exposing web information to an AI model**. unblink fetches a
page, parses it, strips visual-only junk (scripts, styles, navigation, ads),
and hands the model a clean, token-budgeted Markdown representation — over the
[Model Context Protocol (MCP)](https://modelcontextprotocol.io).

An AI doesn't need pixels. It needs structured meaning.

```
fetch(url) → parse HTML5 → [optionally execute JS] → semantic reduction → emit Markdown
```

## Status

**v0.16.0.** The full pipeline works end to end: 14 MCP tools covering reading,
navigation, sessions, forms, structured data, site discovery, and search. The
static read path turns most server-rendered pages into clean Markdown with zero
JavaScript; the opt-in JavaScript engine (`--js`) renders mainstream SPA
frameworks and powers live interactive sessions (`interact`). See
[docs/architecture.md](docs/architecture.md) for the full design (phases 0–16)
and its non-goals.

## Requirements

The official MCP Go SDK requires **Go ≥ 1.25**. The `Makefile` sets
`GOTOOLCHAIN=auto`, so the `go` command downloads the toolchain pinned in
`go.mod` automatically — you do not need to install Go 1.25 yourself, and your
global `go env` is left untouched. (If you run `go` directly rather than via
`make`, prefix commands with `GOTOOLCHAIN=auto`.)

## Install

```sh
GOTOOLCHAIN=auto go install github.com/christopherdavenport/unblink/cmd/unblink@latest
```

Or download a prebuilt binary from the GitHub releases page, or build from
source:

## Build & run

```sh
make build           # -> bin/unblink
make test            # run the test suite
./bin/unblink        # serve MCP over stdio (default transport)
./bin/unblink --js   # also enable opt-in JavaScript rendering (the `render` tool arg)
./bin/unblink --version
```

JavaScript rendering (pure Go, no cgo, no Chromium) runs a page's scripts against
a hand-rolled DOM over the parsed tree: inline **and external** scripts,
**ES modules** (`<script type=module>`, `import`/`export`, dynamic `import()`,
import maps — bundled with esbuild), `window.fetch` + `XMLHttpRequest`, DOM
**events with full capture/bubble propagation** (delegated listeners,
`once`/`passive`/`{signal}` options, `AbortController`, typed Event subclasses),
and `document.cookie` (backed by the session jar). Page-JS network requests are
guarded — requests to private/loopback/metadata IPs are blocked and a per-render
request budget applies (`--js-no-network`, `--js-allow-private`,
`--js-max-requests`). A background pool of fresh runtimes keeps render latency low
(`--js-prewarm`, `0` disables); the per-render budget defaults to 5s (`--js-timeout`).
With a session, `interact` keeps a **live runtime**
alive for the page so JS state persists across calls (a true browser-tab session);
live runtimes are capped (`--js-max-live`, LRU torn down) and both
`window.localStorage` and `window.sessionStorage` persist per session (a session
is a tab), so SPA auth/state flows survive across calls. Common globals
that bundles use without feature-detection are covered: `structuredClone`, a
connection-less `WebSocket` stub (error→close), inert `Worker`, append-mode
`document.write`, and `hashchange`.
Under `--js` the engine renders the **mainstream SPA frameworks** (React, Vue,
Preact, Svelte, Lit / web components) via a flat-DOM model — a real
Node/Element/HTMLElement prototype chain, MutationObserver, custom-element upgrade,
and a flattened (non-encapsulating) Shadow DOM whose content is visible to
extraction. Layout/geometry is constant-stubbed (no pixel layout engine), and
canvas/WebGL, Workers/WebSocket/IndexedDB, and true Shadow-DOM encapsulation remain
out of scope.

## Try it

unblink speaks MCP over stdio. Point any MCP-capable client at the `unblink`
binary, or drive it by hand:

```sh
{ printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}'; \
  sleep 0.3; \
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'; \
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read","arguments":{"url":"https://example.com"}}}'; \
  sleep 2; } | ./bin/unblink
```

### Tools

Every page tool accepts an optional `session` (any string — cookies and history
persist across calls; auto-created on first use), `use_current` (act on the
session's current page instead of fetching a URL), and `render` (run the page's
JavaScript first — requires starting the server with `--js`).

Idle sessions are evicted (default 30 minutes, tune with `--session-ttl` /
`--session-cap`); an evicted id then errors with `session_expired` and must be
re-created via `session(action=new)` — credentials are never carried over
silently. Errors follow a stable `error [code]: message` convention
(`bad_input`, `session_expired`, `no_current_page`, `js_required`,
`not_configured`, `blocked` (SSRF guard), `cursor_expired`, `timeout`,
`fetch_failed`…), and every tool carries MCP annotations (read-only vs
state-changing) so hosts can gate sensitive actions. JS render diagnostics
(`framework`, `js_errors`, `article_fallback`) are reported in `read`/`interact`
results rather than logged away.

| Tool          | Input                                              | Returns                                                                 |
| ------------- | -------------------------------------------------- | ----------------------------------------------------------------------- |
| `read`        | `{ url?, session?, use_current?, mode?, format?, selector?, max_tokens?, cursor?, wait_for?, wait_text?, wait_timeout?, headers?, auth? }` | Main content (`mode=article`, default) or whole page (`full`) as Markdown, paginated via cursor. `format=raw_html` returns the unreduced source (optionally scoped by a CSS `selector`) — the escape hatch for scripts/forms/SSR-embedded JSON that reduction strips; `format=text` returns visible plain text. `wait_for` (CSS selector) / `wait_text` hold a JS render open until that content hydrates (implies `render`, needs `--js`); `wait_timeout` (seconds, capped ~30s) extends the wait, and `wait_met` in the result reports whether it appeared. `headers`/`auth` attach one-shot credentials for a stateless gated GET (see [Authentication](#authentication)). |
| `browse`      | `{ url?, session?, use_current?, headers?, auth? }` | Cheap orientation: title, description, lang, heading outline, link/form/image counts, excerpt, plus `llms_txt`/`robots` presence hints. |
| `links`       | `{ url?, session?, use_current?, filter?, internal_only?, limit? }` | The page's links (text + absolute href), optionally filtered. `limit` defaults to 200 (cap 1000); `total`/`truncated` report the rest. |
| `forms`       | `{ url?, session?, use_current? }`                 | The page's forms and their fields (name, type, required, options).      |
| `find`        | `{ url?, session?, use_current?, query, max_hits? }` | Matching text snippets with the heading path locating each.           |
| `site`        | `{ url?, session?, use_current? }`                 | A host's agent-facing metadata: robots.txt summary (allow/disallow for a browser agent, crawl-delay, sitemaps) + llms.txt content + whether llms-full.txt exists. Context only — never blocks a fetch. |
| `click`       | `{ session, link_index? \| match?, render? }`      | Follows a link from the session's current page (cookies carried); returns a summary. `render=true` runs the destination's JavaScript first (needs `--js`). |
| `submit_form` | `{ session, form?, values?, files?, render? }`     | Submits a form from the current page (cookies carried); returns a summary. Forms declaring `enctype=multipart/form-data` are encoded as multipart automatically; `files` attaches uploads (`{field, filename?, mime?, content \| content_base64}`, capped 8 files / 4 MiB — content is supplied inline, never read from disk; needs a POST form). `render=true` runs the result page's JavaScript first (needs `--js`). |
| `controls`    | `{ url?, session?, use_current? }`                 | Non-link interactive controls (buttons, `role=button`, `onclick`/`tabindex`, submit/reset inputs, tabs, summaries), each with a stable CSS selector for `interact`. |
| `interact`    | `{ session, selector, event?, value? }`            | Dispatches an interaction at a selector and runs the page's JS so its handlers fire, then returns the updated page. `event` defaults to `click`, which emulates a **full primary-button press** (`pointerdown`→`mousedown`→focus→`pointerup`→`mouseup`→`click`) so press/pointer-based widgets (react-aria/Radix tabs, toggles, menus) actually activate — not just plain `onclick`; also `hover` (reveal hover menus/tooltips), `focus` (focus-triggered dropdowns), `input`, `change`, `keydown`, `submit`. The session keeps a **live JS runtime**, so state (variables, listeners, timers, fetched data) persists across calls. Requires `--js`. Does not navigate — but a handler that requests a cross-document navigation (`location.href`/`assign`/`replace`) surfaces the target as `pending_navigation` so you can follow it with `read`/`click`. |
| `data`        | `{ url?, session?, use_current?, kind? }`          | Machine-readable structured data embedded in a page: JSON-LD (schema.org), HTML data tables (caption/headers/rows), and microdata (itemscope/itemprop). `kind` selects `jsonld`, `tables`, `microdata`, or `all` (default). HTML only. *(Tables: colspan and rowspan are expanded onto the real grid; microdata `itemref` unsupported; JSON-LD `@graph` is flattened. `raw_html` returns source with relative URLs left as-is.)* |
| `session`     | `{ action: new\|list\|state\|history\|back\|forward\|close, session?, url?, headers?, cookies?, auth? }` | Manage a session's lifecycle and navigation. `new` accepts `url` + `headers`/`cookies`/`auth` to attach credentials for that origin (see [Authentication](#authentication)); re-creating a live id with new credentials errors (close it first). `list` returns every live session's state (including `live_js`, whether a persistent runtime is attached). |
| `map`         | `{ url, max_urls?, max_depth? }`                   | Discover a site's URLs: harvests sitemap.xml (robots.txt + `/sitemap.xml`, following sitemap indexes) and crawls same-origin links breadth-first from the seed. Returns a bounded, de-duplicated list tagged `source=sitemap\|crawl` with depth. Exposure-grade — surfaces robots.txt but never gates on it. Send an MCP progress token (`_meta.progressToken`) to stream progress while the walk (up to 60s) runs. |
| `search`      | `{ query, count?, site? }`                         | Web search via the configured provider (SearXNG or Brave): ranked results (title, url, snippet). `site` restricts to one domain. Requires `--search-provider` (see [Search](#search)); errors cleanly otherwise. |

### Authentication

Reach gated pages and JSON APIs by attaching credentials. Prefer a **session** —
set them once and they persist across calls, out of every per-call payload:

```jsonc
// session(action=new): bearer/basic + custom headers + cookies, all scoped to url
{ "action": "new", "session": "api",
  "url": "https://api.example.com",              // the origin credentials are pinned to
  "auth": { "type": "bearer", "token_env": "API_TOKEN" },   // or {type:"basic", username, password_env}
  "headers": { "X-Api-Key": "…" },
  "cookies": [ { "name": "sid", "value": "…" } ] }
```

Then any page tool using `session: "api"` carries the credentials. `read`/`browse`
also accept one-shot `headers`/`auth` for a quick stateless gated GET.

- **Secrets stay out of the transcript.** Give a secret literally (`token`/
  `password`) or, better, by env-var name (`token_env`/`password_env`) — unblink
  reads the value from its own environment. Session `state` reports only a redacted
  `auth_type`/`auth_scope`, never the secret; credential query params are masked in
  logs.
- **Origin-scoped, no cross-origin leak.** Credentials are pinned to the `url`'s
  origin: they are sent only there and are **stripped on any cross-origin redirect**
  (including a same-domain port change), so a bearer token can't be exfiltrated to
  another host. A `url` is required whenever you supply auth/headers/cookies.

### Search

The `search` tool is **opt-in** — unblink stays fully self-contained until you
point it at a provider, so it never reaches an external service by default:

```sh
# Self-hosted SearXNG (no key):
./bin/unblink --search-provider=searxng --search-endpoint=https://searx.example/

# Brave Search API (key from the environment, never a flag):
UNBLINK_SEARCH_API_KEY=… ./bin/unblink --search-provider=brave
```

Without `--search-provider` the tool is still listed but returns a clean "not
configured" error. The API key is read only from `UNBLINK_SEARCH_API_KEY`, sent
only as a request header, and never logged. Search only queries the provider —
result URLs are fetched later by `read`/`browse` through the SSRF-guarded path.

The `map` tool needs no configuration: it discovers URLs from a site's own
sitemap.xml and same-origin links, bounded by `max_urls`/`max_depth`.

### Networking

The HTTP layer decodes **brotli/gzip/deflate**, **rate-limits per host** and
**retries** transient failures (429/5xx, honoring `Retry-After`), and logs
(structured, to stderr — `--log-level`). `--tls-mimic` presents a browser
fingerprint to get past naive anti-bot blocks: a Chrome JA3/JA4 ClientHello (utls)
plus best-effort Chrome-tuned HTTP/2 SETTINGS and request headers (`sec-ch-ua`,
`Sec-Fetch-*`). Full h2 fingerprint fidelity (SETTINGS order, window sizes,
pseudo-header order) and Cloudflare/Turnstile remain out of scope. Tunable:
`--rate-limit`,
`--rate-burst`, `--retries`.

Every page fetch runs behind an **SSRF dial guard** that rejects connections to
private/loopback/link-local/metadata IPs — including CGNAT/`100.64.0.0/10` (Alibaba
metadata) — checked against the *resolved* address. It is on by default; pass
`--allow-private` for localhost/internal targets. (In-page JavaScript subrequests are
guarded separately by `--js-allow-private`.)

Because unblink feeds pages to an AI agent, returned web content is treated as
**untrusted by default**: it is wrapped in a provenance/"untrusted content" fence so
the model treats it as data (not instructions), human-hidden text and comments are
stripped, and Markdown image beacons (`![](url)` — a zero-click data-exfil channel)
are defanged to inert text. This is defense-in-depth against indirect prompt
injection, not a guarantee. Pass `--no-safe-output` to get the raw, unmodified
reduction instead. Page JavaScript under `--js` cannot read host files (`require`
is disabled) or read HttpOnly cookies via `document.cookie`.

robots.txt and llms.txt are **surfaced as context, never enforced** — unblink
reports a host's crawl rules (and `allowed_for_us` for the path) but never blocks
a fetch on them. `browse` folds in lightweight presence hints (host-cached, so
repeat browses are free); `--no-site-hints` disables that probe while the `site`
tool stays available.

## License

[MIT](LICENSE)
