package js

import "golang.org/x/net/html"

// Action is one synthetic DOM interaction the engine replays after the page's own
// scripts and lifecycle events have run, so delegated or just-attached listeners
// are in place. Selector is resolved against the live document; Value (when set)
// is written to the target form control before the event fires so listeners that
// read el.value observe it. Matched is filled by the engine — true when Selector
// resolved to a node during replay.
type Action struct {
	Selector string // CSS selector (cascadia)
	Type     string // "click" (default), "input", "change", "keydown", "submit"
	Value    string // optional; written to an input/textarea before dispatch
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
		n := query(b.doc, a.Selector)
		if n == nil {
			continue // a.Matched stays false
		}
		a.Matched = true
		if a.Value != "" {
			setControlValue(n, a.Value)
		}
		switch typ {
		case "click":
			b.dispatchPress(n)
		case "hover":
			b.dispatchHover(n)
		case "focus":
			b.focusNode(n)
		default:
			bubbles, cancelable := eventShape(typ)
			b.dispatchOnNode(n, b.newEvent(typ, bubbles, cancelable, b.wrap(n)))
		}
	}
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
// it: as textarea content for <textarea>, otherwise the value attribute.
func setControlValue(n *html.Node, val string) {
	if n.Data == "textarea" {
		setTextContent(n, val)
		return
	}
	setAttr(n, "value", val)
}
