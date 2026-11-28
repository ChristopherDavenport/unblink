package js

import (
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// <select> state.
//
// A select's value is which of its options are selected, and this engine keeps
// that in one place: the `selected` attribute on the <option> elements. Page
// JavaScript (select.value, selectedIndex, options, option.selected), interact,
// form submission and extraction all read and write that same state, so they
// cannot disagree. (interact used to write a `value` attribute on the <select>
// itself, which nothing in a page reads; React reads select.options and threw
// on the first controlled <select> — webpack.js.org's whole app.)
//
// A browser also tracks "selectedness" separately from the attribute
// (defaultSelected); here they are the same thing, as for input.value. One
// visible consequence: a single-choice select cannot be in the "nothing
// selected" state (selectedIndex -1) a script reaches with `select.value =
// "no such option"` — with nothing marked it shows its first enabled option, as
// it does on load.

// selectOptions lists a select's options in tree order (optgroup children too).
func selectOptions(sel *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			switch c.Data {
			case "option":
				out = append(out, c)
			case "optgroup":
				walk(c)
			}
		}
	}
	walk(sel)
	return out
}

// selectedOptions is the select's current selection. A single-choice select
// with nothing marked displays (and submits) its first enabled option.
func selectedOptions(sel *html.Node) []*html.Node {
	var out []*html.Node
	opts := selectOptions(sel)
	for _, o := range opts {
		if hasAttr(o, "selected") {
			out = append(out, o)
		}
	}
	if hasAttr(sel, "multiple") {
		return out
	}
	if len(out) > 1 {
		return out[len(out)-1:] // the last one marked wins, as in a browser
	}
	if len(out) == 0 {
		for _, o := range opts {
			if !hasAttr(o, "disabled") {
				return []*html.Node{o}
			}
		}
	}
	return out
}

// optionValue is an option's value: its value attribute, else its text.
func optionValue(o *html.Node) string {
	if hasAttr(o, "value") {
		return getAttr(o, "value")
	}
	return strings.Join(strings.Fields(textContent(o)), " ")
}

// selectValue is select.value: the first selected option's value, or "".
func selectValue(sel *html.Node) string {
	if s := selectedOptions(sel); len(s) > 0 {
		return optionValue(s[0])
	}
	return ""
}

// setSelected marks exactly the options keep() accepts, recording mutations.
func (b *bridge) setSelected(sel *html.Node, keep func(i int, o *html.Node) bool) {
	for i, o := range selectOptions(sel) {
		want := keep(i, o)
		if want == hasAttr(o, "selected") {
			continue
		}
		if want {
			b.setAttrMut(o, "selected", "")
		} else {
			b.removeAttrMut(o, "selected")
		}
	}
}

// selectByValue is `select.value = v`: the first option with that value becomes
// the selection; no match leaves nothing selected.
func (b *bridge) selectByValue(sel *html.Node, v string) {
	done := false
	b.setSelected(sel, func(_ int, o *html.Node) bool {
		if !done && optionValue(o) == v {
			done = true
			return true
		}
		return false
	})
	// The select itself carries no value attribute; drop one an older path left.
	if hasAttr(sel, "value") {
		b.removeAttrMut(sel, "value")
	}
}

func (b *bridge) installSelect() {
	vm := b.vm
	sp := b.ifaceProtos["HTMLSelectElement"]
	op := b.ifaceProtos["HTMLOptionElement"]
	if sp == nil || op == nil {
		return
	}
	b.protoProp(sp, "value",
		func(n *html.Node) goja.Value { return vm.ToValue(selectValue(n)) },
		func(n *html.Node, v goja.Value) { b.selectByValue(n, v.String()) })
	selectedIndex := func(n *html.Node) int {
		if s := selectedOptions(n); len(s) > 0 {
			for i, o := range selectOptions(n) {
				if o == s[0] {
					return i
				}
			}
		}
		return -1
	}
	setSelectedIndex := func(n *html.Node, v goja.Value) {
		idx := int(v.ToInteger())
		b.setSelected(n, func(i int, _ *html.Node) bool { return i == idx })
	}
	b.protoProp(sp, "selectedIndex",
		func(n *html.Node) goja.Value { return vm.ToValue(selectedIndex(n)) }, setSelectedIndex)
	b.protoGetter(sp, "type", func(n *html.Node) goja.Value {
		if hasAttr(n, "multiple") {
			return vm.ToValue("select-multiple")
		}
		return vm.ToValue("select-one")
	})
	b.protoGetter(sp, "length", func(n *html.Node) goja.Value { return vm.ToValue(len(selectOptions(n))) })
	b.protoGetter(sp, "selectedOptions", func(n *html.Node) goja.Value { return b.htmlCollection(selectedOptions(n)) })
	add := func(n *html.Node, call goja.FunctionCall) goja.Value {
		opt := b.unwrap(call.Argument(0))
		if opt == nil || opt.Type != html.ElementNode || (opt.Data != "option" && opt.Data != "optgroup") {
			panic(vm.NewTypeError("Failed to execute 'add': the element is not an option or optgroup"))
		}
		var before *html.Node
		if a := call.Argument(1); !goja.IsUndefined(a) && !goja.IsNull(a) {
			if before = b.unwrap(a); before == nil {
				if opts, i := selectOptions(n), int(a.ToInteger()); i >= 0 && i < len(opts) {
					before = opts[i]
				}
			}
		}
		b.detachMut(opt)
		parent := n
		if before != nil && before.Parent != nil {
			parent = before.Parent
		} else {
			before = nil
		}
		parent.InsertBefore(opt, before)
		b.onMutate(mutationRecord{typ: "childList", target: parent, added: []*html.Node{opt}})
		return goja.Undefined()
	}
	removeAt := func(n *html.Node, i int) {
		if opts := selectOptions(n); i >= 0 && i < len(opts) {
			b.detachMut(opts[i])
		}
	}
	b.protoMethod(sp, "add", add)
	b.protoMethod(sp, "item", func(n *html.Node, call goja.FunctionCall) goja.Value {
		if opts, i := selectOptions(n), int(call.Argument(0).ToInteger()); i >= 0 && i < len(opts) {
			return b.wrap(opts[i])
		}
		return goja.Null()
	})
	b.protoMethod(sp, "namedItem", func(n *html.Node, call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		for _, o := range selectOptions(n) {
			if name != "" && (getAttr(o, "id") == name || getAttr(o, "name") == name) {
				return b.wrap(o)
			}
		}
		return goja.Null()
	})
	// select.remove(index) removes an option; select.remove() is ChildNode.remove.
	b.protoMethod(sp, "remove", func(n *html.Node, call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			b.detachMut(n)
		} else {
			removeAt(n, int(call.Argument(0).ToInteger()))
		}
		return goja.Undefined()
	})
	// options: an HTMLOptionsCollection — the option list plus the select's own
	// add/remove/selectedIndex. A snapshot, like the engine's other collections.
	optionsProto := vm.CreateObject(b.protoHTMLCollection)
	b.defineCtor("HTMLOptionsCollection", optionsProto)
	b.setToStringTag(optionsProto, "HTMLOptionsCollection")
	b.protoGetter(sp, "options", func(n *html.Node) goja.Value {
		c := b.htmlCollection(selectOptions(n))
		_ = c.SetPrototype(optionsProto)
		_ = c.Set("add", func(call goja.FunctionCall) goja.Value { return add(n, call) })
		_ = c.Set("remove", func(call goja.FunctionCall) goja.Value {
			removeAt(n, int(call.Argument(0).ToInteger()))
			return goja.Undefined()
		})
		b.defineProp(c, "selectedIndex",
			func() goja.Value { return vm.ToValue(selectedIndex(n)) },
			func(v goja.Value) { setSelectedIndex(n, v) })
		return c
	})

	b.protoGetter(op, "index", func(n *html.Node) goja.Value {
		if sel := enclosingSelect(n); sel != nil {
			for i, o := range selectOptions(sel) {
				if o == n {
					return vm.ToValue(i)
				}
			}
		}
		return vm.ToValue(0)
	})
	// new Option(text, value, defaultSelected, selected)
	ctor := vm.ToValue(func(call goja.ConstructorCall) *goja.Object {
		o := &html.Node{Type: html.ElementNode, Data: "option", DataAtom: atom.Option}
		if a := call.Argument(0); !goja.IsUndefined(a) && a.String() != "" {
			o.AppendChild(&html.Node{Type: html.TextNode, Data: a.String()})
		}
		if a := call.Argument(1); !goja.IsUndefined(a) {
			setAttr(o, "value", a.String())
		}
		if call.Argument(2).ToBoolean() || call.Argument(3).ToBoolean() {
			setAttr(o, "selected", "")
		}
		w, _ := b.wrap(o).(*goja.Object)
		return w
	}).(*goja.Object)
	_ = ctor.Set("prototype", op)
	_ = ctor.DefineDataProperty("name", vm.ToValue("Option"), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
	_ = vm.Set("Option", ctor)
}
