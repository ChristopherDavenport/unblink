package js

import (
	"strings"

	"golang.org/x/net/html"
)

// Action is one synthetic DOM interaction the engine replays after the page's own
// scripts and lifecycle events have run, so delegated or just-attached listeners
// are in place. Selector is resolved against the live document; Value (when set)
// is written to the target form control before the event fires so listeners that
// read el.value observe it. Matched is filled by the engine — true when Selector
// resolved to a node during replay.
type Action struct {
	Selector string // CSS selector (cascadia)
	Type     string // "click" (default), "input", "change", "keydown", "keyup", "keypress", "submit"
	Value    string // optional; written to an input/textarea before dispatch
	Key      string // optional key for keydown/keyup/keypress (e.g. "Enter", "ArrowDown", "a"); defaults to Enter
	Matched  bool   // engine-filled output: did Selector resolve during replay?

	// WaitFor/WaitText, when set, hold the post-dispatch settle open until the
	// selector resolves and/or the text appears (or the budget elapses), so an agent
	// can "click, then wait for the result".
	WaitFor  string
	WaitText string
}

// eventShape maps an action type to (bubbles, cancelable) per the DOM spec for
// the events unblink dispatches. input/change are non-cancelable; click, keydown
// and submit bubble and are cancelable.
func eventShape(typ string) (bubbles, cancelable bool) {
	switch typ {
	case "input", "change":
		return true, false
	default: // click, keydown, submit, and anything else
		return true, true
	}
}

// pressSequence is the primary-button gesture a real mouse fires for one click,
// in browser order. Modern component libraries (react-aria, Radix, …) activate
// on the pointerdown→pointerup pair, not a bare click, so interact must replay
// the whole sequence.
var pressSequence = []string{"pointerdown", "mousedown", "pointerup", "mouseup", "click"}

// runActions replays actions in order, after fireLifecycle. For each it resolves
// the node, optionally writes a control value, then dispatches — a realistic
// gesture for click/hover/focus, or a single event otherwise — through the normal
// capture → target → bubble path. Unmatched selectors are skipped (best-effort);
// actions[i].Matched records whether the selector resolved. The slice is shared
// with the caller and is safe to read after Dispatch returns (the engine's <-done
// wait gives the happens-before edge). This path is Dispatch-only (Render does
// not run actions).
func (b *bridge) runActions(actions []Action) {
	for i := range actions {
		a := &actions[i]
		typ := a.Type
		if typ == "" {
			typ = "click"
		}
		n := b.queryPierce(b.doc, a.Selector) // pierce shadow: target controls a component rendered into its shadow root
		if n == nil {
			continue // a.Matched stays false
		}
		a.Matched = true
		if a.Value != "" {
			b.setControlValue(n, a.Value)
		}
		switch typ {
		case "click":
			b.dispatchPress(n)
		case "hover":
			b.dispatchHover(n)
		case "focus":
			b.focusNode(n)
		case "keydown", "keyup", "keypress":
			b.dispatchKey(n, typ, a.Key)
		default:
			bubbles, cancelable := eventShape(typ)
			b.dispatchOnNode(n, b.newEvent(typ, bubbles, cancelable, b.wrap(n)))
		}
	}
}

// dispatchKey replays a keyboard interaction on n. For event=keydown it fires the
// full browser sequence — keydown → (for a printable key: append the character to
// the control's value and fire input) → keypress → keyup — so search-as-you-type,
// arrow-key menus, and Escape-to-close widgets activate. Enter on a control inside
// a <form> triggers implicit form submission (unless a handler preventDefault'd the
// keydown/keypress), matching a real browser. keyup/keypress requested explicitly
// dispatch just that one event.
func (b *bridge) dispatchKey(n *html.Node, typ, key string) {
	info := resolveKey(key)
	if typ != "keydown" {
		b.dispatchOnNode(n, b.newKeyEvent(typ, info, true, true, b.wrap(n)))
		return
	}

	downOK := b.dispatchOnNode(n, b.newKeyEvent("keydown", info, true, true, b.wrap(n)))
	pressOK := true
	if info.printable && downOK {
		// A printable keydown that wasn't canceled types the character: append to
		// the control value and fire a real input event before keypress/keyup.
		if isTextEntry(n) {
			b.setControlValue(n, controlText(n)+info.char)
			b.dispatchOnNode(n, b.newEvent("input", true, false, b.wrap(n)))
		}
	}
	if info.printable || info.key == "Enter" {
		pressOK = b.dispatchOnNode(n, b.newKeyEvent("keypress", info, true, true, b.wrap(n)))
	}
	b.dispatchOnNode(n, b.newKeyEvent("keyup", info, true, true, b.wrap(n)))

	// Implicit form submission: Enter on a control in a form submits it unless a
	// handler canceled the keydown or keypress.
	if info.key == "Enter" && downOK && pressOK {
		if form := ancestorForm(n); form != nil {
			b.submitForm(form, true)
		}
	}
}

// isTextEntry reports whether n is a control that accepts typed character input
// (so a printable keydown appends to its value).
func isTextEntry(n *html.Node) bool {
	switch n.Data {
	case "textarea":
		return true
	case "input":
		switch strings.ToLower(getAttr(n, "type")) {
		case "", "text", "search", "email", "url", "tel", "password", "number":
			return true
		}
	}
	return false
}

// controlText reads a control's current value the way setControlValue writes it.
func controlText(n *html.Node) string {
	if n.Data == "textarea" {
		return textContent(n)
	}
	return getAttr(n, "value")
}

// ancestorForm returns the nearest <form> ancestor of n (the form a control's
// implicit-submission Enter targets), or nil.
func ancestorForm(n *html.Node) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "form" {
			return p
		}
	}
	return nil
}

// dispatchPress replays a realistic primary-button press at n: pointerdown →
// mousedown → focus → pointerup → mouseup → click. The full sequence always
// fires regardless of preventDefault — browsers still deliver click when
// pointerdown is canceled, and usePress cancels pointerdown yet expects click.
func (b *bridge) dispatchPress(n *html.Node) {
	for _, typ := range pressSequence {
		b.dispatchOnNode(n, b.newUIEvent(typ, true, true, b.wrap(n)))
		if typ == "mousedown" {
			// Browsers move focus to the nearest focusable ancestor on mousedown.
			if ft := focusableTarget(n); ft != nil {
				b.focusNode(ft)
			}
		}
	}
}

// dispatchHover replays a pointer entering n: pointerover → mouseover →
// mouseenter → mousemove. mouseenter is the non-bubbling counterpart of
// mouseover. Drives hover-reveal menus and tooltips.
func (b *bridge) dispatchHover(n *html.Node) {
	b.dispatchOnNode(n, b.newUIEvent("pointerover", true, true, b.wrap(n)))
	b.dispatchOnNode(n, b.newUIEvent("mouseover", true, true, b.wrap(n)))
	b.dispatchOnNode(n, b.newUIEvent("mouseenter", false, false, b.wrap(n)))
	b.dispatchOnNode(n, b.newUIEvent("mousemove", true, true, b.wrap(n)))
}

// setControlValue writes val to a form control the way the .value accessor reads
// it: as textarea content for <textarea>, otherwise the value attribute. It
// reports through the mutation sink (domVersion is the settle and staleness
// signal — a value-only write must invalidate live-page snapshots) but not
// through afterAttr: a .value= property write must not fire a custom element's
// attributeChangedCallback the way a real setAttribute would.
func (b *bridge) setControlValue(n *html.Node, val string) {
	if n.Data == "textarea" {
		setTextContent(n, val)
		b.onMutate(mutationRecord{typ: "characterData", target: n})
		return
	}
	old := getAttr(n, "value")
	setAttr(n, "value", val)
	b.onMutate(mutationRecord{typ: "attributes", target: n, attr: "value", oldValue: old})
}
