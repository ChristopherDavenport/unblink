# The real-page corpus

`make corpus` answers "does unblink work on real pages?" by replaying pages
captured from the live web and scoring the output against what headless
Chromium rendered. It is an on-demand measurement, not a CI gate. The design
and its trade-offs are in [ADR 0022](decisions/0022-real-page-differential-corpus.md).

## Capture (once, needs network + Chromium)

```sh
make corpus-capture                    # every manifest page not yet captured
make corpus-capture ARGS="-only vue-guide,lit-docs -force"
make corpus-capture CHROME=/path/to/chrome
```

The tool (`scripts/corpus`, a separate stdlib-only module) loads each page in
`corpus/manifest.json` once, sequentially, with a pause between loads and no
retries. Chromium is auto-detected on `PATH` and in the puppeteer/playwright
caches. For each page it writes, under `testdata/corpus/<id>/`:

| file | contents |
|---|---|
| `archive.json` | every network exchange: method, URL, status, headers, and the SHA-256 of any request body |
| `bodies/<sha256>` | response bodies, named by content hash |
| `oracle.json` | `innerText` of the body and main landmark, title, visible link/form/control counts, whether the body had a layout box, the settle rule and how long it watched, Chromium version, capture time |

`testdata/corpus/` is gitignored: captures are third-party content and are
never committed. Image, media and font bodies are not stored (unblink never
requests them).

What the capture does and does not record:

- **Redirects** are recorded hop by hop, so replay follows the same chain.
- **Service workers are bypassed.** A worker that installed mid-load would
  answer later requests from a session the capture cannot see; unblink has no
  service workers, so the page is captured talking to the network directly.
- **The HTTP cache is disabled**, so nothing is answered without a request.
- **Request bodies** are recorded as a hash only. Replay uses it to tell apart
  repeated POSTs to one URL; the bytes themselves are not kept.
- **A load cut short after its headers** (the page aborted a prefetch, a
  beacon was torn down) has no body. Replay *fails* such a request and counts
  it (`no body at capture`) instead of answering an empty 200.
- **Requests still in flight at the snapshot** are recorded as such. Almost all
  are ad iframes that never finish; an in-flight script or data request is
  reported as an oracle warning.
- **Not recorded:** requests made by out-of-process iframes, workers and
  service workers themselves (the capture watches the page's own session), and
  `Set-Cookie` headers Chromium withholds from the DevTools response.

Captures made before a field existed (`req_body`, `settle_rule`,
`settled_after_ms`, `body_rendered`) still load; replay falls back to capture
order for repeated POSTs and infers an unrendered body from an empty
`body_text` beside a non-empty `main_text`. Which settle rule produced such a
capture cannot be recovered from it.

## Replay (offline)

```sh
make corpus                            # all captured pages
CORPUS_ONLY=hydration make corpus      # a cell group, a cell, or page ids
CORPUS_JS_TIMEOUT=20s make corpus      # is a miss "too slow" or "cannot"?
CORPUS_DIR=/elsewhere make corpus      # a private capture set
CORPUS_VERBOSE=1 make corpus           # keep the fetch layer's warnings
CORPUS_REPEAT=5 make corpus            # replay each page 5x, score the worst, report what moved
CORPUS_OUT=/tmp/one.json CORPUS_ONLY=vue-guide make corpus   # don't overwrite last-run.json
```

### One page, everything

```sh
CORPUS_ONLY=hn-algolia CORPUS_DEBUG=1 CORPUS_DUMP=/tmp/dump make corpus
```

`never requested` lists the documents, scripts and fetch/XHR responses Chromium
loaded that unblink never asked for — the mirror of a lost request, and the lead
to follow when a page scores low with no error at all (its data-loading code did
not run, or ran too late). The count is in every run's JSON (`unrequested`); the
full list needs `CORPUS_DEBUG`.

`CORPUS_DEBUG=1` prints, per page: scores, oracle health and warnings, render
diagnostics, **every** lost request (kind, first/third party, how close the
archive came), every script and console error (uncapped), Chromium's own
exceptions, the stacks the capture shows and the verdict on each manifest
cell. `CORPUS_DUMP=<dir>` writes the texts being compared:

| file | contents |
|---|---|
| `<id>.oracle.txt` | what Chromium showed — the text recall is measured against |
| `<id>.full.md` / `.article.md` / `.static.md` | unblink's output: rendered full, rendered article, `render=false` |
| `<id>.missing.txt` | the oracle's 3-token shingles absent from `full.md`, in document order |

`missing.txt` is usually the fastest way to see *what* was lost.

Nothing touches the network. Each page runs through the production pipeline
with JavaScript on; a request the capture cannot answer fails and is counted.
The scorecard goes to stderr and the full per-page report to
`corpus/last-run.json`. With no captures the test skips.

## Reading the scorecard

One row per feature-matrix cell (a page counts in every cell it claims):

- **pass / a / b / c / d / ?** — bucket counts. `pass` is main-content recall
  >= 0.85. `a` engine divergence (a script error Chromium did not raise), `b` a
  declared non-goal API was hit, `c` the engine rejected syntax Chromium ran,
  `d` the comparison is unsound (unusable oracle, thin oracle, or a low score
  with *consequential* requests lost in replay), `?` below threshold with no
  diagnostic. A page with an unusable oracle is not scored at all — its row
  says `unscored`, never `recall=0.000`.
- **rate** — pass / (pages - d). `d` pages say nothing about the engine.
- **recall** — mean share of the oracle's main-content 3-token shingles found
  in the rendered `mode=full` Markdown.
- **static** — the same with `render=false`. `recall - static` is what running
  JavaScript bought; `static > recall` means rendering *lost* content.
- **article** — the same for the default `mode=article`. A gap to `recall` is
  reduction dropping content the page did deliver.
- **junk** — share of article-mode tokens the browser showed nowhere.
- **links** — the *median* of extracted links / links visible in Chromium, over
  the pages where Chromium saw at least one visible link. A page with no
  visible link has no ratio and is left out (`link_ratio_ok` in the JSON).
- **lossy** — passes reached although replay lost consequential requests.
- **jsgain** — scored pages where rendering raised recall by more than 0.05
  over `render=false`.

**Read `lossy` and `jsgain` before trusting a cell.** A server-rendered page
passes on its HTML alone. If the page's scripts could not be replayed and
rendering added nothing, that pass is evidence about extraction, not about the
JavaScript engine. A hydration or framework cell whose passes are all lossy and
whose `jsgain` is 0 has not measured the engine at all. The section "passes
with consequential replay losses" lists those pages with their gain.

The `replay:` line totals how requests were answered: exact, the two fallback
tiers, failed-at-capture-too, no-body-at-capture, and unmatched. It also counts
**non-http URLs sent to the HTTP transport** (`data:`, `blob:`): Chromium
resolves those without the network, so they are the engine's own requests,
never a replay miss.

The `lost requests:` lines break the unanswerable ones down:

- **consequential / harmless** — whether the lost response could change the
  rendered page. Analytics and ad hosts (a short, explicit list), images, fonts
  and media are harmless; scripts, documents, data fetches, stylesheets and
  preflights are consequential. Only a consequential loss excuses a low score
  into bucket `d`; a page that merely missed beacons is judged like any other.
  An unknown host is treated as consequential, so the list can at worst excuse
  a page — it can never blame the engine for a harness loss.
- **first-party / third-party** — by registrable domain.
- **by nearest archive entry** — `never`: Chromium requested no such host+path
  at all (the engine asked for something the browser did not); `query`: same
  path, a query string no fallback tier could reconcile (a harness limit);
  `method`: same path under another method; `nobody`: in the archive without a
  body.

Matching ignores scheme and host case and a default port, as the URL standard
does: Chromium records the lower-cased host, and a page that builds a URL with
an upper-case host must still find it.

Three further sections follow the per-page list:

- **results that moved between repeats** (under `CORPUS_REPEAT`) — the capture
  and the build are the same for every repeat, so a spread is not the page's
  or the harness's doing. The worst run is the one scored: a page that passes
  on some runs has not passed.
- **captures to redo or inspect** — unusable oracles and oracle warnings: the
  capture hit the hard cap before the network went quiet, a script or data
  request was still in flight at the snapshot, the main landmark holds under
  15% of the visible text (possibly the wrong element), the landmark was too
  thin and the whole page was scored instead, or navigation ended on another
  site.
- **manifest labels vs. captures** — see "Adding a page".

A lettered bucket is a lead, not a finding: it records that a diagnostic was
present on a page that scored low, not that it caused the miss. Confirm before
filing. To dig into one page, read its entry in `corpus/last-run.json`
(`js_errors`, `console_errors`, `replay.misses`, `render_budget_hit`) and
rerun it alone with `CORPUS_ONLY=<id>`.

Compare engine builds against the **same** captures. A recapture is a new
baseline: the pages themselves have changed.

## Comparing against another tool (`make corpus-compare`)

`make corpus` asks how close unblink gets to Chromium. `make corpus-compare`
asks the next question: on the same captured pages, against the same oracle,
how does unblink's output compare with another agent browser's, in fidelity and
in cost? Today the other tool is [Charlotte](comparison.md#charlotte), which
serves the same purpose from the other side: a real Chromium under a
token-efficiency layer.

```sh
make corpus-compare CHROME=$HOME/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome
make corpus-compare ARGS="-corpus-only shape:docs -corpus-runs 1"
make corpus-compare ARGS="-corpus-out /tmp/cc.json -corpus-md /tmp/cc.md -corpus-dump /tmp/cc-dump"
make corpus-compare TOOLS=unblink          # one tool, same pipeline
```

It needs the captures, Node ≥ 20 (npx fetches the pinned Charlotte, as for
`make crossbench`) and a Chromium. **Pass the build that made the captures**
(`CHROME=`; the run warns when it differs): the oracle is what that build
rendered, and a different build replaying it is a second variable in
Charlotte's score. `CORPUS_DIR`, when set, must be an absolute path.

The code is in `scripts/membench` (`corpus_*.go`, build tag `corpus`), which
already owns the tool adapters and the BPE token meter; the oracle, scorer,
replay matcher and exclusion rules are imported from `corpus/` (`compare.go` is
that package's only exported surface), so both tools are scored by the same
code. Nothing is copied.

### Same input, two routes to it

- **Charlotte** drives a real Chromium, which cannot take an in-process
  transport. `corpus.Proxy` is a forward proxy that answers every request —
  plain HTTP, or HTTPS inside a CONNECT tunnel it terminates with a throwaway
  in-memory CA — from the page's archive, through the same matcher `make corpus`
  uses (exact, then cache-bust, then path; POST bodies told apart by hash), and
  drops the connection for anything else. Charlotte has no option for browser
  launch arguments, so `PUPPETEER_EXECUTABLE_PATH` points at a generated
  wrapper that execs the real binary with `--proxy-server`,
  `--ignore-certificate-errors` and `--disable-quic` added; Charlotte itself
  runs unmodified at its pinned version. Certificate checks are off only because
  the browser has no route to anything but recorded bytes.
- **unblink** runs as a real MCP server (the production tool surface,
  untrusted-content framing on) inside the harness process, with the archive as
  its transport — the binary has no proxy support, and adding one for a
  benchmark would be a product change. So the asymmetry is: unblink is spared a
  loopback TLS hop and a process start, and its `read` latency is in-process.
  Latency is therefore the least comparable column; recall and tokens are not
  affected.

Neither tool can reach the network during a run. A request the archive cannot
answer is a network error for both, and is counted per tool per page. Chromium's
own housekeeping traffic (update checks, safe browsing) is refused without
being counted against the page.

### What is run

Every configuration gets a fresh server and a fresh browser profile per page
per run, one page at a time, at the tool's own defaults (unblink as `make
corpus` configures it: no retries, no robots/llms probes, default JavaScript
budget; Charlotte: default profile, default 30 s navigation timeout, its own
1440×900 viewport — the captures were made at 1280×720, and that is not tuned
away). Three runs by default; the median is reported and the run-to-run spread
shown.

| configuration | call |
|---|---|
| `unblink/default` | `read(url)` — article mode, one call at the default token budget |
| `unblink/full` | `read(url, mode=full)`, following `next_cursor` to the end |
| `charlotte/default` | `charlotte_navigate(url)` — detail `minimal`: landmarks, headings, counts |
| `charlotte/summary` | `charlotte_observe(detail=summary)` on the page the full run loaded (not timed) |
| `charlotte/full` | `charlotte_navigate(url, detail=full)` — all text content |

"Default" is what an agent gets from the tool's page-reading call with nothing
but the URL. The two defaults are not the same kind of call — Charlotte's is an
orientation pass that carries no body text by design, unblink's is a read — so
the default-vs-default row says which tool hands over content unasked, not
which extracts better. `unblink/default` against `charlotte/full` is the
"cheapest call that actually returns the page" comparison; full against full is
the fidelity comparison.

### How outputs are scored

- **Fidelity** uses `corpus`'s scorer unchanged: main-content 3-shingle recall
  and token recall against the oracle's main text, precision against the
  oracle's whole-page text. unblink's Markdown is scored as `make corpus`
  scores it. Charlotte answers with one JSON document; it is scored on its
  decoded string values in document order — `full_content`, headings, landmark
  and control labels, the content summary, the title — with identifiers (`id`,
  `type`, `role`, URLs, timestamps) and the `errors` diagnostics block left
  out. Scoring the raw JSON would split the text at every key and escape and
  understate Charlotte for a formatting reason. Precision is a multiset
  measure, so text Charlotte states twice (a heading in `headings` and again in
  `full_content`) costs it precision the second time.
- **Cost** is o200k_base tokens of the payload exactly as returned — for
  Charlotte the whole JSON, keys, ids and bounds included; for unblink every
  page of the read, framing included — because that is what the model's context
  pays. `recall per 1k tokens` is the mean over pages of recall ÷ (tokens/1000).
- **A run that errors or times out** gave the agent nothing: recall 0, 0 tokens,
  counted under `failed` and kept in the means.
- **Exclusions** are the harness's own. A page whose oracle is unusable is
  scored for neither tool. A tool's page is *not replay-sound* when its full
  configuration scored below 0.85 after losing a consequential request the
  archive could not answer; the head-to-head is reported both over every
  usable-oracle page and over the pages sound for both tools.
- **Win / tie / loss** uses a recall margin of 0.05, fixed in the code
  (`compareMargin`) before any run.

### Read the result with this in mind

The oracle is Chromium's `innerText`, and Charlotte *is* Chromium reading the
same DOM: on anything decided by layout or by JavaScript the engine cannot run,
Charlotte starts ahead by construction, and a tie there is the best unblink can
do. unblink starts ahead on tokens for the mirror-image reason: it reduces,
Charlotte serialises. The comparison measures how much of each structural
advantage survives real pages; it does not show that one design is right. The
measured table, with tool and Chromium versions, is in
[comparison.md](comparison.md#real-page-corpus-unblink-vs-charlotte).

## Adding a page

Append to `corpus/manifest.json`:

```json
{"id": "my-page", "url": "https://example.org/docs", "cells": ["framework:vue", "shape:docs"], "note": "why this page"}
```

`id` is a lowercase slug; cells are `group:name`. Prefer stable public
documentation, reference and demo pages; avoid anything behind a login, a
paywall or a bot challenge. Then `make corpus-capture ARGS="-only my-page"`.

The cell labels say which stack a site is believed to run. They are written by
hand and drift when a site is rebuilt, so every run checks each claimed cell
against the capture itself and prints the result under "manifest labels vs.
captures" (per page: `cell_checks`, `detected`, `unclaimed` in the JSON):

- `supported` — the capture shows it (the evidence is printed).
- `contradicted` — a structural signal says otherwise: no `<astro-island>` in
  the document, no `/_next/` anywhere, `document.characterSet` is UTF-8, no
  shadow root in Chromium's DOM, the served HTML already carries the content
  on a `client-only` page, or lacks it on a server-rendered one.
- `no-evidence` — a marker was looked for and not found, but it is one a real
  site can lack (minified away, or in a chunk Chromium never loaded).
- `unchecked` — no offline test exists (`shape:docs`, `arrival:client-routing`).
- `unclaimed` — a stack the page's own document or first-party scripts show
  that no claimed cell accounts for. A hint, not a verdict.

Nothing rewrites the manifest. Fix a `contradicted` label by hand, after
looking: markers are strings, and a documentation page *about* a framework can
contain them.

## What the replay cannot tell you

- **Charset transcoding.** Chromium returns text bodies already decoded, so
  the stored bytes are UTF-8 whatever the server declared. Replay rewrites the
  `Content-Type` charset to `utf-8` to match the bytes it serves (otherwise the
  fetch layer would transcode UTF-8 as Shift_JIS). The `shape:non-utf8` pages
  therefore check that such content survives the pipeline, not that unblink
  decodes Shift_JIS or KOI8-R. That is covered by `internal/fetch` tests.
- **Whether a result is stable.** One replay is one sample. In the first
  corpus two Docusaurus pages gave three different outcomes across runs of the
  same capture (16 requests and a pass; ~540 requests and a pass; ~550 requests
  and the whole page lost), stable within a run and different between runs.
  The cause was not established. Use `CORPUS_REPEAT` before reading a single
  run as a verdict, check its "results that moved" section, and never compare
  two builds on one run each.
- **Reception on the live web.** Replayed responses are the ones Chromium got.
  UA sniffing, TLS fingerprinting and rate limits are not exercised.
- **Apps that load more code than Chromium did.** Replay can only answer what
  Chromium fetched. In the first corpus this is nearly the whole of the replay
  loss: of 1,620 lost requests, 1,533 were scripts, 1,578 were first-party and
  1,618 were for a URL Chromium never requested at all — not a URL-matching
  problem, and not one a recapture fixes. Where checked (angular.dev), the
  lost chunks are targets of `() => import("./chunk-….js")` thunks inside
  scripts Chromium did load and never called. When such a request fails and
  the module graph cannot be built without it, the app never boots; a low
  score then lands in bucket `d` as "replay incomplete", and a server-rendered
  page still passes on its HTML (the `lossy` column). Until the engine stops
  requesting them — or replay is taught a sound answer for them — the corpus
  does not measure those apps' JavaScript. The lost URLs are in
  `replay.misses` (all of them under `CORPUS_DEBUG`).
- **Anything behind interaction.** One load, 1280x720, no scroll, no click.
- **Exact equality.** `innerText` and Markdown differ legitimately (alt text,
  `text-transform`, generated content); that is why the line is 0.85.
