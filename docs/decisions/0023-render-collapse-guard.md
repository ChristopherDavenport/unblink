# ADR 0023: Render-collapse guard

Date: 2026-11-27
Status: accepted

## Context

unblink renders a page's JavaScript by default, and everything downstream
trusts the result: the scripts mutate the canonical tree in place, and reduce,
emit and every structure tool read that tree without knowing whether JS ran.
That is the right design when rendering adds to a page. It has no answer for a
render that takes the page away.

The real-page corpus (ADR 0022) found the case. A Docusaurus documentation page
arrives fully server-rendered. Hydration threw (a table-of-contents helper
walked off the top of a zero-geometry tree), React's error boundary replaced
the document's content with "This page crashed", and `read` returned those 143
tokens: recall 0.001 against 0.995 for the same page with `render=false`. A
second page, a Next.js course index, did the same thing later in the same
investigation for an unrelated reason ("This page couldn't load", 13 tokens,
against 0.68 static). The specific engine gaps get fixed as they are found. The
class does not go away: any hydration failure, and any client app that boots
into an error, consent or sign-in shell, turns a page the server delivered into
one line of apology — and the result said nothing was wrong beyond a
`js_errors` entry.

Nothing compared the document before and after its scripts ran.

## Decision

A guard at the one place a one-shot render happens,
`Browser.processFetched` (`internal/browser/collapse.go`). It is on by default
and has no flag.

**The measure.** Before the scripts run and again after, the document is sized
with `dom.MeasureDocument(...).Region`: bytes of text (whitespace dropped) in
the page's content region — the main landmark when there is exactly one,
otherwise the body — outside navigation, complementary, banner, contentinfo and
search landmarks, and outside `script`/`style`/`head`/`noscript`/`template`.
Two choices in that definition carry the design:

- *Chrome is left out.* A framework's error boundary usually sits inside the
  layout, so the header, navigation and footer survive the crash. Measured over
  the whole body, a page that lost all of its content but kept 800 bytes of
  navigation looks 20% intact.
- *Hidden text is counted.* Scripts hide content all the time — a collapsed
  accordion, an inactive tab, a "read more" fold — and that text is still in
  the document. Only text that is *gone* counts as lost. (What a reader cannot
  see is still dropped from the output afterwards by safe-output stripping;
  that is a different question from whether the render destroyed the page.)

**The rule.** The render collapsed the page when both hold:

1. `before >= 1000` bytes (`collapseMinBefore`, roughly 250 tokens). The server
   sent a real page. A client-rendered app's shell holds next to
   nothing, so the guard never applies where only JavaScript can produce the
   content — there would be nothing to fall back to.
2. `after < 0.10 * before` (`collapseKeep`). The render kept under a tenth of
   it.

**What happens then.** The rendered DOM is discarded: `p.Doc` is replaced by a
fresh parse of the server's bytes (`p.Raw`, with declarative Shadow DOM
flattened exactly as the no-JS path does), so `dom.Extract`, reduce, emit and
every later tool see the server-rendered page. `p.Rendered` goes back to
`false`, because nothing downstream is the rendered DOM any more — `raw_html`
then returns the server's bytes, as it does for any unrendered page.
`RenderDiag` is kept and gains `Discarded`, `TextBefore` and `TextAfter`: the
render did run, and `js_errors`, `framework` and the `requests`/`console` logs
still describe it. `read` and `browse` report `render_discarded: true`, and
`read`'s trusted trailer says in a sentence what happened and that
JavaScript-only content is absent.

**A met wait gate wins.** A read with `wait_for`/`wait_text` whose gate was met
is never discarded: the caller named the content it wanted and the rendered DOM
has it. A gate that was *not* met gets no such exemption.

**Sessions and live runtimes.** A session's navigation goes through the same
one-shot render, so its stored page is the server's HTML after a discard and
session reads say `render_discarded`. `interact` is different. It runs against
a persistent runtime opened from the page's bytes, and the only DOM whose
listeners and state exist is that runtime's: the guard does not act there,
because swapping the tree under `interact` would have it dispatch events at
nodes the page's code has never seen. Instead:

- the first `interact` on a page whose render was discarded reports
  `render_was_discarded: true` — the page it acted on is not the one earlier
  reads returned;
- from that snapshot on, the session's current page *is* the live DOM, as after
  any `interact`, and `render_discarded` is cleared because it no longer
  describes the page (`liveRenderDiag`).

So `read` and `interact` never silently disagree: either both describe the
server's HTML, or the switch to the live DOM is announced on the call that
makes it.

**Cost.** Two read-only, allocation-free walks of the document per render
(`dom.MeasureDocument`; three or four when the page has a main landmark, the
extra ones over that subtree only). The re-parse happens only on the collapse
path. Measured numbers are in "Evidence" below.

## Alternatives considered

- **Always trust the render** (the status quo). Simple, and right for the
  great majority of pages. It makes the engine's every hydration bug, and every
  site's every client-side failure, a total loss of a page the server had
  already delivered — and it reports that loss only as an error string the
  agent has to know to distrust.
- **Always prefer the longer of the two documents.** No threshold to argue
  about, and it would have caught both corpus cases. It is wrong in the other
  direction, routinely: a client-side route change, a filter, pagination, a
  consent banner dismissed by script, and a "loading…" skeleton replaced by a
  short real answer all make a page legitimately shorter. The rendered DOM is
  the page; a guard may only overrule it on evidence that it is *not* a page.
  An order-of-magnitude loss of a substantial document is that evidence; "a bit
  shorter" is not.
- **Return both.** Hand the agent the static and the rendered text and let it
  choose. That doubles the token cost of exactly the reads that went wrong, and
  moves a judgement the server can make from numbers onto every caller, who
  cannot see the numbers. The caller can still get the other view on request:
  `render=false` for the server's HTML at any time, and a `wait_for` gate to
  insist on the render.
- **Detect error boundaries by their text or markup.** "This page crashed",
  `data-nextjs-error`, and so on. Every framework and every site words it
  differently, and a consent or sign-in shell has no error marker at all.
- **Diagnostic only** — report the collapse, keep the rendered DOM. This was
  the fallback had the rule proved unsound on real pages. It did not (see
  Evidence), and a diagnostic alone still hands the agent 13 tokens of apology
  when the server's whole page was available.

## Why these thresholds

*1000 bytes before.* The floor separates "the server sent a page" from "the
server sent a shell or a placeholder". The five client-rendered pages in the
corpus hold 0 bytes of content text before their scripts run. A "loading…" or
"please enable JavaScript" placeholder is a sentence or two. The floor sits
well above both, at the size below which the article rule in `internal/reduce`
also declines to act (`partialMinDropped`, the same 1000 bytes): under about
250 tokens there is too little to lose, and the guard cannot tell a crash from
a client app replacing a short placeholder with a short answer. The corpus
does not discriminate between 1000 and, say, 2000 — no page in that band
changed size at all — so the number rests on that argument, not on a fit.

*A tenth after.* The legitimate shortenings we can name keep far more than a
tenth of a substantial page, or shorten a page that was a placeholder to begin
with: a route change renders another view of comparable size; filtering or
client-side pagination keeps a slice of a list; a skeleton or "enable
JavaScript" notice is short already. Error and consent shells are a heading and
a sentence: the corpus collapse kept 1.7%, and the Docusaurus one 143 tokens of
a page with 9 KB of content text. Every other guarded render in the corpus kept at least 93%.
Between the two populations there is a gap of an order of magnitude on each
side of the threshold.

The rule deliberately has no absolute cap on `after`. A server-rendered
5,000-row table that hydrates into a virtualised list showing nothing (unblink
has no layout, so a virtualised list has no viewport to fill) is a collapse by
this rule, and returning the server's table is the better answer.

## Evidence

Measured on the real-page corpus (74 captured pages, one frozen build, guard
off against guard on; ADR 0022), recording `before`/`after` for every render:

| | renders |
|---|---|
| total | 74 |
| `before` = 0 (client-rendered, or blocked at capture) | 5 |
| 0 < `before` < 1000 — below the floor, unguarded | 7 |
| guarded (`before` >= 1000) | 62 |
| … kept >= 93% of their content text | 61 |
| … kept < 10%: discarded | 1 |

The lowest ratios among guarded renders were 0.017, 0.936, 0.998 and 1.000.
The one discard:

| page | `before` | `after` | recall, `render=false` | recall, rendered, guard off | recall, guard on |
|---|---|---|---|---|---|
| `nextjs-learn` | 3495 | 58 | 0.678 | 0.000 | 0.678 |

Its rendered output was 13 tokens ("This page couldn't load — Reload to try
again, or go back"). No other page's score moved in either direction with the
guard on, so on this corpus the rule made one decision and it was right; it
made no false trips. Mean full-mode recall over the 72 scored pages went from
0.893 to 0.902.

The Docusaurus crash that motivated the guard did not reproduce on the build
the guard was measured on (its three Docusaurus pages rendered with their
content text unchanged), so the guard was not exercised against it. By the
numbers it is covered — that page holds 9,149 bytes of content text before its
scripts run, and the crashed render left 143 tokens in total — but that is
arithmetic, not a run. The tests (`internal/browser/collapse_test.go`, the
`render-collapse-guard` eval case) use a hand-written page that does what that
one did: the layout survives and the content is replaced.

That is one collapse, and a sample of one says little about how collapses
vary. What the corpus does establish is the other side: across 61 ordinary
renders of server-rendered pages — documentation, news and framework sites —
running the page's JavaScript never removed more than 7% of its content text.
Legitimate renders, as far as this corpus shows them, do not come near the
threshold. The corpus loads each page once and never clicks, so it has no
client-side route changes; those are covered only by the tests.

Cost, `BenchmarkReadRenderedCold` (fetch, parse, render, extract, reduce and
emit of a 9 KB article over loopback, a unique URL per iteration; guard off
against guard on, 10 runs each):

```
                    │  guard off  │             guard on              │
                    │   sec/op    │   sec/op     vs base              │
ReadRenderedCold-32   4.982m ± 8%   5.136m ± 9%  ~ (p=0.796 n=10)
                    │    B/op     │    B/op      vs base              │
ReadRenderedCold-32   1.975Mi ± 0%  1.976Mi ± 0%  ~ (p=0.436 n=10)
                    │  allocs/op  │  allocs/op   vs base              │
ReadRenderedCold-32   20.97k ± 0%   20.97k ± 0%  ~ (p=0.099 n=10)
```

No measurable difference. The walk itself (`BenchmarkMeasureDocument`, a
1,500-element document) is about 21 µs and allocates nothing, against about
790 µs to parse the same document; the guard runs it twice per render.

## Consequences

- A discarded render costs the agent whatever only JavaScript would have added
  to that page. The result says so, and `wait_for` overrides it.
- The guard can be fooled in both directions by construction: a page whose
  scripts legitimately replace 1,000+ bytes of server-rendered content with
  under a tenth as much (a long placeholder essay swapped for a one-line
  answer) is discarded wrongly; a crash that leaves more than a tenth of the
  content (a partial hydration failure) is kept. The first is reported and
  overridable. The second is the status quo.
- `Page.Rendered` now means "the tree is the rendered DOM", not "scripts ran".
  Whether scripts ran is `RenderDiag != nil`.
- `browser.WithRenderCollapseGuard(false)` turns the guard off for embedders
  and benchmarks. There is no CLI flag; add one if an operator needs it.
- The measure lives in `internal/dom` (`MeasureDocument`, sharing its walk with
  the `MeasureContent` used by article reduction) and stays out of
  `internal/js`: the guard needs nothing from the engine.
