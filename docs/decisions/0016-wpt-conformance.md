# ADR 0016: web-platform-tests as a conformance measurement tool

Date: 2026-07-04
Status: accepted

## Context

unblink deliberately implements a large slice of the web platform (DOM, events,
custom elements, composed Shadow DOM, Fetch/CORS, WebCrypto, encoding, URL,
Streams) so that untrusted page JavaScript hydrates real content into the tree it
extracts. Until now the correctness of that surface was reasoned about from the
code and from `docs/web-api-priorities.md`, never *measured*. We had no data-backed
answer to "are we actually right?"

The web-platform-tests suite (WPT, https://github.com/web-platform-tests/wpt) is the
industry's cross-browser conformance corpus — ~56k test files / ~1.8M subtests. Most
of it is irrelevant to unblink by construction: **reftests, visual tests, and
wdspec** compare *rendering* or drive a real browser over WebDriver, and unblink has
no layout engine, computes no pixels, and returns zero-rect geometry (ADR 0007).
Only **testharness.js** tests — pure JS/DOM unit tests whose results live in the DOM
— can run here, and even among those, only the directories exercising content-bearing
DOM/JS behavior are in scope.

The question this ADR settles: *how* do we measure WPT conformance in a way that is
honest (doesn't credit or blame us for things outside our design), repeatable
(offline, deterministic, reviewable), and actionable (produces a ranked to-do list)?

## Decision

Add an offline WPT conformance harness (`wpt/`, build tag `wpt`, `make wpt`) and
treat it as an **on-demand analysis tool, not a CI gate**. The existing `make eval`
0.9 floor remains the enforced quality gate; WPT conformance is a report we run when
we want to know where we stand.

Five load-bearing choices:

1. **Drive the JS engine directly, not the MCP server.** WPT scores *engine*
   conformance, and testharness.js writes results into a `<script>` node the `read`
   reducer would strip. So the harness sits at the `internal/js` `Render` seam
   (parse HTML → `Render` → walk the mutated tree for the results node), one layer
   below the `eval` harness. A custom `/resources/testharnessreport.js` uses WPT's
   `add_completion_callback` to serialize each subtest into
   `<script id="__wpt_results">`; an `Env.Wait` gate on that selector holds the
   render until results finalize (ADR 0004 guarantees the settle can't close early).

2. **In-scope is defined by content value, not spec breadth.** `wpt/scope.json`
   classifies each directory as in-scope / partial / stub-only / out-of-scope,
   seeded from `docs/web-api-priorities.md`. Out-of-scope dirs (css, canvas, webgl,
   workers, service-workers, webaudio, media/device, layout observers) are never
   vendored or walked.

3. **An honest denominator via four-bucket failure classification.** Every subtest
   is PASS or one of: **(a)** fixable in-scope gap, **(b)** out-of-scope-by-design
   (declared non-goal — asymmetric crypto, geometry/CSSOM, CSS-level shadow styling,
   legacy encodings, stub-only dirs), **(c)** goja engine ceiling (unsupported
   syntax / the WeakMap bug — a reviewed goja bump under ADR 0002, not a gap), or
   **(d)** harness limitation (needs iframes / workers / multi-origin / a wptserve
   `.py` handler the single-document, single-thread, single-origin runner cannot
   provide). **Reported conformance = pass / (pass + a).** (b)/(c)/(d) are excluded
   *and itemized* so exclusions are auditable, never hand-waved. A static
   source-scan pre-filter assigns (d) *before* running, so runner limits can neither
   inflate nor deflate the score.

4. **A pinned, vendored corpus.** `make wpt-sync` sparse-checks-out only the
   in-scope directories (plus `/resources` and `/common`) at the SHA recorded in
   `testdata/wpt/WPT_VERSION` and commits them under `testdata/wpt/`. This keeps
   `make wpt` offline and hermetic (like every other target) and makes a corpus
   refresh an explicit, reviewable, re-run-the-report change — the same posture as
   the `third_party/pdf` vendoring (ADR 0001) and the deliberate dependency pins
   (ADR 0002).

5. **On-demand, not a gate.** The pass-rate deliberately mixes in-scope gaps with
   by-design exclusions, and the corpus is large and settle-timeout-prone, so a
   conformance floor would be arbitrary and slow. `make wpt` prints a scorecard and
   writes `wpt/last-run.json`; it fails only on infrastructure errors. (A narrow,
   flake-free no-regression *lock* over specific passing subtests is a possible
   future follow-up, deliberately deferred.)

The ranked roadmap this harness feeds lives in `docs/wpt-compatibility.md`, ordered
by unblink's own metric — does closing the gap materialize more real page content in
the extracted Markdown? — so content-extraction fidelity (URL, HTML/DOM, Fetch)
ranks above security-posture parity (CSP/SRI/CORS/Fetch-Metadata).

## Consequences

- We can now state standards compatibility with evidence, and each priority in the
  roadmap cites concrete WPT (a)-failures rather than intuition.
- The `wpt/` package imports `internal/js` directly (like the `_test.go` render
  helpers); it stays out of `make test` via the build tag, exactly like `eval/`.
- The vendored corpus adds committed third-party test files under `testdata/wpt/`
  (kept lean to the in-scope slice); refreshing it is a manual, reviewed step.
- Reftests/visual/wdspec conformance, real layout/CSSOM/canvas/worker behavior, and
  consuming upstream `MANIFEST.json` remain explicit non-goals of this work.
