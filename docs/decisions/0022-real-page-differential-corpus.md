# ADR 0022: a real-page differential corpus, scored against Chromium

Date: 2026-11-27
Status: accepted

## Context

unblink's correctness evidence came in two shapes, and neither answers "does it
work on real pages?"

- **`make eval`** (the CI gate) runs 49 hand-written, single-purpose fixture
  pages. It proves each tool and each engine feature works in isolation. Its SPA
  cases load real framework bundles, but each drives a toy app that renders one
  `<article>`.
- **`make wpt`** (ADR 0016) measures engine conformance subtest by subtest. It
  says which DOM method is wrong, not whether a documentation site hydrates.

What real pages add is everything in between: a bundler runtime wrapped around
the framework, SSR markup that must hydrate without tearing, content that
arrives from a fetch after boot, a 20,000-node DOM, a consent script that throws
first. A feature that passes its unit test can still lose the article on a page
that combines six of them. Before this ADR the only real-page evidence was
anecdote ("it read poe.ninja") and one opt-in live read of example.com.

The question this ADR settles: how do we measure real-page behaviour in a way
that is **repeatable** (the web changes daily), **offline** (the suite must not
depend on, or hammer, third-party sites), **honest** (does not blame the engine
for a broken comparison), and **actionable** (says which *kind* of page fails)?

## Decision

Add a differential corpus: capture real pages once with headless Chromium,
replay them offline through the full unblink pipeline, and score unblink's
output against what Chromium rendered. Like WPT it is an **on-demand
measurement, not a CI gate**.

Seven load-bearing choices:

1. **Chromium is the oracle, and the oracle is text.** At capture time the tool
   saves `document.body.innerText`, the `innerText` of the page's main landmark
   (`main`/`[role=main]`, else the largest `article`), and visible link, form
   and control counts. `innerText` is CSS-aware, so it is exactly "what a user
   could read" — the thing unblink's Markdown is supposed to carry. Comparing
   text, not DOM, keeps the measurement on unblink's actual job (semantic
   reduction, ADR 0007) and off rendering details it deliberately lacks.

2. **Capture and replay are separate programs joined only by a file format.**
   `scripts/corpus` (its own module, like `scripts/membench`) needs Chromium and
   the network and is stdlib-only: it drives Chromium over
   `--remote-debugging-pipe` with untyped JSON, because a typed CDP binding lags
   the browser and silently drops events it cannot decode — fatal for a
   recorder. The replay side (`corpus/`, build tag `corpus`, in the main module)
   needs neither. They share a small versioned JSON schema, duplicated on
   purpose so the main module never gains a dependency on the capture tool.

3. **Replay runs the production pipeline with only the wire replaced.** Each
   page gets a fresh `browser.New` driven through `Read`/`Links`/`Forms`/
   `Controls`/`Console` — the entry points the MCP tools call — in one session,
   so every tool reads the same rendered page. The network is an archive-backed
   `http.RoundTripper` under both the page-fetch clients
   (`fetch.WithSharedTransport`) and the page-JS subrequest pool (the new
   `browser.WithJSRoundTripper`, test-only in intent and wired to no flag).
   CORS, CSP, SRI, the byte budget and the render budget all still apply. A
   request the archive cannot answer gets a transport error; nothing ever
   reaches the network.

4. **Non-determinism is tolerated in tiers and always reported.** A replayed
   request matches by exact method+URL; else by URL with cache-busting query
   parameters removed; else by path with the same parameter *names*. Each tier
   is counted per page, and an unmatched request is a first-class statistic,
   because a page whose replay was incomplete cannot be held against the engine.

5. **The score is shingle recall of the main content.** The headline number is
   the share of the oracle's distinct 3-token shingles found in unblink's
   rendered `mode=full` Markdown: order-sensitive only locally, so a moved block
   costs boundary shingles while a bag of the right words in the wrong order
   still fails. Three companions locate a loss: article-mode recall (did
   reduction drop it), `render=false` recall (what the JS engine contributed),
   and token precision against the whole visible page (junk). A page passes at
   recall >= 0.85.

6. **Misses are bucketed like WPT, and never guessed.** pass / **(a)** engine
   divergence with no known excuse (a script error Chromium did not raise) /
   **(b)** a declared non-goal API was hit / **(c)** the engine rejected syntax
   Chromium ran / **(d)** the comparison is unsound (bot challenge, HTTP error
   or near-empty oracle at capture, incomplete replay). A bucket is assigned
   only from a recorded diagnostic; a miss without one is **unclassified** and
   left for a human. (d) is excluded from every rate, itemized. A lettered
   bucket is *evidence for triage*, not a verdict: an uncaught error on a page
   that also scored low is correlated with the miss, not proven to cause it.

7. **Captures are not committed; the manifest is.** Captured pages are
   third-party content, tens of megabytes, and stale within months. Only
   `corpus/manifest.json` (URL, the feature-matrix cells the page is evidence
   for, a note) is tracked. `testdata/corpus/` is gitignored, as `testdata/wpt/`
   is. The scorecard is reported **per cell** — hydration style, bundler
   runtime, framework, content arrival, page shape, web components — so a result
   reads "SSR-hydrated Vue: 5/6", not one number.

## Consequences

- There is now a number for "works on real pages", broken down by page kind,
  and a worst-pages list that names the next thing to look at.
- **Results are not comparable across captures.** A recapture changes the pages.
  Compare two engine builds against the *same* captures; treat a recapture as a
  new baseline. There is no pinned corpus version, by construction.
- **Capture needs a browser and the network and is never run in CI.** With no
  captures present the replay test skips with instructions.
- **Known blind spots of the replay**, each documented in `docs/corpus.md`:
  - Chromium hands text bodies back already decoded, so replay serves them as
    UTF-8. Charset *transcoding* is not exercised (the fetch tests cover it);
    SRI over a non-UTF-8 script would be checked against re-encoded bytes.
  - Replay can only answer what Chromium fetched. An engine that requests more
    (a lazily imported chunk fetched up front) gets a failure the live web would
    not give it, and the page is bucketed (d). The first run put most
    client-only SPAs there: the corpus does not yet measure them.
  - The responses are the ones Chromium was served. A site that varies by
    User-Agent or TLS fingerprint is replayed as Chromium saw it, so the corpus
    measures the engine, not unblink's reception on the live web.
  - The oracle is one load at 1280x720 with no interaction: content behind a
    click, a scroll, or a consent dialog is absent from both sides.
  - `innerText` and Markdown differ legitimately at the margins (alt text,
    `text-transform`, generated content), which is why the pass line is 0.85 and
    not 1.0.
- `browser.WithJSRoundTripper` is a new public option on an internal package. It
  replaces the pool carrying the SSRF dial guard, so it must stay out of the
  flag surface.
- The manifest's stack attributions (which site runs which framework) come from
  each project's description of its own site and will drift. The scorecard
  reports the framework the engine detected so drift is visible.

## Alternatives considered

- **Live differential runs** (fetch each page with both engines every time).
  Simplest, but unrepeatable, slow, rude to the sites, and unable to tell an
  engine regression from the page changing.
- **Commit the captures.** Makes results reproducible across machines, at the
  cost of vendoring third-party content and ~100 MB of churn. Rejected for the
  default; nothing stops an operator keeping a private capture set and pointing
  `CORPUS_DIR` at it.
- **A recording HTTPS proxy instead of CDP.** Would preserve raw bytes
  (charsets, encodings) but needs TLS interception and cannot say what the
  browser *rendered*, which is the half of the capture that matters.
- **Compare DOM trees or screenshots.** Out of scope by ADR 0007: unblink
  computes no layout, and a DOM diff punishes every legitimate difference
  between a browser's tree and a reduced one.
- **Fold real pages into `make eval`.** The gate must be hermetic, small and
  stable; a corpus that cannot be committed cannot gate.
