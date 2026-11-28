package js

import (
	"strings"

	"golang.org/x/net/html"
)

// Click activation behaviour (HTML "activation behavior", legacy-pre-activation
// steps). A click on a checkbox or radio button changes its checkedness before
// the event is dispatched, so a listener reading `el.checked` sees the new
// state; a canceled click puts it back; an uncanceled one is followed by
// `input` and `change`. A click on a <label> is forwarded to its control. Both
// element.click() and the interact tool's click go through clickActivate.

// clickActivate runs dispatch — which must dispatch the click on n and report
// whether it was not canceled — around n's activation behaviour.
func (b *bridge) clickActivate(n *html.Node, dispatch func() bool) {
	undo, changed := b.preActivate(n)
	ok := dispatch()
	if undo != nil {
		if !ok {
			undo()
			return
		}
		if changed {
			b.dispatchOnNode(n, b.newEvent("input", true, false, b.wrap(n)))
			b.dispatchOnNode(n, b.newEvent("change", true, false, b.wrap(n)))
		}
		return
	}
	if ok {
		b.activateLabel(n)
	}
}

// preActivate applies a checkbox's or radio button's state change ahead of the
// click. It returns how to undo it (nil when n has no such behaviour) and
// whether the checkedness actually changed.
func (b *bridge) preActivate(n *html.Node) (undo func(), changed bool) {
	if n.Type != html.ElementNode || n.Namespace != "" || n.Data != "input" || controlDisabled(n) {
		return nil, false
	}
	switch strings.ToLower(getAttr(n, "type")) {
	case "checkbox":
		was := hasAttr(n, "checked")
		b.setChecked(n, !was)
		return func() { b.setChecked(n, was) }, true
	case "radio":
		if hasAttr(n, "checked") {
			return func() {}, false
		}
		var prev *html.Node
		for _, r := range b.radioGroup(n) {
			if r != n && hasAttr(r, "checked") {
				prev = r
				b.setChecked(r, false)
			}
		}
		b.setChecked(n, true)
		return func() {
			b.setChecked(n, false)
			if prev != nil {
				b.setChecked(prev, true)
			}
		}, true
	}
	return nil, false
}

// setChecked writes a control's checkedness the way the .checked setter does.
func (b *bridge) setChecked(n *html.Node, on bool) {
	if on {
		b.setAttrMut(n, "checked", "")
		return
	}
	if hasAttr(n, "checked") {
		removeAttr(n, "checked")
		b.onMutate(mutationRecord{typ: "attributes", target: n, attr: "checked"})
	}
}

// radioGroup returns the radio buttons sharing n's name and form owner (or,
// with no form, n's tree). A radio button with no name is a group of its own.
func (b *bridge) radioGroup(n *html.Node) []*html.Node {
	name := getAttr(n, "name")
	if name == "" {
		return []*html.Node{n}
	}
	owner := b.formOwner(n)
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		if c.Type == html.ElementNode && c.Namespace == "" && c.Data == "input" &&
			strings.EqualFold(getAttr(c, "type"), "radio") && getAttr(c, "name") == name && b.formOwner(c) == owner {
			out = append(out, c)
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			walk(k)
		}
	}
	walk(treeRoot(n))
	return out
}

// controlDisabled reports whether a form control is disabled, by its own
// attribute or an enclosing <fieldset disabled> (outside its first legend).
func controlDisabled(n *html.Node) bool {
	if hasAttr(n, "disabled") {
		return true
	}
	for p, child := n.Parent, n; p != nil; p, child = p.Parent, p {
		if p.Type == html.ElementNode && p.Data == "fieldset" && hasAttr(p, "disabled") {
			if first := firstChildElement(p, "legend"); first != nil && first == child {
				continue
			}
			return true
		}
	}
	return false
}

func firstChildElement(p *html.Node, tag string) *html.Node {
	for c := p.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			return c
		}
	}
	return nil
}

// activateLabel forwards an uncanceled click inside a <label> to the label's
// control, unless the click was on that control (or inside it) already.
func (b *bridge) activateLabel(target *html.Node) {
	var label *html.Node
	for p := target; p != nil; p = p.Parent {
		if p.Type != html.ElementNode || p.Namespace != "" {
			continue
		}
		switch p.Data {
		case "label":
			label = p
		case "a", "button", "input", "select", "textarea":
			if label == nil {
				return // an interactive element between the click and any label takes the click itself
			}
		}
		if label != nil {
			break
		}
	}
	if label == nil {
		return
	}
	ctl := labeledControl(label)
	if ctl == nil || ctl == target || controlDisabled(ctl) {
		return
	}
	for p := target; p != nil; p = p.Parent {
		if p == ctl {
			return
		}
	}
	b.clickActivate(ctl, func() bool {
		return b.dispatchOnNode(ctl, b.newUIEvent("click", true, true, b.wrap(ctl)))
	})
}

// labeledControl is a label's control: the element its for= names, else its
// first labelable descendant.
func labeledControl(label *html.Node) *html.Node {
	labelable := func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Namespace != "" {
			return false
		}
		switch n.Data {
		case "button", "meter", "output", "progress", "select", "textarea":
			return true
		case "input":
			return !strings.EqualFold(getAttr(n, "type"), "hidden")
		}
		return false
	}
	if hasAttr(label, "for") {
		if c := findByID(treeRoot(label), getAttr(label, "for")); c != nil && labelable(c) {
			return c
		}
		return nil
	}
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil && found == nil; c = c.NextSibling {
			if labelable(c) {
				found = c
				return
			}
			walk(c)
		}
	}
	walk(label)
	return found
}

// isFormControlTag reports whether n is an element the disabled attribute
// applies to.
func isFormControlTag(n *html.Node) bool {
	if n.Type != html.ElementNode || n.Namespace != "" {
		return false
	}
	switch n.Data {
	case "button", "input", "select", "textarea", "fieldset", "optgroup", "option":
		return true
	}
	return false
}
