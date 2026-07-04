# ADR 0007: The structured representation is semantic-only — computed from the node tree, never a layout

Date: 2026-07-03
Status: accepted

## Context

Agent-facing browser tools increasingly return a *typed decomposition* of a page
— landmarks/regions, an interactive-element inventory with ARIA state, a
structured heading tree, stable element handles — instead of only prose. A
comparison with charlotte (a competing MCP browser server) made the gap concrete:
charlotte drives real Chromium and pulls the browser's pre-computed accessibility
tree (`Accessibility.getFullAXTree`), so it gets roles, accessible names, ARIA
state *and* bounding boxes for free.

unblink is pure-Go with **no layout engine** — CSSOM/geometry are constant-stubbed
to zero and declared permanent non-goals (see CLAUDE.md, ADR 0005). So the question
was which of that decomposition unblink can and should offer. The answer: the
*semantic* half is fully computable from the parsed `*html.Node` tree with no
browser; the *spatial* half is not, and never will be.

Before this change unblink surfaced only a flat element-count summary, an
indented-**text** heading outline (dropping the heading ids it had already
extracted), controls carrying just `disabled`+`role`, and five head-metadata keys
(with `canonical` extracted but surfaced by no tool).

## Decision

Add a **semantic-only** structured representation, computed entirely in
`internal/dom` from the node tree, surfaced through `internal/browser` result
structs (no `internal/mcpserver` changes — the tools serialize the structs
verbatim). Five parts:

1. **Interactive ARIA state.** `page.Control`/`page.Field` gain
   `checked/expanded/pressed/selected` (string enums `"true"/"false"/"mixed"` —
   a `"false"` collapsed menu or un-pressed toggle is meaningful), plus
   `required/invalid/disabled` (bool), `value/placeholder/href`. Derived from
   `aria-*` attributes and native equivalents.
2. **Landmark/region map.** `page.Region` records the ARIA landmarks
   (banner/navigation/main/complementary/contentinfo/form/search/region) with a
   role, an accessible label, and a per-region interactive inventory (the
   "by-landmark" counts). `header`/`footer` are banner/contentinfo **only at the
   top level** (ARIA scoping); `form`/`section` are landmarks **only when named**.
3. **Structured outline.** `browse` now also returns `headings[]`
   (level/text/id), so an agent can rebuild the tree and deep-link via `#id`. The
   indented-text `outline` stays for back-compat.
4. **Stable element ids.** A cacheable, mutation-resilient handle per control /
   region / heading. Its scheme is its own decision — see ADR 0008.
5. **Unified head metadata.** A grouped `metadata` object
   (canonical/image/author/published/modified/favicon/theme-color/twitter) —
   which finally surfaces the long-extracted `canonical`.

**Accessible names** are a computable subset of the WAI-ARIA accname algorithm
(`internal/dom/aria.go`): `aria-labelledby` → `aria-label` → native value →
ancestor `<label>` → `alt`/`title`/`placeholder` → text. There is **no
visibility filtering** step (that needs layout) — a deliberate, documented
divergence from the full algorithm.

Everything is on-by-default and `omitempty`, computed once in `Extract` on the
shared page (so every tool reuses it) and bounded by output caps
(`maxRegionsOut`, `maxHeadingsOut`) so `browse` stays a cheap orientation call.
No workstream touches `internal/js`, so the provable-idle settle invariant
(ADR 0004) is unaffected.

## Still non-goals

Anything requiring layout or a real accessibility engine: element geometry /
bounding boxes, CSSOM, computed-style visibility filtering in name computation,
`::part`/style-scoped naming, and any spatial "find near x,y" query. These stay
constant-stubbed / absent. `itemref`-style cross-references and explicit
`<label for>` association in the accname path are also out (rare for the control
set we extract; ancestor-`<label>` is honored).

## Consequences

- `browse` gains `metadata`, `regions`, and `headings`; `controls`/`forms`/
  `interact` controls gain state fields and an `id` — all additive and
  `omitempty`, so existing eval scorers (typed field access) and the text
  `outline` assertion are unaffected.
- The region role table and ARIA scoping rules are standards-derived and live in
  `landmarkRole`/`sectioningScoped` (`internal/dom/extract.go`); this ADR is
  their rationale-of-record.
- See `internal/dom/aria.go`, `internal/dom/extract.go` (regions, control state,
  metadata), `internal/browser/browser.go` (`PageMetadata`, `summarize`), and
  ADR 0008 for the id scheme.
