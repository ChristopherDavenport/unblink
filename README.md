# unblink

[![CI](https://github.com/ChristopherDavenport/unblink/actions/workflows/ci.yml/badge.svg)](https://github.com/ChristopherDavenport/unblink/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ChristopherDavenport/unblink)](https://github.com/ChristopherDavenport/unblink/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/christopherdavenport/unblink.svg)](https://pkg.go.dev/github.com/christopherdavenport/unblink)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

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

**v0.18.0.** The full pipeline works end to end: 14 MCP tools covering reading,
navigation, sessions, forms, structured data, site discovery, and search. The
static read path turns most server-rendered pages into clean Markdown with zero
JavaScript; the JavaScript engine — **on by default** (opt out with `--disable-js`)
— renders mainstream SPA frameworks and powers live interactive sessions
(`interact`). It ships as one
static binary (~36 MB) with a ~26 MB idle footprint — measured head-to-head on
identical pages, roughly **5× lighter idle and 10× faster to start than a
headless Chromium**, turning SPA fixtures around in **~2–10 ms per render**
(ahead of the warm-browser MCP tools on the same corpus — the settle proves
idleness instead of waiting out a quiet window, ADR 0004), and it reads a
nav-heavy page for **~1% of the tokens** of a browser-tool accessibility
snapshot ([measured](docs/comparison.md#measured-head-to-head), against all
four alternatives). See
[docs/architecture.md](docs/architecture.md) for the full design (phases 0–23)
and its non-goals, and [docs/comparison.md](docs/comparison.md) for how unblink
compares to other AI web-browsing tools (Playwright MCP, Charlotte, Obscura,
Lightpanda).

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

Or run the multi-arch (amd64/arm64) Docker image — no Go toolchain needed:

```sh
docker run -i --rm ghcr.io/christopherdavenport/unblink:latest --version
```

Or download a prebuilt binary from the GitHub releases page, or build from
source (see [Build & run](#build--run)).

## Use it with an MCP client

unblink speaks MCP over stdio, so any MCP-capable client launches it as a
subprocess. For **Claude Code**:

```sh
claude mcp add unblink -- /path/to/unblink
# or, via Docker (no install):
claude mcp add unblink -- docker run -i --rm ghcr.io/christopherdavenport/unblink:latest
```

For **Claude Desktop** (or any client using the `mcpServers` config shape), add
to `claude_desktop_config.json`:

```jsonc
{
  "mcpServers": {
    "unblink": {
      "command": "/path/to/unblink",
      "args": []
    }
    // or, via Docker:
    // "unblink": {
    //   "command": "docker",
    //   "args": ["run", "-i", "--rm", "ghcr.io/christopherdavenport/unblink:latest"]
    // }
  }
}
```

JavaScript rendering is on by default; add `--disable-js` for the zero-JavaScript
static read path (lighter, still handles most server-rendered pages). Add flags
like `--search-provider` or `--tls-mimic` to `args` as needed — see
[Configuration](#configuration).

unblink is also listed in the [MCP registry](https://registry.modelcontextprotocol.io)
as `io.github.ChristopherDavenport/unblink`, and the repo ships a Claude Code
plugin manifest ([`.claude-plugin/plugin.json`](.claude-plugin/plugin.json))
pinned to the current release image.

## Build & run

```sh
make build           # -> bin/unblink
make test            # run the test suite
./bin/unblink            # serve MCP over stdio (JavaScript rendering on by default)
./bin/unblink --disable-js  # zero-JavaScript static read path only
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
The engine (on by default) renders the **mainstream SPA frameworks** (React, Vue,
Preact, Svelte, Lit / web components) via a flat-DOM model — a real
Node/Element/HTMLElement prototype chain, MutationObserver, custom-element upgrade,
and an **encapsulating, composed Shadow DOM**: each shadow root is a detached subtree
(so page JS `querySelector` respects the boundary), and a compose pass flattens it —
resolving `<slot>` distribution — into the light tree for extraction. Events cross the
boundary correctly (composed path, `target` retargeting, `composedPath()`), and
declarative Shadow DOM (`<template shadowrootmode>`) renders on the static no-JS path.
Layout/geometry is constant-stubbed (no pixel layout engine), and canvas/WebGL,
Workers/WebSocket/IndexedDB, and Shadow-DOM *style scoping*
(`:host`/`::slotted`/`::part`) remain out of scope.

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
JavaScript first — **on by default**; pass `render=false` to skip it, or start the
server with `--disable-js` to turn JS off entirely).

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
| `read`        | `{ url?, session?, use_current?, mode?, format?, selector?, max_tokens?, cursor?, wait_for?, wait_text?, wait_timeout?, headers?, auth? }` | Main content (`mode=article`, default) or whole page (`full`) as Markdown, paginated via cursor. `format=raw_html` returns the unreduced source (optionally scoped by a CSS `selector`) — the escape hatch for scripts/forms/SSR-embedded JSON that reduction strips; `format=text` returns visible plain text. `wait_for` (CSS selector) / `wait_text` hold the JS render open until that content hydrates (forces the render even if `render=false`); `wait_timeout` (seconds, capped ~30s) extends the wait, and `wait_met` in the result reports whether it appeared. `headers`/`auth` attach one-shot credentials for a stateless gated GET (see [Authentication](#authentication)). |
| `browse`      | `{ url?, session?, use_current?, headers?, auth? }` | Cheap orientation: title, description, lang, heading outline, link/form/image counts, excerpt, plus `llms_txt`/`robots` presence hints. |
| `links`       | `{ url?, session?, use_current?, filter?, internal_only?, limit? }` | The page's links (text + absolute href), optionally filtered. `limit` defaults to 200 (cap 1000); `total`/`truncated` report the rest. |
| `forms`       | `{ url?, session?, use_current? }`                 | The page's forms and their fields (name, type, required, options).      |
| `find`        | `{ url?, session?, use_current?, query, max_hits? }` | Matching text snippets with the heading path locating each.           |
| `site`        | `{ url?, session?, use_current? }`                 | A host's agent-facing metadata: robots.txt summary (allow/disallow for a browser agent, crawl-delay, sitemaps) + llms.txt content + whether llms-full.txt exists. Context only — never blocks a fetch. |
| `click`       | `{ session, link_index? \| match?, render? }`      | Follows a link from the session's current page (cookies carried); returns a summary. The destination's JavaScript runs by default; pass `render=false` to skip it. |
| `submit_form` | `{ session, form?, values?, files?, render? }`     | Submits a form from the current page (cookies carried); returns a summary. Forms declaring `enctype=multipart/form-data` are encoded as multipart automatically; `files` attaches uploads (`{field, filename?, mime?, content \| content_base64}`, capped 8 files / 4 MiB — content is supplied inline, never read from disk; needs a POST form). The result page's JavaScript runs by default; pass `render=false` to skip it. |
| `controls`    | `{ url?, session?, use_current? }`                 | Non-link interactive controls (buttons, `role=button`, `onclick`/`tabindex`, submit/reset inputs, tabs, summaries), each with a stable CSS selector for `interact`. |
| `interact`    | `{ session, selector, event?, value?, key? }`      | Dispatches an interaction at a selector and runs the page's JS so its handlers fire, then returns the updated page. `event` defaults to `click`, which emulates a **full primary-button press** (`pointerdown`→`mousedown`→focus→`pointerup`→`mouseup`→`click`) so press/pointer-based widgets (react-aria/Radix tabs, toggles, menus) actually activate — not just plain `onclick`; also `hover` (reveal hover menus/tooltips), `focus` (focus-triggered dropdowns), `input`, `change`, `keydown`/`keyup`/`keypress`, `submit`. For key events, `key` names the key (`Enter`, `Escape`, `ArrowDown`, a single character…; defaults to `Enter`) so handlers reading `e.key`/`e.keyCode` fire — and **`Enter` on a control inside a form submits it**; combine with `value` to type-then-press (`{event:"keydown", value:"query", key:"Enter"}` drives a search box). The session keeps a **live JS runtime**, so state (variables, listeners, timers, fetched data) persists across calls. Needs JS (on by default; `--disable-js` turns it off). Does not navigate — but a handler that requests a cross-document navigation (`location.href`/`assign`/`replace`) surfaces the target as `pending_navigation` so you can follow it with `read`/`click`. |
| `data`        | `{ url?, session?, use_current?, kind? }`          | Machine-readable structured data embedded in a page: JSON-LD (schema.org), HTML data tables (caption/headers/rows), and microdata (itemscope/itemprop). `kind` selects `jsonld`, `tables`, `microdata`, or `all` (default). HTML only. *(Tables: colspan and rowspan are expanded onto the real grid; microdata `itemref` unsupported; JSON-LD `@graph` is flattened. `raw_html` returns source with relative URLs left as-is.)* |
| `requests`    | `{ url?, session?, use_current? }`                 | The network requests the page's JavaScript made while rendering (fetch/XHR, scripts, modules, dynamic imports), each with method/url/status. The escape hatch for data-driven pages: render once, see the JSON endpoint the page fetched, then `read` it directly instead of scraping the hydrated DOM. Requires JavaScript (not exposed under `--disable-js`). |
| `console`     | `{ url?, session?, use_current?, level? }`         | The page's captured console output (log/info/warn/error/debug) from its JavaScript render, in order — for debugging why a page rendered as it did (boot errors, failed data loads, framework warnings). `level` filters to one severity. Requires JavaScript (not exposed under `--disable-js`). |
| `cookies`     | `{ session, action?, url?, cookies? }`             | Inspect or change a session's cookies, scoped to an origin (`url`, else the session's current page). `action=list` (default) returns the jar's cookies as name/value; `set` adds/updates the cookies you pass; `clear` expires them. Requires a session. |
| `session`     | `{ action: new\|list\|state\|history\|back\|forward\|close, session?, url?, headers?, cookies?, auth? }` | Manage a session's lifecycle and navigation. `new` accepts `url` + `headers`/`cookies`/`auth` to attach credentials for that origin (see [Authentication](#authentication)); re-creating a live id with new credentials errors (close it first). `list` returns every live session's state (including `live_js`, whether a persistent runtime is attached). |
| `map`         | `{ url, max_urls?, max_depth? }`                   | Discover a site's URLs: harvests sitemap.xml (robots.txt + `/sitemap.xml`, following sitemap indexes) and crawls same-origin links breadth-first from the seed. Returns a bounded, de-duplicated list tagged `source=sitemap\|crawl` with depth. Exposure-grade — surfaces robots.txt but never gates on it. Send an MCP progress token (`_meta.progressToken`) to stream progress while the walk (up to 60s) runs. |
| `search`      | `{ query, count?, site? }`                         | Web search via the configured provider (SearXNG or Brave): ranked results (title, url, snippet). `site` restricts to one domain. Requires `--search-provider` (see [Search](#search)); not exposed otherwise. |

### Tool selection

Every tool the server advertises costs the model context on each turn, so unblink
exposes only tools that can actually do something, and lets you narrow that further:

- **Unusable tools are hidden automatically.** `search` isn't exposed without a
  `--search-provider`, and `interact`/`requests`/`console` aren't exposed under
  `--disable-js` — a tool that could only return an error is noise in the tool list.
- **Pick a subset with `--tools`.** Pass a comma-separated list of tool names
  and/or presets. Presets: `core` (`read`, `browse`, `find`), `read-only` (every
  read-only tool — no sessions, navigation, or form/interaction writes), and
  `full` (everything, the default). `--disable-tools` subtracts from the set.

```sh
./bin/unblink --tools core                 # minimal reading surface: read, browse, find
./bin/unblink --tools read-only            # all read-only tools, no state changes
./bin/unblink --tools core,data            # a preset plus an extra tool
./bin/unblink --disable-tools map,search   # everything except these
```

A tool named explicitly that can't run (e.g. `--tools interact` with `--disable-js`)
is dropped with a stderr warning; unknown names are ignored with a warning.

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

Without `--search-provider` the `search` tool isn't exposed at all (a tool that
could only return "not configured" is context cost with no value — see
[Tool selection](#tool-selection)). The API key is read only from `UNBLINK_SEARCH_API_KEY`, sent
only as a request header, and never logged. Search only queries the provider —
result URLs are fetched later by `read`/`browse` through the SSRF-guarded path.

The `map` tool needs no configuration: it discovers URLs from a site's own
sitemap.xml and same-origin links, bounded by `max_urls`/`max_depth`.

### Networking

The HTTP layer decodes **brotli/gzip/deflate**, can **rate-limit per host**
(opt-in politeness limiter, `--rate-limit`; off by default so throughput is
bounded by the site, not by unblink), **retries** transient failures
(429/5xx, honoring `Retry-After`), and logs
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
reduction instead. Page JavaScript cannot read host files (`require`
is disabled) or read HttpOnly cookies via `document.cookie`.

robots.txt and llms.txt are **surfaced as context, never enforced** — unblink
reports a host's crawl rules (and `allowed_for_us` for the path) but never blocks
a fetch on them. `browse` folds in lightweight presence hints (host-cached, so
repeat browses are free); `--no-site-hints` disables that probe while the `site`
tool stays available.

## Configuration

All configuration is via CLI flags (pass them in your MCP client's `args`).
`--version` prints the build and exits.

| Flag | Default | What it does |
| --- | --- | --- |
| `--log-level` | `warn` | Log level (`debug`/`info`/`warn`/`error`); logs go to stderr, stdout is reserved for MCP. |
| `--rate-limit` | off | Per-host requests/sec politeness limiter (opt-in; e.g. `5` to crawl politely). |
| `--rate-burst` | `10` | Per-host request burst (used when `--rate-limit` is set). |
| `--retries` | `2` | Retries for transient fetch failures (429/5xx, honoring `Retry-After`). |
| `--tls-mimic` | off | Present a Chrome TLS/h2 fingerprint (utls) to get past naive anti-bot blocks; also sets `navigator.webdriver=false`. |
| `--allow-private` | off | Permit page fetches to private/loopback/metadata IPs (needed for localhost/internal targets). |
| `--no-site-hints` | off | Omit robots.txt/llms.txt presence hints from `browse`. |
| `--no-safe-output` | off | Disable the untrusted-content safety pass (fence, hidden-text strip, image-beacon defang) — return raw reduction. |
| `--disable-js` | off | Disable JavaScript rendering entirely (JS is **on by default**; reads render unless the caller passes `render=false`). |
| `--js-timeout` | `5s` | Per-render wall-clock budget for JavaScript. |
| `--js-no-network` | off | Disable page-JS network requests (DOM-only render). |
| `--js-allow-private` | off | Permit page-JS subrequests to private/loopback IPs. |
| `--js-max-requests` | `50` | Max page-JS network requests per render. |
| `--js-prewarm` | `4` | Pre-warmed JS runtimes kept ready (0 disables). |
| `--js-concurrency` | auto | Max concurrent JS renders (auto = CPU count clamped to 4..16). Same-host fetch pacing stays `--rate-limit`'s job. |
| `--js-max-live` | `16` | Max concurrent live per-session JS runtimes (LRU torn down over the cap). |
| `--js-memory-limit` | `1024` | MiB of Go heap page JS may grow before every render is interrupted (0 disables the guard). |
| `--js-asset-cache` | on | Cache page-JS script/module/bundle downloads across renders for 60s (data fetch/XHR never cached). |
| `--session-ttl` | `30m` | Idle time before a session is evicted. |
| `--session-cap` | `256` | Max concurrent sessions (oldest evicted on overflow). |
| `--search-provider` | none | Web-search backend for the `search` tool: `searxng` or `brave` (empty disables it). |
| `--search-endpoint` | none | Search endpoint URL (SearXNG base URL; optional Brave override). API key comes from `UNBLINK_SEARCH_API_KEY`. |
| `--tools` | all | Limit the exposed tools to a comma-separated list of tool names and/or presets (`core`, `read-only`, `full`). Empty exposes every usable tool. |
| `--disable-tools` | none | Remove tools from the exposed set (comma-separated names/presets), applied after `--tools`. |

## License

[MIT](LICENSE)

## Security

unblink runs untrusted page content (and, with JS enabled — the default — untrusted
page JavaScript) as part of its job. See [SECURITY.md](SECURITY.md) for the threat
model and how to report a vulnerability.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the build/test workflow, the eval
gate, and the ADR process. Changes to the JS engine or dependency pins follow the
[architecture doc](docs/architecture.md) and [ADRs](docs/decisions/).
