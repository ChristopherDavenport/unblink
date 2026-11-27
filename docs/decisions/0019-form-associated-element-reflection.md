# ADR 0019: Form-associated custom element values reach extraction by tree reflection

Date: 2026-11-27
Status: accepted

## Context

A form-associated custom element (`static formAssociated = true` plus
`attachInternals()`) keeps its submission value in the JS heap:
`ElementInternals.setFormValue` stores it, and nothing in the markup says what a
design-system `<my-input name="email">` would submit. With `ElementInternals`
implemented, page JS sees the value (`new FormData(form)`, `form.submit()`), but
unblink's own `forms` / `submit_form` tools read the `*html.Node` tree. That
tree is the single source of truth (see `docs/architecture.md`), and nothing
below the engine may consult the JS heap, so without a bridge an agent would
see a form with its custom controls missing.

## Decision

The engine reflects each form-associated custom element's state onto the tree
handed to extraction, as two attributes whose names live in `internal/page`:

- `data-unblink-form-value`: what the element currently contributes to its
  form's entry list, as an `application/x-www-form-urlencoded` string in entry
  order. Empty when it submits nothing.
- `data-unblink-form-invalid`: present when the element fails constraint
  validation (`setValidity`).

`dom.extractForms` turns a marked non-native element into `page.Field`s (type =
tag name, one field per entry). Submission needed no change.

The attributes exist only on the extraction-bound tree. A one-shot render
stamps them after page JS has finished; a live session stamps them on the
snapshot and removes them again, so page JS never observes them.

Rejected: synthesising hidden `<input>` elements for the values. That loses
the control's type, `required` and invalid state, and makes a custom control
indistinguishable from a real hidden field.

## Consequences

- `forms`, `interact` followed by `forms`, and `submit_form` see custom
  controls; `internal/browser/formassociated_test.go` covers all three.
- The marker is visible in `raw_html` output of a rendered page.
- `File` values are not reflected.
- A page can write the marker attributes into its own static HTML and so
  present fields of its choosing. That grants nothing new: a page already
  controls every field of its own forms.
- `formStateRestoreCallback` is never called (there is no session restore),
  `form="id"` association and the `:state()` selector are not implemented, and
  native controls still have no constraint validation.

## Amendment 1 (2026-11-27): default ARIA semantics, `:state()`, `form="id"`

The same reflection now carries the default ARIA semantics a custom element
sets through `ElementInternals`. The engine stamps, on the extraction-bound
tree only, `data-unblink-default-role`,
`data-unblink-default-aria-{label,checked,expanded,pressed,selected,required,invalid,disabled}`
and `data-unblink-default-labelledby-text` (the resolved text, since
`ariaLabelledByElements` holds element references rather than ids). The engine
stamps every default it has; `internal/dom` applies the ARIA-in-HTML rule that
an explicit `role` / `aria-*` attribute wins (`lookupARIA`). A custom element
whose default role is interactive (button, tab, ...) therefore appears as a
control, and a default landmark role creates a region.

`:state()` is matched without exposing state to page JS: a page-JS selector
containing `:state(` is rewritten to an attribute selector on a
per-process-random attribute name, and elements with states carry that
attribute only for the duration of the one synchronous match.

`form="id"` association is honoured by one owner rule on both sides: the
engine (`formOwner` in `internal/js/forms.go`, for native and custom controls)
and `dom.extractForms`. A descendant control whose `form` attribute names
another form, or no existing form, no longer belongs to its ancestor form.

Still open: `adoptedCallback`. Every node's `ownerDocument` is the main
document, including nodes inside `createHTMLDocument()` documents and template
content, so there is no adoption to observe. Making `ownerDocument` truthful
for those trees is its own change (sanitizers such as DOMPurify parse into
them). `formStateRestoreCallback` stays uncalled: there is no form-state
restoration to trigger it.

## Amendment 2 (2026-11-27): owner markers, exact undo

Review of the above found three places where the text and the code needed to
change.

- **Form ownership is reflected too.** Extraction resolves form owners on the
  composed (flattened) tree, where a shadow boundary is no longer visible, so it
  attributed a form-associated element inside a shadow root to an enclosing
  light-DOM form that the engine, following the HTML Standard, says it does not
  belong to. The engine now stamps `data-unblink-form-owner` on each
  form-associated custom element, naming a `data-unblink-form-key` stamped on
  its owner form (empty = no owner). For a stamped custom element,
  `dom.formFields` takes that owner over its own rule. So the "one owner rule on
  both sides" in Amendment 1 holds for native controls; for custom elements the
  engine's answer is authoritative.
- **Known gap, unchanged:** native controls inside a shadow root under a
  light-DOM form. The engine gives them no form owner, while `forms` and
  `submit_form` still list and send them by ancestry on the composed tree. Closing
  it means stamping every native shadow control and removing fields agents see
  today; it is left for its own change.
- **The live-session undo restores, it does not remove.** A page can set these
  attribute names on its own elements. Removing them after a snapshot deleted
  the page's copy and let the page detect the snapshot. One helper (`treeStamp`)
  now records each attribute's prior state and restores it exactly: the previous
  value, its absence, and attribute order.

The markers visible in `raw_html` of a rendered page are therefore
`data-unblink-form-value`, `-form-invalid`, `-form-owner`, `-form-key` and the
`data-unblink-default-*` family.
