package js

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Form support: document.forms, form.elements, form.submit()/requestSubmit(). A
// render never performs the real cross-document fetch, but a programmatic submit —
// including the one a JS bot-check interstitial fires on DOMContentLoaded — is a
// navigation, recorded in pendingNav (like location.href) so the caller can follow
// it. GET forms carry their serialized fields as the query string; POST forms record
// the action target best-effort (the body isn't carried on the follow).

// htmlCollection wraps a node slice as a live-ish HTMLCollection: numeric indices,
// length, item(i), and namedItem(name) (matched by id, then name attribute). The
// challenge uses both document.forms[0] (index) and form.elements.namedItem("solution").
func (b *bridge) htmlCollection(nodes []*html.Node) *goja.Object {
	vm := b.vm
	o := vm.NewObject()
	for i, n := range nodes {
		_ = o.Set(strconv.Itoa(i), b.wrap(n))
	}
	_ = o.Set("length", len(nodes))
	_ = o.Set("item", func(call goja.FunctionCall) goja.Value {
		i := int(call.Argument(0).ToInteger())
		if i < 0 || i >= len(nodes) {
			return goja.Null()
		}
		return b.wrap(nodes[i])
	})
	_ = o.Set("namedItem", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		if name == "" {
			return goja.Null()
		}
		for _, n := range nodes {
			if getAttr(n, "id") == name || getAttr(n, "name") == name {
				return b.wrap(n)
			}
		}
		return goja.Null()
	})
	return o
}

// formControls returns a form's submittable controls in document order. Kept to the
// form-associated elements a submit serializes; nested forms are not a real thing.
func formControls(form *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				switch c.Data {
				case "input", "select", "textarea", "button", "fieldset", "output":
					out = append(out, c)
				}
			}
			walk(c)
		}
	}
	walk(form)
	return out
}

// controlValue reads a control's current submitted value: textarea uses its text,
// select its selected option, everything else the value attribute.
func controlValue(n *html.Node) string {
	switch n.Data {
	case "textarea":
		return textContent(n)
	case "select":
		for _, opt := range queryAll(n, "option") {
			if hasAttr(opt, "selected") {
				if v := getAttr(opt, "value"); v != "" || hasAttr(opt, "value") {
					return v
				}
				return textContent(opt)
			}
		}
		return ""
	default:
		return getAttr(n, "value")
	}
}

// serializeForm builds the successful-controls name/value set per the HTML form
// submission algorithm (skips unnamed/disabled controls, unchecked checkboxes and
// radios, and non-submitting button/reset types).
func serializeForm(form *html.Node) url.Values {
	vals := url.Values{}
	for _, c := range formControls(form) {
		name := getAttr(c, "name")
		if name == "" || hasAttr(c, "disabled") {
			continue
		}
		switch c.Data {
		case "fieldset", "output":
			continue
		case "button":
			continue // only the activated submitter submits; we don't model button values
		case "input":
			switch strings.ToLower(getAttr(c, "type")) {
			case "checkbox", "radio":
				if !hasAttr(c, "checked") {
					continue
				}
				v := getAttr(c, "value")
				if v == "" {
					v = "on"
				}
				vals.Add(name, v)
				continue
			case "submit", "reset", "button", "image", "file":
				continue
			}
		}
		vals.Add(name, controlValue(c))
	}
	return vals
}

// submitForm runs the form-submission navigation. When fireEvent is set (requestSubmit,
// not submit()), a cancelable submit event fires first — through addEventListener
// listeners and the onsubmit property handler — and cancellation (preventDefault or an
// onsubmit returning false) aborts. The target is the action resolved against the
// current URL; GET replaces the query with the serialized fields, other methods keep
// the action as-is. Recorded via navigate() so it lands in pendingNav like any other
// JS navigation.
func (b *bridge) submitForm(form *html.Node, fireEvent bool) {
	if form == nil || form.Data != "form" {
		return
	}
	if fireEvent && !b.fireSubmitEvent(form) {
		return // canceled
	}
	action := getAttr(form, "action")
	resolved := b.resolveNav(action) // action="" resolves to the current URL
	if resolved == nil {
		return
	}
	target := *resolved // copy: resolveNav("") aliases currentURL, which navigate() reads
	method := strings.ToUpper(getAttr(form, "method"))
	if method == "" {
		method = "GET"
	}
	if method == "GET" {
		target.RawQuery = serializeForm(form).Encode()
		target.Fragment = "" // a GET submit drops any fragment
	}
	b.navigate(target.String())
}

// fireSubmitEvent dispatches a cancelable, bubbling submit event at the form and
// invokes its onsubmit property handler, returning false if submission was canceled
// (preventDefault called, or the onsubmit handler returned false — the classic
// content-attribute cancel convention).
func (b *bridge) fireSubmitEvent(form *html.Node) bool {
	wrapper := b.wrap(form)
	ev := b.newEvent("submit", true, true, wrapper)
	notCanceled := b.dispatchOnNode(form, ev)
	if obj := wrapper.ToObject(b.vm); obj != nil {
		if fn, ok := goja.AssertFunction(obj.Get("onsubmit")); ok {
			if ret, err := fn(wrapper, ev.js); err == nil && ret != nil && !ret.ToBoolean() {
				return false
			}
		}
	}
	return notCanceled && !ev.defaultPrevented
}
