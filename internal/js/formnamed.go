package js

import (
	"strconv"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// Named access: `form.fieldname`, `form[0]`, `form.elements.fieldname`,
// `document.formname`. Page code written against forms leans on it heavily
// (`this.form.q.value` in an inline handler, `document.search.submit()`).
//
// A browser exposes these as own "named properties" of the object. Here the
// lookup lives one step up the prototype chain: the API prelude places a Proxy
// between HTMLFormElement.prototype (and Document.prototype) and its parent, whose
// get trap asks namedGet for anything the real chain does not have. Two documented
// consequences: a built-in always wins (a browser lets a control named "submit"
// shadow form.submit — [LegacyOverrideBuiltIns] — and this engine does not), and
// `'fieldname' in form` is false, because a `has` trap is not told which object
// was asked. Inline event handlers, whose scope chain is the document and the
// form owner, get scope objects that answer `has` themselves (namedScope).
//
// Window named access (a global per element id) is deliberately absent: it would
// put a lookup behind every unresolved global name and let an element id answer
// `typeof module` or `typeof require`.

// installFormNamedAccess builds RadioNodeList and HTMLFormControlsCollection and
// hands the prelude the lookup it wires into the prototype chains.
func (b *bridge) installFormNamedAccess() {
	vm := b.vm

	b.protoRadioNodeList = vm.CreateObject(b.protoNodeList)
	b.defineCtor("RadioNodeList", b.protoRadioNodeList)
	b.setToStringTag(b.protoRadioNodeList, "RadioNodeList")
	// value is the checked radio button's value ("on" when it has none); setting
	// it checks the button carrying that value and unchecks the rest.
	radios := func(this goja.Value) []*html.Node {
		o := this.ToObject(vm)
		var out []*html.Node
		for i, n := int64(0), o.Get("length").ToInteger(); i < n; i++ {
			if el := b.nodeOf(o.Get(strconv.FormatInt(i, 10))); el != nil && el.Data == "input" && getAttr(el, "type") == "radio" {
				out = append(out, el)
			}
		}
		return out
	}
	radioValue := func(n *html.Node) string {
		if hasAttr(n, "value") {
			return getAttr(n, "value")
		}
		return "on"
	}
	_ = b.protoRadioNodeList.DefineAccessorProperty("value",
		vm.ToValue(func(call goja.FunctionCall) goja.Value {
			for _, r := range radios(call.This) {
				if hasAttr(r, "checked") {
					return vm.ToValue(radioValue(r))
				}
			}
			return vm.ToValue("")
		}),
		vm.ToValue(func(call goja.FunctionCall) goja.Value {
			want := call.Argument(0).String()
			rs := radios(call.This)
			var hit *html.Node
			for _, r := range rs {
				if radioValue(r) == want {
					hit = r
					break
				}
			}
			if hit == nil {
				return goja.Undefined()
			}
			for _, r := range rs {
				if r != hit && hasAttr(r, "checked") {
					removeAttr(r, "checked")
					b.onMutate(mutationRecord{typ: "attributes", target: r, attr: "checked"})
				}
			}
			if !hasAttr(hit, "checked") {
				b.setAttrMut(hit, "checked", "")
			}
			return goja.Undefined()
		}),
		goja.FLAG_TRUE, goja.FLAG_TRUE)

	b.protoFormControls = vm.CreateObject(b.protoHTMLCollection)
	b.defineCtor("HTMLFormControlsCollection", b.protoFormControls)
	b.setToStringTag(b.protoFormControls, "HTMLFormControlsCollection")
	_ = b.protoFormControls.Set("namedItem", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		if name == "" {
			return goja.Null()
		}
		o := call.This.ToObject(vm)
		var hits []*html.Node
		for i, n := int64(0), o.Get("length").ToInteger(); i < n; i++ {
			if el := b.nodeOf(o.Get(strconv.FormatInt(i, 10))); el != nil && namedBy(el, name) {
				hits = append(hits, el)
			}
		}
		if v := b.namedResult(hits); v != nil {
			return v
		}
		return goja.Null()
	})

	// Taken by the API prelude and removed before page code runs.
	_ = vm.Set("__unblinkNamedGet", func(call goja.FunctionCall) goja.Value {
		if v := b.namedGet(call.Argument(0), call.Argument(1).String()); v != nil {
			return v
		}
		return goja.Undefined()
	})
}

// namedBy reports whether an element answers to name through its id or name.
func namedBy(n *html.Node, name string) bool {
	return getAttr(n, "id") == name || getAttr(n, "name") == name
}

// namedResult is what a named lookup over a form's controls returns: the one
// element, a RadioNodeList when several share the name, nil when none do.
func (b *bridge) namedResult(hits []*html.Node) goja.Value {
	switch len(hits) {
	case 0:
		return nil
	case 1:
		return b.wrap(hits[0])
	}
	arr := make([]interface{}, len(hits))
	for i, n := range hits {
		arr[i] = b.wrap(n)
	}
	o := b.vm.NewArray(arr...)
	_ = o.SetPrototype(b.protoRadioNodeList)
	return o
}

// formControlsCollection is form.elements: the controls in tree order, with each
// distinct id/name also reachable as a property. Like every collection here it is
// a snapshot taken when read.
func (b *bridge) formControlsCollection(form *html.Node) goja.Value {
	controls := b.formControls(form)
	arr := make([]interface{}, len(controls))
	for i, n := range controls {
		arr[i] = b.wrap(n)
	}
	o := b.vm.NewArray(arr...)
	_ = o.SetPrototype(b.protoFormControls)
	seen := make(map[string]bool)
	for _, n := range controls {
		for _, key := range []string{getAttr(n, "id"), getAttr(n, "name")} {
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			if cur := o.Get(key); cur != nil && !goja.IsUndefined(cur) {
				continue // an index, length, or a collection method keeps its meaning
			}
			var hits []*html.Node
			for _, c := range controls {
				if namedBy(c, key) {
					hits = append(hits, c)
				}
			}
			_ = o.DefineDataProperty(key, b.namedResult(hits), goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_FALSE)
		}
	}
	return o
}

// namedGet resolves a property the prototype chain of a form or a document does
// not define. Returns nil when the name selects nothing.
func (b *bridge) namedGet(receiver goja.Value, key string) goja.Value {
	if key == "" {
		return nil
	}
	n := b.nodeOf(receiver)
	if n == nil {
		return nil
	}
	switch {
	case n.Type == html.ElementNode && n.Data == "form" && n.Namespace == "":
		controls := b.formControls(n)
		if i, err := strconv.Atoi(key); err == nil && strconv.Itoa(i) == key {
			if i >= 0 && i < len(controls) {
				return b.wrap(controls[i])
			}
			return nil
		}
		var hits []*html.Node
		for _, c := range controls {
			if namedBy(c, key) {
				hits = append(hits, c)
			}
		}
		if len(hits) == 0 { // then the form's images, by the same rule
			for _, img := range queryAll(n, "img") {
				if namedBy(img, key) {
					hits = append(hits, img)
				}
			}
		}
		return b.namedResult(hits)
	case n.Type == html.DocumentNode:
		// The document's named elements: embed, form, iframe, img and object by
		// name; object also by id, and img by id when it has a name too.
		var hits []*html.Node
		for _, el := range queryAll(n, "embed, form, iframe, img, object") {
			name, id := getAttr(el, "name"), getAttr(el, "id")
			switch {
			case name == key:
			case id == key && (el.Data == "object" || (el.Data == "img" && name != "")):
			default:
				continue
			}
			hits = append(hits, el)
		}
		switch len(hits) {
		case 0:
			return nil
		case 1:
			return b.wrap(hits[0])
		}
		return b.htmlCollection(hits)
	}
	return nil
}

// namedScope wraps a form or the document for an inline event handler's scope
// chain, where an identifier is resolved by asking `has` and then `get`: the
// object's own chain first, then its named elements.
func (b *bridge) namedScope(o goja.Value) goja.Value {
	if b.namedScopeFn == nil || o == nil {
		return o
	}
	if v, err := b.namedScopeFn(goja.Undefined(), o); err == nil {
		return v
	}
	return o
}

// takePreludeHelpers collects the functions the API prelude leaves for the
// engine and removes them from the page world. Runs right after the preludes.
func (b *bridge) takePreludeHelpers() {
	g := b.vm.GlobalObject()
	if fn, ok := goja.AssertFunction(g.Get("__unblinkNamedScope")); ok {
		b.namedScopeFn = fn
	}
	_ = g.Delete("__unblinkNamedScope")
}
