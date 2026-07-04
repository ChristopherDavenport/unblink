# ADR 0008: Stable content-hash element ids are a reference key, not an interact target

Date: 2026-07-03
Status: accepted

## Context

The semantic structured representation (ADR 0007) gives each control, region, and
heading a short id (`btn-a3f1c2`, `rgn-…`, `h-…`). The point of such an id is to
be a **cacheable handle**: an agent that lists controls, reasons for a few turns,
then acts should be able to refer back to "the same button" even though the page's
JS has re-rendered in between.

unblink already resolves interact targets with a **CSS selector**
(`page.Control.Selector`, built by `selectorFor`). Selectors are legible and drive
the live goja runtime today, but they are positional: an `:nth-of-type` path breaks
when a dynamic list reorders. charlotte's answer is a hash of a semantic composite
key that "survives element reordering within the same container." We wanted that
resilience without giving up the working selector path.

Two design questions had to be settled: **how to compute the id** (so it is stable
yet unique enough), and **whether interact should accept it as a target**.

## Decision

**Scheme.** `id = <prefix>-<6 hex>` where the hash is FNV-1a-64 over a composite
key: `tag \0 role \0 lower(accessibleName) \0 structuralSig`.
- `structuralSig` is the ancestor **tag-name chain**, anchored at the nearest
  id-bearing or landmark ancestor, with **no `:nth` positional indices** — so
  sibling reordering and class churn do not change it.
- FNV-1a (stdlib `hash/fnv`, no new dependency) is deliberate over a crypto hash:
  this is a cache key, not a security token; fast and dependency-free wins.
  Determinism matters — no `rand`, no time — so re-extracting a cached page yields
  identical ids.
- Prefix is by kind (`btn`/`tab`/`sum`/`el` for controls, `sel`/`inp`/`chk`/`txt`
  for fields, `rgn` for regions, `h` for headings) — cosmetic legibility.

**Collision rule.** Identical semantic twins (e.g. ten identical "upvote"
buttons) hash the same. Within one Extract pass an `idMinter` counts emitted ids
and appends `-2`/`-3`… to the *displayed* string for repeats; the base hash is
unchanged. This guarantees per-snapshot uniqueness while keeping the base hash
reorder-stable. The tradeoff is explicit: twins are only distinguished by
occurrence order, so reordering the twins themselves shifts their suffixes — which
is acceptable because interchangeable controls are, by definition, interchangeable.

**Reference key only — not an interact target (v1).** The id is returned for
correlation/caching; `interact` still resolves the **selector**, which remains
authoritative. Reasoning grounded in the code: `Interact` resolves the selector
*inside the live goja runtime* via `query()`, which understands CSS only, and
controls are recomputed after every interaction. Accepting an id would require a
server-side `id → selector` map rebuilt on every snapshot and held in session
state — new stateful surface for marginal benefit while the selector already works.

## Future path (decided against for now)

If interact-by-id is later wanted, the clean seam is `handleInteract`: when the
target matches the `<prefix>-<hex>` shape, look it up in the current page's
`p.Meta.Controls` and substitute its `Selector` before dispatch — no change to the
JS runtime, no new persistent state beyond the already-cached page. Left unbuilt
until a concrete need appears.

## Consequences

- `page.Control`/`page.Field`/`page.Region` carry an `id`; headings use their
  authored DOM `id`, falling back to a hash id only when absent.
- The scheme lives in `internal/dom/aria.go` (`hashID`, `structuralSig`,
  `idMinter`). Any new structural primitive that wants a stable handle must route
  through `idMinter` so the collision rule holds pass-wide.
- Stability is contractual and cacheable — a change to the composite key or hash
  is a breaking change to that contract and must update this ADR.
