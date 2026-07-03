# unblink vs. other AI web-browsing tools

*Comparison as of July 2026. Every project here moves fast — versions and
numbers below will drift. Third-party performance figures are each vendor's own
published claims, linked in [Sources](#sources). unblink's own numbers vs. a
headless Chromium baseline are measured and reproducible — see
[Measured footprint](#measured-footprint).*

Tools that give AI agents access to the web differ along two axes:

1. **What executes the page's JavaScript.** A real browser (Playwright MCP,
   Charlotte), a from-scratch engine embedding real V8 (Obscura, Lightpanda),
   or a pure-Go interpreter with a hand-rolled DOM (unblink).
2. **What the model receives.** Automation primitives over a live DOM
   (Obscura, Lightpanda), accessibility-tree snapshots (Playwright MCP), a
   typed structural decomposition (Charlotte), or reduced, token-budgeted
   Markdown (unblink).

unblink sits at the far end of both axes. It is not a browser-automation tool
that happens to speak MCP; it is a *semantic reduction* tool — fetch a page,
parse it, optionally run its JavaScript, throw away everything that exists
only for human eyes, and hand the model clean Markdown under a token budget.
An AI doesn't need pixels; it needs structured meaning. That bet buys a single
static binary with no browser install and some safety properties none of the
tools below have — and it forfeits real-browser fidelity. This document is
explicit about both directions.

## At a glance

| | **unblink** | **Playwright MCP** | **Charlotte** | **Obscura** | **Lightpanda** |
|---|---|---|---|---|---|
| Engine | Pure Go, hand-rolled DOM (no browser engine) | Real Chrome / Firefox / WebKit / Edge | Real headless Chromium (Puppeteer/CDP) | From-scratch engine in Rust | From-scratch engine in Zig |
| JavaScript | goja (Go interpreter), opt-in `--js`; renders React/Vue/Preact/Svelte/Lit via a flat-DOM model | Full (real browser) | Full (real Chromium) | Embedded V8 | Embedded V8; beta — "hundreds of Web APIs" unimplemented |
| Agent protocol | MCP (stdio), 14 tools | MCP (Node), 50+ tools | MCP (Node ≥ 20), 43 tools in profiles (7/23/43) | CDP (Puppeteer/Playwright drop-in) + MCP (11 tools) | CDP (WebSocket) + MCP (stdio) + CLI agent mode |
| Page representation | Reduced Markdown (article or full page) + structured tools (outline, links, forms, JSON-LD/tables) | Accessibility-tree snapshots | Typed page decomposition, 3 detail levels, targeted `find` queries | DOM automation primitives (navigate/click/fill/evaluate) | DOM automation primitives; some semantic extraction |
| Screenshots / pixels | No — permanent non-goal | Yes, plus video, tracing, PDF export | Yes (real Chromium) | Not a focus | No — no graphical rendering |
| Install footprint | One static binary (linux/darwin/windows), nothing else; ~36 MB, ~25 MB idle RSS ([measured](#measured-footprint)) | Node + full browser install | Node + Chromium (npm or Docker) | ~70 MB binary; claims 30 MB RAM, <50 ms boot | Single binary; claims ~16× less memory, ~9× faster than headless Chrome; no native Windows, glibc-only Linux |
| Token efficiency | Core design: token-budgeted, cursor-paginated Markdown; cheap `browse`/`find` orientation | Verbose — its own docs note CLI workflows are more token-efficient | Core design: orientation 23–178× smaller than a11y-tree dumps | Not a representation-level concern | Not a representation-level concern |
| Sessions | Cookies + history + persistent live JS runtime per session | Real browser profile, tabs, storage | Persistent Chromium session, stable hashed element IDs, structural diffs | Fast-boot ephemeral sessions | CDP sessions |
| Agent-safety defaults | SSRF dial guard + untrusted-content fence + origin-scoped credentials, all on by default | — | Chromium sandbox on by default | Stealth / anti-fingerprinting, tracker blocking (a different goal) | robots.txt respect, proxy support |
| License | MIT | Apache-2.0 | MIT | Apache-2.0 | AGPL-3.0 |
| Maturity (07/2026) | v0.17.0 | Mature incumbent (in GitHub Copilot's coding agent) | v0.7.0 | v0.1.9 | Beta |

## The tools

### Playwright MCP

Microsoft's MCP server over Playwright: a real Chrome, Firefox, WebKit, or
Edge, exposed to the model as structured accessibility-tree snapshots rather
than screenshots. 50+ tools cover navigation, forms, tabs, network mocking,
storage, tracing, video, screenshots, PDF export, and test assertions. It is
the incumbent — GitHub Copilot's coding agent ships with it.

**Where it beats unblink:** everything that needs a real browser. Perfect
JS/CSS fidelity, real layout and geometry, screenshots and video, E2E testing
against the app you're building, sites gated by serious anti-bot
(Turnstile-class challenges unblink explicitly cannot pass). If the task is
"verify this UI change renders correctly," there is no substitute.

**What it costs:** a Node runtime plus a full browser install, the heaviest
per-page resource footprint here, and token-verbose snapshots — Playwright's
own docs steer token-sensitive workflows toward its CLI instead. Nothing in
its output model reduces a page to meaning; the model wades through the
accessibility tree itself.

### Charlotte

The closest project to unblink in spirit, with the opposite architecture. It
keeps a persistent real headless Chromium (via Puppeteer/CDP) and puts a
token-efficiency layer on top: pages decompose into a typed structure
(landmarks, headings, interactive elements, forms) at three detail levels,
agents pull specifics with `find` queries, and element IDs are stable hashes
that survive minor DOM mutation. 43 tools, loaded in profiles so small
workflows don't pay for the full set.

**Where it beats unblink:** full Chromium fidelity under the efficient
surface — any page Chrome can run, Charlotte can represent, including
screenshots when the agent needs eyes. Its interactive-element model
(stable IDs, structural diffs) is richer for long multi-step UI driving than
unblink's selector-based `controls`/`interact`.

**What it costs:** the Chromium dependency (Node ≥ 20 plus a browser, or a
Docker image) and Chromium's memory profile per session. It optimizes what
the *model* pays per page, not what the *host* pays to run the browser. Young
project (v0.7.0).

### Obscura

A from-scratch headless browser in Rust with V8 embedded, positioned as a
drop-in replacement for headless Chrome: existing Puppeteer/Playwright
scripts work over CDP, and an MCP server exposes 11 automation tools
(`browser_navigate`, `browser_click`, `browser_fill`, `browser_evaluate`,
`browser_wait_for`, …). Claims 30 MB memory and sub-50 ms session boot versus
200+ MB and seconds for headless Chrome, plus a `--stealth` mode
(per-session fingerprint randomization, ~3,520 tracker domains blocked).

**Where it beats unblink:** real V8 means real-world JS compatibility beyond
what goja reaches, at a footprint far below Chrome's; CDP compatibility means
the whole Puppeteer/Playwright ecosystem just works; stealth tooling for
scraping-adjacent work is a first-class feature unblink deliberately keeps
minimal (`--tls-mimic` is best-effort, and evading bot detection is not a
goal).

**What it costs:** it hands the agent a browser, not meaning — the model gets
automation primitives and raw DOM access, and token-efficient representation
is its problem. Very early (v0.1.9), so the usual young-engine caveats apply.

### Lightpanda

A ground-up browser engine in Zig with V8, aimed at headless automation and
AI agents — no Chromium, no WebKit, no graphical rendering. Speaks CDP over
WebSocket (Puppeteer/Playwright compatible), MCP over stdio, and has a CLI
agent mode. Claims ~16× less memory and ~9× faster than headless Chrome on
its 100-page benchmark.

**Where it beats unblink:** real V8 compatibility and the CDP ecosystem, like
Obscura, in a single binary. Its trajectory is "Chrome-class engine without
Chrome's weight," which covers automation use cases unblink never will.

**What it costs:** it is explicitly beta — hundreds of Web APIs
unimplemented, some sites still crash — so today its practical coverage of
the JS web is a moving target, much like unblink's flat-DOM model but without
the static-reduction fallback unblink leans on when JS isn't needed. Platform
constraints (no native Windows, glibc-only Linux) and AGPL-3.0 licensing may
matter for embedding.

## How unblink differs

unblink refuses the premise the other four share: that an agent needs a
browser. Its pipeline — fetch, parse HTML5, optionally execute JS against a
hand-rolled DOM, reduce, emit Markdown — never computes a pixel. That single
decision drives everything distinctive about it, good and bad.

**What the bet buys:**

- **Zero-dependency deployment.** One static pure-Go binary (no cgo, no
  Chromium, no Node, no V8) cross-compiled for linux/darwin/windows on
  amd64/arm64. There is nothing to install next to it and no browser process
  to babysit.
- **Meaning, not markup.** `read` returns reduced, sanitized Markdown under
  an explicit token budget with cursor pagination — never an unbounded blob.
  `browse` gives a ~free orientation pass; `find`, `links`, `forms`, `data`
  (JSON-LD / tables / microdata) answer questions without shipping the page.
  PDFs, RSS/Atom/JSON feeds, and images are handled transparently by the same
  `read` tool.
- **Agent-trust hardening no one else has.** Returned content is treated as
  untrusted *by the model*: wrapped in a provenance fence so it reads as data
  rather than instructions, human-hidden text stripped, Markdown image
  beacons (a zero-click exfiltration channel) defanged — on by default.
  Every fetch, including page-JS subrequests, sits behind an SSRF dial guard
  checked against resolved IPs. Credentials are origin-pinned and stripped on
  any cross-origin redirect. The other tools secure the *browser*; unblink
  also hardens the *content boundary* between the web and the model.
- **Discovery built in.** `map` (sitemap harvest + bounded same-origin
  crawl), `site` (robots.txt/llms.txt surfaced as context, never enforced),
  and opt-in `search` make it a research tool, not only a page loader.

**What it forfeits:**

- **JS compatibility has a ceiling.** goja is not V8. The flat-DOM model
  renders mainstream React/Vue/Preact/Svelte/Lit apps, but layout/geometry
  are constant stubs, and canvas/WebGL, Workers, WebSocket, and true
  Shadow-DOM encapsulation are permanent non-goals. Pages whose content
  depends on those won't fully materialize.
- **No pixels, ever.** No screenshots, no visual verification, no "does this
  look right" — by design.
- **An anti-bot ceiling.** `--tls-mimic` gets past naive fingerprint checks
  and the engine clears trivial JS interstitials, but Turnstile-class
  interactive challenges and server-side proof-of-work are out of scope.

## Which tool when

- **Screenshots, E2E testing, pixel-perfect fidelity, hostile anti-bot** →
  Playwright MCP. It's a real browser; nothing else here is.
- **Real-Chromium coverage with a token-efficient agent surface** →
  Charlotte. Chromium fidelity underneath, structured economy on top.
- **A low-footprint CDP drop-in for existing Puppeteer/Playwright automation**
  → Obscura (or Lightpanda, accepting beta coverage and AGPL). Real V8
  without Chrome's weight.
- **Reading, extracting, and researching the web for a model at minimal
  token cost, footprint, and attack surface** → unblink. Content for
  reasoning, not automation fidelity.

These compose: an agent can use unblink for the hundred pages it reads and a
real browser for the one page it must drive or see.

## Measured footprint

Head-to-head against a headless Chromium baseline, on **identical local
fixtures** (a static article plus real React / Vue / Lit SPAs), driving unblink's
`read` (with `render:true`) and Chromium via CDP through the same pages. Memory is
**PSS** (proportional set size — shared pages counted once), so Chromium's
multi-process tree isn't double-counted. Reproduce with `make membench` (see
[`scripts/membench`](../scripts/membench), a separate module so its chromedp
dependency never touches the published binary).

Measured 2026-07-02 · WSL2 (Linux 6.6, 4 vCPU / 5.8 GiB) · unblink v0.17.0 ·
Chromium 150.0.7871.46 · median of 3 runs:

| Metric | unblink | Chromium (headless) | Ratio |
|---|---|---|---|
| Binary on disk | 36 MB | 268 MB | 7.4× smaller |
| Cold start → ready | 15 ms | 232 ms | 15× faster |
| Idle RSS (post-init) | 25 MB | 387 MB | 15× less |
| Peak RSS (4 fixture renders) | 44 MB | 422 MB | 10× less |
| Peak RSS (8 concurrent) | 44 MB | 512 MB | 12× less |

The gap widens under concurrency: unblink's per-render runtime is bounded and
short-lived (a goja heap + event loop, torn down after the snapshot), so eight
simultaneous renders barely move its RSS, while each Chromium tab adds a renderer
process.

### Render speed

Same harness, timing each engine's URL→ready round trip on a cache miss (both
engines get the same ~60 ms quiet window; unblink's number includes the full
`read` — fetch, parse, JS render, semantic reduction, Markdown emit):

| Page | unblink | Chromium (headless) |
|---|---|---|
| Static / server-rendered article | **~2 ms** | ~110 ms |
| React SPA render | ~200 ms | ~120 ms |
| Vue SPA render | ~200 ms | ~120 ms |
| Lit SPA render | ~200 ms | ~115 ms |
| Throughput (mixed corpus, sequential) | ~5 pages/s | ~8 pages/s |

Two honest halves. On **static and server-rendered pages** — the bulk of what an
agent reads (articles, docs, product pages, most content sites) — unblink is
~**50× faster**: no browser to drive, just fetch → parse → reduce. On a **heavy
client-side SPA** unblink is ~**1.7× slower** per page: goja is a tree-walking
interpreter, Chromium's V8 is a JIT, and executing a full React/Vue bundle is
exactly where that gap shows. The mixed-corpus throughput here is dragged down by
its 3:1 SPA-to-static ratio; a realistic reading corpus (mostly SSR/static) tilts
hard the other way.

Both figures are per-call latency: over one MCP stdio connection the server is
single-flight (an agent awaits each tool result), so unblink does not run a
Chromium-style tab farm — throughput is `1 / latency`, not parallel tabs. The
takeaway matches the thesis: unblink is a fast **semantic reducer** for the pages
agents mostly read, not a faster **renderer** than Chrome for the SPAs it doesn't.

**Honest caveats.** This is not a like-for-like capability comparison — Chromium
does full layout, paint, and compositing (and can screenshot); unblink does
semantic reduction and never rasterizes a pixel. The numbers are machine-specific
and reflect these fixtures, not the whole web. They are the floor of what a real
browser costs, not a claim that unblink *replaces* one — see
[Which tool when](#which-tool-when).

## Sources

- unblink — this repository ([README](../README.md),
  [architecture](architecture.md))
- Playwright MCP — <https://github.com/microsoft/playwright-mcp>,
  <https://playwright.dev/mcp/introduction>
- Charlotte — <https://github.com/TickTockBent/charlotte>
- Obscura — <https://github.com/h4ckf0r0day/obscura>, <https://obscura.sh>
- Lightpanda — <https://github.com/lightpanda-io/browser>,
  <https://lightpanda.io>
