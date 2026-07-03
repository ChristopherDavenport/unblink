# membench — cross-tool footprint, latency, and token benchmark

Measures unblink against the other AI web-browsing tools in
[`docs/comparison.md`](../../docs/comparison.md) on **identical local
fixtures**, and prints the markdown tables that document cites. Three metric
families:

- **Footprint** — binary/install size, cold start to MCP-ready, first page
  ready, idle/peak PSS of the whole process tree.
- **Latency/throughput** — per-fixture median render, sequential pages/s,
  pipelined-concurrency pages/s where the server supports it.
- **Token cost** — what an agent's context window pays per abstract task
  (`read-article`, `read-full`, `orient`), counted with an offline o200k_base
  BPE tokenizer, raw bytes alongside.

This is a **separate Go module** so its dependencies (chromedp, tiktoken)
never touch the published binary. Run from the repo root:

```sh
make membench                 # unblink vs. raw headless Chromium (the original comparison)
make crossbench               # every installed tool
make crossbench TOOLS=unblink,playwright
make crossbench ARGS="-selfcheck -dump-dir /tmp/dumps"
```

## The tools

Every MCP tool is driven the way a real client drives it: spawned as a
subprocess, newline-delimited JSON-RPC over stdio, `initialize` handshake
(timed as cold start), then `tools/call`. Tools that aren't installed are
skipped with a note — never a hard failure.

| Tool | How it's launched | Prerequisites |
|---|---|---|
| `unblink` | `bin/unblink --js` (run `make build`) | — |
| `chrome` | chromedp, raw headless Chromium baseline (not MCP) | a chrome/chromium binary (auto-detected from PATH, puppeteer, and playwright caches; or `CHROME=/path`) |
| `playwright` | `npx @playwright/mcp@<pinned> --headless --isolated --output-mode stdout` | Node ≥ 18 (npx fetches the pinned version) |
| `charlotte` | `npx @ticktockbent/charlotte@<pinned>` | Node ≥ 20 |
| `obscura` | `obscura mcp` | binary from [obscura releases](https://github.com/h4ckf0r0day/obscura/releases): `-obscura-bin`, `$OBSCURA_BIN`, PATH, or `~/.cache/unblink-membench/obscura` |
| `lightpanda` | `lightpanda mcp` | nightly binary from [lightpanda releases](https://github.com/lightpanda-io/browser/releases) (glibc-only Linux): `-lightpanda-bin`, `$LIGHTPANDA_BIN`, PATH, or `~/.cache/unblink-membench/lightpanda` |

Install the external binaries (deliberately not automated):

```sh
mkdir -p ~/.cache/unblink-membench
curl -L https://github.com/h4ckf0r0day/obscura/releases/latest/download/obscura-x86_64-linux.tar.gz \
  | tar xz -C ~/.cache/unblink-membench/
curl -L -o ~/.cache/unblink-membench/lightpanda \
  https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-x86_64-linux
chmod +x ~/.cache/unblink-membench/lightpanda
```

## Fairness rules

- **Same fixtures, same local server** for every tool — no network variance.
  The footprint/latency corpus is the original 4 pages (static article + real
  React/Vue/Lit SPAs); the token corpus adds three static pages (a junk-laden
  article, a long read, and a generated ~90 KB nav-heavy portal) so
  JS-coverage differences can't distort the token metric.
- **Shared Chromium**: playwright (`--executable-path`) and charlotte
  (`PUPPETEER_EXECUTABLE_PATH`) drive the same binary as the raw baseline.
- **Tool defaults only**, with one recorded exception: unblink runs with
  `--rate-limit 0`. Its default politeness limiter (5 req/s per host) is a
  crawl-courtesy policy, and the whole corpus is served from a single loopback
  host with cache-busted page URLs — under the default, every SPA latency
  median collapses to token pacing (~200 ms/request) regardless of engine
  speed, and sequential throughput pins at exactly the limiter rate. No other
  benchmarked tool ships such a limiter, so disabling it measures capability
  against capability. (Numbers published before 2026-07-03 included the
  limiter; their render-latency and sequential-throughput rows measured the
  policy, not the engine.) The task set is abstract: `read-article` is each
  tool's canonical way to get the page's content in front of the model,
  `read-full` retrieves the *whole* document (unblink follows pagination
  cursors — its budget cap can't masquerade as efficiency), `orient` is the
  cheapest "what's on this page" call (`n/a` where a tool has none — that
  absence is a finding).
- **Sentinel validation**: every counted output must contain a
  fixture-specific content string (page title for `orient`). Failures print
  as `fail`, never as numbers — an error message can't score as token
  efficiency.
- **Memory is PSS** of the whole process tree (`/proc/<pid>/smaps_rollup`),
  so a multi-process browser's shared pages are counted once. Cross-check
  with `-debug-mem` against `smem`/`ps`.
- **Cold start** is spawn → `initialize` response; npx-based tools get one
  throwaway warm-up start first, so package download/extraction never
  pollutes the figure. "First page ready" captures lazily-launched browsers.
- **Concurrency** is only measured where the server actually dispatches
  concurrent requests (unblink pipelines on one stdio connection; the raw
  Chromium baseline opens tabs). Single-browsing-context servers print `n/a`
  rather than a number measured by corrupting interleaved navigations.
- Versions are pinned (npx) or recorded (initialize `serverInfo`), and the
  run header prints date/kernel/CPU/RAM — the provenance line the doc cites.

## Publishing numbers

```sh
make crossbench ARGS="-selfcheck"   # directional sanity assertions must pass
```

Run 3×, take medians, and update `docs/comparison.md`'s measured sections
(footprint, render speed, token cost) plus its provenance line. `-dump-dir`
writes every counted task output for eyeball review; `-probe <tool>` prints a
server's identity and tool list (use it to re-pin adapter vocabularies when
bumping pinned versions).

## Content-boundary probe

```sh
make crossbench ARGS="-safety"      # regenerates docs/comparison.md's content-boundary table
```

Separate from the numeric passes: instead of *how much* a read costs, this
measures *what an untrusted page can smuggle into the model*. It drives each
tool's `read-article` against a fixture carrying three hidden-instruction
blocks (`display:none`, `aria-hidden`, off-screen) and an image-beacon
exfiltration URL, plus a one-page PDF, and reports — per tool — how many hidden
blocks leaked, whether the beacon reached the model live/inert/dropped, whether
the output was fenced as untrusted, and how the PDF read came back. The fixture
lives in `safety.go` (no external corpus beyond the repo's `eval/corpus/sample.pdf`).

Linux-only (WSL2 included): memory sampling reads `/proc`.
