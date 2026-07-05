# WPT compatibility: where unblink stands and what to fix next

This is unblink's ranked roadmap for web-standards conformance, measured against the
[web-platform-tests](https://github.com/web-platform-tests/wpt) suite. It is
*generated from data*: the `make wpt` harness runs the in-scope WPT testharness.js
corpus directly against the JS engine and buckets every result (see
[ADR 0016](decisions/0016-wpt-conformance.md) for the how and why, and
`wpt/last-run.json` for the full machine-readable output).

The ranking metric is unblink's own (`docs/web-api-priorities.md`): **does closing
this gap materialize more real page content in the extracted Markdown?** So
content-extraction fidelity (URL, text decoding, DOM, HTML parsing) ranks above
security-posture parity, which ranks above everything a semantic-reduction tool
never needs.

## How to read the numbers

Every subtest is `PASS` or one of four excluded buckets; **only (a) counts against
us**:

| Bucket | Meaning | In the denominator? |
|---|---|---|
| **PASS** | Subtest passed | — |
| **(a) FIX** | Real in-scope gap — the to-do list | **Yes** |
| **(b) OOS** | Failure caused by a *declared non-goal* (no layout/CSSOM, legacy encodings, asymmetric crypto, CSS-level shadow styling, stub-only dirs) | No |
| **(c) CEIL** | goja engine ceiling — unsupported syntax; moves only with a reviewed goja bump ([ADR 0002](decisions/0002-dependency-pinning-policy.md)) | No |
| **(d) HARN** | Runner limitation — needs iframes / workers / WebDriver (`testdriver.js`) / multi-origin / a wptserve `.py` handler the single-document, single-thread, single-origin runner can't provide | No |
| **TMO** | Never produced results within the render budget (a hung async test or an engine-level busy loop) — excluded, but reported so a slow/hanging API is visible | No |

**Reported conformance = PASS / (PASS + FIX).** (b)/(c)/(d)/TMO are excluded *and*
itemized in `last-run.json` so the exclusions are auditable, not hand-waved. A
static source-scan pre-filter assigns (d) *before* running, so runner limits never
inflate or deflate the score.

## Baseline (wpt `b86e7702`, 16 directories, 3,215 test variants)

| Directory | Tests | Subtests | PASS | FIX (a) | OOS (b) | HARN (d) | TMO | Conformance |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| html/syntax | 376 | 2423 | 2287 | 136 | 0 | 92 | 182 | **0.944** |
| streams | 88 | 27 | 21 | 6 | 0 | 14 | 68 | 0.778† |
| FileAPI | 66 | 282 | 143 | 139 | 0 | 20 | 16 | 0.507 |
| domparsing | 56 | 85 | 43 | 42 | 0 | 7 | 21 | 0.506 |
| encoding | 196 | 978 | 128 | 126 | 862 | 20 | 8 | 0.504 |
| dom | 626 | 848 | 411 | 431 | 0 | 176 | 224 | 0.488 |
| console | 12 | 11 | 5 | 6 | 0 | 6 | 3 | 0.455 |
| fetch/api | 186 | 488 | 202 | 286 | 0 | 83 | 52 | 0.414† |
| url | 49 | 27 | 11 | 16 | 0 | 4 | 41 | 0.407† |
| hr-time | 13 | 6 | 2 | 3 | 0 | 5 | 3 | 0.400 |
| WebCryptoAPI | 157 | 4853 | 25 | 53 | 4775 | 59 | 75 | 0.321 |
| shadow-dom | 278 | 101 | 28 | 70 | 2 | 106 | 128 | 0.286† |
| html/webappapis | 331 | 196 | 49 | 146 | 0 | 132 | 56 | 0.251† |
| xhr | 348 | 285 | 37 | 247 | 0 | 113 | 136 | 0.130† |
| html/dom | 255 | 60149 | 5745 | 54404 | 0 | 17 | 47 | 0.096‡ |
| custom-elements | 178 | 578 | 44 | 529 | 5 | 25 | 71 | 0.077‡ |

† **timeout-dominated** — the directory's flagship tests hang (see below), so the
conformance ratio is computed over only the handful that complete; read the TMO
column, not the ratio. ‡ **reflection-dominated** — the FIX count is almost
entirely exhaustive IDL attribute/ARIA reflection (see the low-priority note).

**The `TOTAL` (0.139) is not a meaningful headline** — it is swamped by html/dom's
54,404 IDL-reflection assertions. Across the **content-extraction core** (dom,
encoding, domparsing, FileAPI, html/syntax), conformance is **≈0.78**, and HTML
tree-construction alone is **0.944**. The real story is in the per-directory rows.

The four security-posture directories (CSP, SRI, fetch/metadata, referrer-policy)
were vendored and run **separately** — see *Secondary* below; they are largely not
measurable by a client-side harness.

## Ranked priorities (content-extraction first)

**The full list at a glance:**

| # | Item | Where | Signal |
|---|---|---|---|
| **P1** | Replace the regex URL parser with a WHATWG one | `internal/js/globals.go` | url suite **times out entirely**; most content-critical API |
| **P2** | TextDecoder/TextEncoder correctness (fatal, options, labels, surrogates) | `internal/js/prelude_api.go` | encoding **0.504**, 4 concrete gaps |
| **P3** | DOM fidelity (CharacterData, DOMTokenList, passive listeners, dispatchEvent) | `internal/js/{domapi,events,bridge}.go` | dom **0.488** |
| **P4** | HTML tree-construction — **maintain only** | `x/net/html` | html/syntax **0.944**, already strong |
| **P5** | Custom-element / Shadow-DOM lifecycle — triage (ElementInternals) | `internal/js/customelements.go` | reflection/iframe-dominated; smaller real slice |
| **secondary** | Security-posture edges — **closed** (audit-driven, see below) | `internal/js`, `internal/fetch` | WPT can't measure these; a targeted audit found 6 gaps, all now fixed/locked |
| *not ranked* | IDL reflection breadth, streams backpressure, legacy encodings, asymmetric crypto | — | by-design or low content-value (excluded) |

Detail for each below.

### P1 — Replace the URL parser with a WHATWG-conformant one (`url/`)

**Evidence:** every flagship WHATWG URL test **times out** — `a-element.html`,
`a-element-origin.html`, `url-constructor.any.js`, `url-setters`, `IdnaTestV2.any.js`,
`toascii.window.js`, `javascript-urls.window.js` (41 of url/'s outcomes are TMO,
leaving only 27 subtests that complete at all). unblink's `URL` is a *pragmatic
regex parser* (`internal/js/globals.go`), and it is too slow — or catastrophically
backtracks — to get through the standard URL corpus within the render budget.

**Why it's #1:** URL correctness is the most content-bearing API there is — it
governs link resolution, `<base>` handling, and every fetch target, and agents act
on the links unblink extracts. A conformant parser both fixes the failures and
removes a real slow-path (a URL input that hangs the parser is a latent DoS on any
page that constructs it). The fix is self-contained: swap the regex `URL`/`URLSearchParams`
for a WHATWG state-machine implementation.

### P2 — TextDecoder / TextEncoder correctness (`encoding/`)

**Evidence:** `encoding/` is **0.504** (128 pass / 126 fix), and the fixable
failures are four concrete, self-contained gaps:

- **No `fatal: true` mode** (`textdecoder-fatal.any.js`, 35 subtests). Must *throw*
  `TypeError` on malformed input; unblink always substitutes U+FFFD.
- **Constructor options not reflected** (`decode-attributes.any.js`, 14).
  `decoder.fatal` / `decoder.ignoreBOM` read back `false`, and `ignoreBOM` has no
  decoding effect.
- **Encoding-label normalization** (`unsupported-encodings.any.js`,
  `textdecoder-mistakes.any.js`). UTF-8 aliases must normalize to `"utf-8"`; unknown
  labels must throw `RangeError`. unblink accepts them loosely.
- **Lone-surrogate replacement in `TextEncoder`** (`encode-utf8.any.js`,
  `api-surrogates-utf8.any.js`). Encoding a lone surrogate must emit `EF BF BD`;
  unblink emits WTF-8 (`ED …`).

**Why #2:** encoding correctness is content-bearing (bytes that decode wrong extract
wrong), and each gap is a small, local, well-specified fix in the existing
UTF-8-first codec (`internal/js/prelude_api.go`) — high value per unit effort.

### P3 — DOM Standard fidelity (`dom/`)

**Evidence:** `dom/` is **0.488** (411 pass / 431 fix) — solid, trustworthy signal
in the most content-central surface. The fixable failures cluster on real, local
gaps, not layout:

- **`CharacterData` methods** — `replaceData` (34), `substringData` (26) (and
  `insertData`/`deleteData`): off-by-spec text-node editing.
- **`DOMTokenList` coverage** (`DOMTokenList-coverage-for-attributes.html`, 54) —
  `classList`-style reflected token lists (`relList`, `sandbox`, …) missing.
- **Passive event-listener defaults** (`passive-by-default.html`, 32) — `wheel`/
  `touchstart` should default to `passive`.
- **`dispatchEvent` edge cases** (`EventTarget-dispatchEvent.html`, 23) and
  `DOMImplementation.createHTMLDocument` (13).

**Why #3:** the DOM tree *is* the extraction source; each of these is a bounded fix
in `internal/js` (`domapi.go`, `events.go`, `bridge.go`).

### P4 — HTML tree-construction: already strong, maintain only (`html/syntax`)

**Evidence:** `html/syntax` is **0.944** (2287 pass / 136 fix) — the html5lib
tree-construction suite, the foundation everything else reads. **This refutes the
prior assumption that HTML parsing was a large, expensive gap:** `golang.org/x/net/html`
is a genuinely good HTML5 parser, and the tree is faithful. The remaining 136 fixes
are edge cases (some foreign-content, template, and adoption-agency corners).

**Action: none urgent** — do not rewrite the parser. Track the 136 edge cases; adopt
upstream `x/net/html` fixes as they land. This is the biggest data-driven *demotion*
from the original plan.

### P5 — Custom-element & Shadow-DOM lifecycle (triage, medium value)

**Evidence & caveat:** `custom-elements` (0.077) and `shadow-dom` (0.286) score low,
but the low scores are **not** the CE upgrade/reaction machinery (which renders real
React/Lit/web-component SPAs — see `internal/js/frameworks_test.go`). They are
dominated by **ARIA/`ElementInternals` IDL reflection** (`AriaMixin-string-attributes.html`
80, `ElementInternals-accessibility.html` 49) and **cross-document iframe tests**
(correctly excluded as (d)). The genuinely fixable, content-relevant slice is
smaller and needs per-test triage; `ElementInternals` form participation is the most
promising item. Medium value (component-heavy sites).

### Secondary — security-posture parity (measured: largely *not* client-side-measurable)

unblink deliberately implements CORS, SRI, CSP, Fetch-Metadata, and SOP over
untrusted page JS ([ADRs 0011–0015](decisions/)). Their WPT suites were run — and the
headline result is that **they are mostly unmeasurable by a single-process,
single-origin, client-side harness**, which is itself the finding:

| Directory | Tests | Completed subtests | Outcome |
|---|--:|--:|---|
| referrer-policy | 1390 | 0 | 100% (d) — asserts on the `Referer` a *server* received |
| fetch/metadata | 121 | 0 | 100% (d) — asserts on the `Sec-Fetch-*` a *server* received |
| content-security-policy | 865 | 115 | 392 TMO, 313 OOS (unenforced directives), 114 (d); conf **0.147** over the few that complete |
| subresource-integrity | 30 | 11 | 15 TMO, 12 (d); the completers are the *tentative* signature-SRI feature |

Why: `referrer-policy` and `fetch/metadata` assert on **what a server received** via
wptserve `.py` echo handlers — unblink *sends* spec-correct Referrer and Sec-Fetch
headers (ADR 0013), but a client-side runner has no server to observe receipt.
`content-security-policy` and `subresource-integrity` fare a little better but are
dominated by TMO/(d): restrictive test policies block the harness's own scripts (no
completion), and many need iframes or a second origin. unblink enforces only the
directives it claims (script-src/connect-src/default-src + nonce/hash/strict-dynamic/
unsafe-eval; the resource-type + document directives are counted OOS per ADR 0014).

### Confirming the posture without WPT (the audit + gap-closure)

Because WPT can't measure these, the actual confidence lives in unblink's *own*
targeted tests (unit + integration + fuzz), which control both sides and assert
unblink's real contract ("gate the page, not the operator" — a blocked read, but the
request still sent and logged). A dedicated audit of that suite against every ADR
0011–0015 enforcement claim found the single-hop core well-confirmed (CORS is nearly
airtight; SRI raw-bytes hashing is locked; CSP parsing/matching is broad and fuzzed)
but surfaced **six specific gaps** — exactly the multi-origin / redirect / capability
edges that structurally need a real browser + wptserve, which is why neither WPT nor
the prior unit tests reached them. **All six are now closed:**

| Gap | Resolution |
|---|---|
| **CSP nonce hiding** (page JS could scrape a nonce from the DOM and reuse it) | **Code fix** — `hideNonces` (`internal/js/csp.go`) captures nonces before page JS runs and blanks the content attribute; enforcement reads the captured value. `getAttribute('nonce')`→`''`, `[nonce="…"]` selectors match nothing. |
| **Same-origin fetch redirected cross-origin read as `basic`** (a redirect laundering a cross-origin read past SOP) | **Code fix** — `doFetch` now re-validates CORS on the final response when a same-origin request's `FinalURL` is cross-origin (`internal/js/cors.go`). |
| SharedArrayBuffer under `crossOriginIsolated===true` | test lock — stays `undefined` (ADR 0013 fidelity-not-gate) |
| Cross-origin SRI ↔ CORS interaction | test lock — integrity holds cross-origin; SRI deliberately not CORS-gated (no read to protect) |
| Public-suffix cookie jar | test lock — `Domain=.com` supercookie rejected (`internal/fetch/jar_test.go`) |
| SOP DOM-isolation stubs | test lock — `opener`/`parent`/`top`/`frames`/iframe `contentWindow`/`window.name` all inert |

**Conclusion for the priority list:** security-posture conformance **cannot be ranked
from WPT** — the suites need a real multi-origin server + browser — so it is confirmed
by the targeted suite instead, which is now gap-free for the known edges. It sits
**below every content-extraction item** (it materializes no page content), and it has
**no open to-dos**: the two real bugs the audit found are fixed and the rest are locked
by tests.

## Deliberately out of scope (never in the denominator)

Chasing these would contradict the product — unblink does *semantic reduction, not
rendering*, with no layout engine ([ADR 0007](decisions/0007-semantic-structured-representation.md)):

- **Exhaustive IDL reflection breadth** — html/dom's 54k `reflection-*` assertions
  and custom-elements' `AriaMixin` reflection are real gaps but *low content-value*:
  extraction reads a handful of attributes (`href`, `src`, `alt`, control state), not
  every element's full typed IDL surface. Not prioritized.
- **Rendering conformance** — reftests, visual tests, wdspec; `css/`, `2dcontext/`,
  `webgl/`; `getComputedStyle`/geometry/CSSOM (constant stubs).
- **Threads & persistence** — Workers, Service Workers, real WebSocket, persistent
  IndexedDB, SharedArrayBuffer (one goja runtime per render, by construction).
- **Asymmetric WebCrypto** — RSA/ECDSA/ECDH and wrap/unwrap reject by design
  ([ADR 0006](decisions/0006-crypto-subtle.md)); this is the whole of WebCryptoAPI's
  4,775 OOS.
- **Legacy text encodings** — TextDecoder is UTF-8-first (encoding's 862 OOS).
- **Streams backpressure & shadow-DOM style scoping** — documented shim limitations
  ([ADR 0005](decisions/0005-shadow-dom-composition.md)).

## Reproducing this report

```sh
make wpt          # offline: run the pinned corpus, print the scorecard, write wpt/last-run.json
make wpt-sync     # refresh testdata/wpt/ to the SHA in testdata/wpt/WPT_VERSION (needs network)
```

`make wpt` is an on-demand analysis tool, not a CI gate — the `make eval` 0.9 floor
remains the enforced quality gate. A full run takes ~30 min (it is dominated by the
~1,100 tests that hang on unsupported async capabilities and pay the render budget).
The corpus is a pinned, vendored subset under `testdata/wpt/` (`WPT_VERSION` records
the upstream SHA); `wpt/scope.json` is the authoritative in-scope classification.
Runs are deterministic — the same corpus yields identical bucket counts.
