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
