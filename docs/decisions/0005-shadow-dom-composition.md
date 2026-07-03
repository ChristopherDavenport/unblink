# ADR 0005: Shadow DOM is composed (flattened tree + cross-boundary events), not flattened-in-place

Date: 2026-07-03
Status: accepted

## Context

Phase 8 shipped a **flat, non-encapsulating Shadow DOM**: `attachShadow(host)`
returned a `ShadowRoot` object whose backing node *was the host element*, so every
shadow write landed directly in the host's light children. There was exactly one
`*html.Node` tree, and extraction, event bubbling, and interact-selector resolution
all worked "for free" because no real boundary existed. "True Shadow-DOM
encapsulation" was listed as a permanent non-goal.

That shortcut turned out to *lose real content and mis-model behavior*, not merely
skip encapsulation:

1. **Slotted content loss.** Setting `shadowRoot.innerHTML` clears the node's
   children first; because the shadow root aliased the host, a component that renders
   `shadowRoot.innerHTML = '<div><slot></slot></div>'` after light children exist
   **destroyed** those slotted children, and `<slot>` never projected them back
   (`assignedNodes`/`assignedElements` did not exist).
2. **Events across the boundary were unmodeled.** `composedPath()` did not exist,
   `composed` was written but never read (not even copied from `new Event` options),
   and there was no `target` retargeting — spec-incorrect and a blocker for real
   component libraries ("click outside", react-aria, Radix).
3. **Declarative Shadow DOM** (`<template shadowrootmode>`) was invisible on the
   static path (every extraction walk skips `<template>`) yet partially/incorrectly
   harvested into `Meta` by the cascadia extract selectors.

unblink's mission is semantic reduction: it must *see* content, never hide it. So the
value of "encapsulation" here is not privacy — it is producing the **correct composed
(flattened) tree** a browser would render, and modeling event flow correctly, while
still exposing everything to extraction.

## Decision

Give each shadow root its **own detached `*html.Node` backing subtree** (a
`DocumentNode`, tracked by `shadowRoots[host]` plus the reverse `shadowHostOf`), and:

- **Encapsulation for page JS, piercing for unblink.** Because the subtree is never
  linked into `b.doc`'s child chain, page-JS `document.querySelector` and
  document-scoped cascadia provably stop at the boundary; a shadow-internal
  `querySelector` still works. unblink's *own* tooling deliberately pierces: interact
  and `wait_for` resolve selectors through shadow subtrees (`queryPierce`), and the
  compose pass flattens shadow content for extraction.
- **Compose = the browser flattened tree.** One clone-producing algorithm
  (`internal/js/slots.go`) walks the light tree, replaces each shadow host with its
  shadow subtree, and resolves every `<slot>` to the host's assigned light children
  (by `slot` name, with fallback), recursing through nested hosts. It runs
  destructively into `doc` at the end of one-shot `Render` and clone-only at live
  `Snapshot` (so a live session keeps its separate subtrees across `Dispatch`).
- **Cross-boundary events (the coupled correctness half).** Detaching the subtree
  breaks `n.Parent`-chain bubbling, so `elementTargetPath` was rewritten to build the
  shadow-including **composed path** with per-hop `target` retargeting, `composedPath()`
  was added, and the `composed` flag is now plumbed and honored. `bubbles` and
  `composed` stay orthogonal: `composed` gates whether the path crosses the boundary;
  `bubbles` gates only the bubble phase. Anchors are retargeted in every phase (a
  non-bubbling composed event still fires ancestor *capture* listeners with the host
  as `target`).
- **Declarative Shadow DOM on the no-JS path.** `dom.ComposeDeclarativeShadow` promotes
  `<template shadowrootmode>` into composed light content so SSR web components render
  without `--js`. Under `--js` the imperative `attachShadow` path owns composition.
- **Closed mode.** `.shadowRoot` returns `null` for a closed root (browser-faithful
  feature detection), but its content is **still composed into extraction output** —
  the compose pass iterates `shadowRoots` regardless of mode. Mission > privacy.

The change is always-on under `--js` (no flag): it is strictly more correct, existing
tests are unchanged, and all compose/piercing work is gated on shadow presence, so
non-shadow pages pay nothing.

## Still non-goals

No CSS/layout engine, so: `:host`/`::slotted`/`::part` style scoping and any cascade;
slot reprojection (`<slot slot="…">`); manual slot assignment (`slot.assign()`,
`slotAssignment:"manual"`); `slotchange` timing fidelity; closed-mode privacy *from
extraction* and closed-mode `composedPath()` trimming (we return the full path); and
routing a slotted *light* node's events through its slot's shadow ancestors (such a
click still bubbles through the host's light tree and the document — just not through
the slot's shadow ancestors).

## Consequences

- The content-loss bug is fixed for free: shadow `innerHTML` now writes into the
  detached subtree, never the host's light children.
- Component code that read shadow content via `this.querySelector` (host scope) now
  correctly gets `null`; Lit et al. use `this.renderRoot`/`this.shadowRoot`, which
  work.
- Compose deep-clones shadow pages once at the extraction boundary — O(n), gated on
  shadow presence.
- See `docs/architecture.md` (Phase 8 / Phase 23) and `internal/js/slots.go`,
  `internal/js/events.go`, `internal/dom/shadow.go`.
