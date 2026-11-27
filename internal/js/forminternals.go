package js

import (
	"net/url"
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

// Form-associated custom elements (HTML Standard §4.13.7 ElementInternals, §4.10.17
// form-control infrastructure).
//
// The split with the prelude: everything that is a question about the *tree* lives
// here (form owner, disabled-by-fieldset, labels, the lifecycle reactions, the form's
// entry list, and the reflection onto the tree for extraction); the ElementInternals
// object itself — validity flags, CustomStateSet, the ARIA mixin — is plain JS in
// prelude_api.go, which pushes the two facts Go needs (the submission value and
// whether the element is invalid) through the __unblinkFormInternals hook.
//
// formStateRestoreCallback is never invoked: unblink has no session-history restore
// or autofill, the only two triggers the standard defines for it.

// faceEntry is one name/value pair a form-associated custom element contributes to
// its form's entry list. file marks a File value, of which only the name is kept.
type faceEntry struct {
	name, value string
	file        bool
}

// faceState is the per-element form-association state. form/disabled cache the last
// values delivered to formAssociatedCallback/formDisabledCallback so each fires only
// on a real change.
type faceState struct {
	form     *html.Node
	disabled bool

	// Submission value from internals.setFormValue: none (null), a single value
	// submitted under the element's name attribute, or a FormData entry list.
	mode    int
	scalar  faceEntry // name unused; the element's name attribute is read at use
	entries []faceEntry

	invalid bool // internals.setValidity left at least one flag set
}

const (
	faceNone = iota
	faceScalar
	faceList
)

// isFACE reports whether n is an element whose definition is form-associated.
func (b *bridge) isFACE(n *html.Node) bool {
	return n != nil && n.Type == html.ElementNode && b.formAssoc[n.Data]
}

func (b *bridge) faceStateOf(n *html.Node) *faceState {
	st := b.faces[n]
	if st == nil {
		st = &faceState{}
		b.faces[n] = st
	}
	return st
}

// readDefinitionFlags records the static formAssociated / disabledFeatures of a
// definition at customElements.define, as the standard does (they are read once).
func (b *bridge) readDefinitionFlags(name string, ctor *goja.Object) {
	delete(b.formAssoc, name)
	delete(b.internalsDisabled, name)
	if v := ctor.Get("formAssociated"); v != nil && v.ToBoolean() {
		b.formAssoc[name] = true
	}
	if df := ctor.Get("disabledFeatures"); df != nil && !goja.IsUndefined(df) && !goja.IsNull(df) {
		_ = b.vm.Try(func() {
			b.vm.ForOf(df, func(v goja.Value) bool {
				if v.String() == "internals" {
					b.internalsDisabled[name] = true
				}
				return true
			})
		})
	}
}

// faceDisabled reports whether a form-associated custom element is disabled: its own
// disabled attribute, or a disabled <fieldset> ancestor — unless the element sits
// inside that fieldset's first <legend>, which stays enabled.
func faceDisabled(n *html.Node) bool {
	if hasAttr(n, "disabled") {
		return true
	}
	for child, p := n, n.Parent; p != nil; child, p = p, p.Parent {
		if p.Type != html.ElementNode || p.Data != "fieldset" || !hasAttr(p, "disabled") {
			continue
		}
		var legend *html.Node
		for c := p.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "legend" {
				legend = c
				break
			}
		}
		if legend == nil || legend != child {
			return true
		}
	}
	return false
}

// faceWillValidate is the "candidate for constraint validation" test: a disabled or
// readonly element, or one inside a <datalist>, is barred.
func faceWillValidate(n *html.Node) bool {
	if faceDisabled(n) || hasAttr(n, "readonly") {
		return false
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "datalist" {
			return false
		}
	}
	return true
}

// syncFACE recomputes n's form owner and disabled state and fires
// formAssociatedCallback / formDisabledCallback for whichever changed. The owner is
// the nearest ancestor <form> (the form="id" attribute is not honoured, matching this
// engine's native controls); the walk stops at a shadow boundary, as the standard's
// does.
func (b *bridge) syncFACE(n *html.Node) {
	st := b.faceStateOf(n)
	if owner := ancestorForm(n); owner != st.form {
		st.form = owner
		b.invokeLifecycleArgs(n, "formAssociatedCallback", b.wrap(owner))
	}
	if dis := faceDisabled(n); dis != st.disabled {
		st.disabled = dis
		b.invokeLifecycleArgs(n, "formDisabledCallback", b.vm.ToValue(dis))
	}
}

// syncFACETree runs syncFACE over every upgraded form-associated custom element at
// or under root. Called when a subtree leaves the tree or a disabled attribute
// changes (insertion is covered by scan). Free when the page defines none.
func (b *bridge) syncFACETree(root *html.Node) {
	if root == nil || len(b.faces) == 0 {
		return
	}
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if _, ok := b.faces[nd]; ok {
			b.syncFACE(nd)
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
}

func (b *bridge) invokeLifecycleArgs(n *html.Node, name string, args ...goja.Value) {
	o, ok := b.wrap(n).(*goja.Object)
	if !ok {
		return
	}
	if fn, ok := goja.AssertFunction(o.Get(name)); ok {
		b.callSafe(fn, o, args...)
	}
}

// faceEntries is what n contributes to its form's entry list right now: nothing when
// disabled or when no submission value is set; a FormData value's own entries; or a
// single value under the element's name attribute (nothing if it has no name).
func (b *bridge) faceEntries(n *html.Node) []faceEntry {
	st := b.faces[n]
	if st == nil || faceDisabled(n) {
		return nil
	}
	switch st.mode {
	case faceList:
		return st.entries
	case faceScalar:
		if name := getAttr(n, "name"); name != "" {
			return []faceEntry{{name: name, value: st.scalar.value, file: st.scalar.file}}
		}
	}
	return nil
}

// faceCheckValidity is internals.checkValidity(): an invalid candidate fires a
// cancelable `invalid` event and reports false.
func (b *bridge) faceCheckValidity(n *html.Node) bool {
	st := b.faces[n]
	if st == nil || !st.invalid || !faceWillValidate(n) {
		return true
	}
	b.dispatchOnNode(n, b.newEvent("invalid", false, true, b.wrap(n)))
	return false
}

// validateForm statically validates a form's constraints. Only form-associated
// custom elements carry validity here — native controls have no constraint
// validation model in this engine and count as valid.
func (b *bridge) validateForm(form *html.Node) bool {
	ok := true
	for _, c := range b.formControls(form) {
		if b.isFACE(c) && !b.faceCheckValidity(c) {
			ok = false
		}
	}
	return ok
}

// resetForm is form.reset(): a cancelable reset event, then formResetCallback on each
// form-associated custom element. Native controls are not restored — the engine keeps
// a control's current value in its value attribute and has no separate default.
func (b *bridge) resetForm(form *html.Node) {
	if form == nil || form.Data != "form" {
		return
	}
	ev := b.newEvent("reset", true, true, b.wrap(form))
	if !b.dispatchOnNode(form, ev) || ev.defaultPrevented {
		return
	}
	for _, c := range b.formControls(form) {
		if b.isFACE(c) {
			b.invokeLifecycleArgs(c, "formResetCallback")
		}
	}
}

// faceLabels returns the <label> elements whose labeled control is n: a label whose
// for attribute names n's id, or a for-less label whose first labelable descendant is n.
func (b *bridge) faceLabels(n *html.Node) []*html.Node {
	root := n
	for root.Parent != nil {
		root = root.Parent
	}
	id := getAttr(n, "id")
	var out []*html.Node
	for _, l := range queryAll(root, "label") {
		if hasAttr(l, "for") {
			if id != "" && getAttr(l, "for") == id {
				out = append(out, l)
			}
			continue
		}
		if b.firstLabelable(l) == n {
			out = append(out, l)
		}
	}
	return out
}

func (b *bridge) firstLabelable(root *html.Node) *html.Node {
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.Data {
		case "button", "meter", "output", "progress", "select", "textarea":
			return c
		case "input":
			if !strings.EqualFold(getAttr(c, "type"), "hidden") {
				return c
			}
		default:
			if b.formAssoc[c.Data] {
				return c
			}
		}
		if found := b.firstLabelable(c); found != nil {
			return found
		}
	}
	return nil
}

// isShadowIncludingDescendant reports whether n is a descendant of anc, crossing
// from a shadow root to its host (setValidity's anchor must be one).
func (b *bridge) isShadowIncludingDescendant(n, anc *html.Node) bool {
	for p := n; p != nil; {
		if p.Parent == nil {
			host, ok := b.shadowHostOf[p]
			if !ok {
				return false
			}
			p = host
		} else {
			p = p.Parent
		}
		if p == anc {
			return true
		}
	}
	return false
}

// installFormInternals exposes the tree-side primitives the prelude's
// ElementInternals is built on. The prelude consumes and deletes the hook, like the
// other __unblink* bootstrap hooks.
func (b *bridge) installFormInternals() {
	vm := b.vm
	h := vm.NewObject()
	el := func(call goja.FunctionCall) *html.Node {
		if n := b.unwrap(call.Argument(0)); n != nil && n.Type == html.ElementNode {
			return n
		}
		return nil
	}
	// kind: 0 not a custom element, -1 internals disabled by the definition,
	// 1 custom element, 2 form-associated custom element.
	_ = h.Set("kind", func(call goja.FunctionCall) goja.Value {
		n := el(call)
		switch {
		case n == nil || n.Namespace != "" || b.customElements[n.Data] == nil:
			return vm.ToValue(0)
		case b.internalsDisabled[n.Data]:
			return vm.ToValue(-1)
		case b.formAssoc[n.Data]:
			return vm.ToValue(2)
		}
		return vm.ToValue(1)
	})
	_ = h.Set("form", func(call goja.FunctionCall) goja.Value {
		if n := el(call); n != nil {
			return b.wrap(ancestorForm(n))
		}
		return goja.Null()
	})
	_ = h.Set("labels", func(call goja.FunctionCall) goja.Value {
		if n := el(call); n != nil {
			return b.nodeList(b.faceLabels(n))
		}
		return b.nodeList(nil)
	})
	_ = h.Set("disabled", func(call goja.FunctionCall) goja.Value {
		n := el(call)
		return vm.ToValue(n != nil && faceDisabled(n))
	})
	_ = h.Set("willValidate", func(call goja.FunctionCall) goja.Value {
		n := el(call)
		return vm.ToValue(n != nil && faceWillValidate(n))
	})
	_ = h.Set("shadowRoot", func(call goja.FunctionCall) goja.Value {
		// Unlike element.shadowRoot this returns a closed root too — handing the
		// component its own closed root is what internals.shadowRoot is for.
		if n := el(call); n != nil {
			if sr, ok := b.shadowRoots[n]; ok {
				return sr
			}
		}
		return goja.Null()
	})
	_ = h.Set("contains", func(call goja.FunctionCall) goja.Value {
		n, anchor := el(call), b.unwrap(call.Argument(1))
		return vm.ToValue(n != nil && anchor != nil && b.isShadowIncludingDescendant(anchor, n))
	})
	// setValue(el, mode, names, values, files): the submission value, flattened to
	// parallel arrays (names is ignored for a scalar value).
	_ = h.Set("setValue", func(call goja.FunctionCall) goja.Value {
		n := el(call)
		if n == nil {
			return goja.Undefined()
		}
		st := b.faceStateOf(n)
		st.mode, st.scalar, st.entries = int(call.Argument(1).ToInteger()), faceEntry{}, nil
		var names, values []string
		var files []bool
		_ = vm.ExportTo(call.Argument(2), &names)
		_ = vm.ExportTo(call.Argument(3), &values)
		_ = vm.ExportTo(call.Argument(4), &files)
		for i := range values {
			e := faceEntry{value: values[i]}
			if i < len(names) {
				e.name = names[i]
			}
			if i < len(files) {
				e.file = files[i]
			}
			if st.mode == faceScalar {
				st.scalar = e
				break
			}
			st.entries = append(st.entries, e)
		}
		b.domVersion++ // the value is part of the snapshot (reflectFormValues)
		return goja.Undefined()
	})
	_ = h.Set("setInvalid", func(call goja.FunctionCall) goja.Value {
		if n := el(call); n != nil {
			b.faceStateOf(n).invalid = call.Argument(1).ToBoolean()
			b.domVersion++
		}
		return goja.Undefined()
	})
	_ = h.Set("checkValidity", func(call goja.FunctionCall) goja.Value {
		n := el(call)
		return vm.ToValue(n == nil || b.faceCheckValidity(n))
	})
	_ = h.Set("validateForm", func(call goja.FunctionCall) goja.Value {
		n := el(call)
		return vm.ToValue(n == nil || n.Data != "form" || b.validateForm(n))
	})
	_ = vm.Set("__unblinkFormInternals", h)
}

// reflectFormValues writes each form-associated custom element's current entry-list
// contribution onto the tree as page.FormValueAttr (and page.FormInvalidAttr when it
// fails validation), so extraction — which reads only the *html.Node tree, never the
// JS heap — sees the control and its value. The returned func removes the attributes
// again; a live session calls it once the snapshot is serialized so page JS never
// observes them. The writes bypass the mutation sink for the same reason.
//
// File values are left out: a filename is not the file, and submit_form takes file
// content from its caller.
func (b *bridge) reflectFormValues() (undo func()) {
	if len(b.faces) == 0 {
		return func() {}
	}
	stamped := make([]*html.Node, 0, len(b.faces))
	for n, st := range b.faces {
		if !b.isFACE(n) {
			continue
		}
		var sb strings.Builder
		for _, e := range b.faceEntries(n) {
			if e.file {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('&')
			}
			sb.WriteString(url.QueryEscape(e.name))
			sb.WriteByte('=')
			sb.WriteString(url.QueryEscape(e.value))
		}
		setAttr(n, page.FormValueAttr, sb.String())
		if st.invalid && faceWillValidate(n) {
			setAttr(n, page.FormInvalidAttr, "")
		}
		stamped = append(stamped, n)
	}
	return func() {
		for _, n := range stamped {
			removeAttr(n, page.FormValueAttr)
			removeAttr(n, page.FormInvalidAttr)
		}
	}
}
