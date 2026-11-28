package js

import (
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Go-side DOM helpers added for framework rendering: tree mutation that records
// MutationObserver entries, node cloning/insertion, and the dataset/attributes
// companion objects. The pure *html.Node helpers (getAttr, childNodes, …) live in
// bridge.go.

// nodeType maps an html.Node to its DOM nodeType constant. The real document node
// is wrapped as the dedicated document object (own nodeType=9); any DocumentNode
// reaching here is a synthetic fragment.
func nodeType(n *html.Node) int {
	switch n.Type {
	case html.ElementNode:
		return 1
	case html.TextNode:
		return 3
	case html.CommentNode:
		return 8
	case html.DocumentNode:
		return 11
	default:
		return 0
	}
}

func nodeName(n *html.Node) string {
	switch n.Type {
	case html.TextNode:
		return "#text"
	case html.CommentNode:
		return "#comment"
	case html.DocumentNode:
		return "#document-fragment"
	default:
		if n.Namespace != "" {
			return n.Data // SVG/MathML names keep their case
		}
		return strings.ToUpper(n.Data)
	}
}

func prevElement(n *html.Node) *html.Node {
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode {
			return s
		}
	}
	return nil
}

// contains reports whether n is parent itself or a descendant of it (DOM semantics).
func contains(parent, n *html.Node) bool {
	for a := n; a != nil; a = a.Parent {
		if a == parent {
			return true
		}
	}
	return false
}

// treeRoot walks to the topmost ancestor (the document node for connected nodes).
func treeRoot(n *html.Node) *html.Node {
	for n.Parent != nil {
		n = n.Parent
	}
	return n
}

// firstInTreeOrder returns whichever of a or b a pre-order walk from root hits
// first (nil if neither is under root). Used for document-order comparison.
func firstInTreeOrder(root, a, b *html.Node) *html.Node {
	var found *html.Node
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n == a || n == b {
			found = n
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(root)
	return found
}

// cloneNode copies a node, deeply when requested. The clone is detached and
// uncached (lazily wrapped on first access).
func cloneNode(n *html.Node, deep bool) *html.Node {
	c := &html.Node{
		Type:      n.Type,
		DataAtom:  n.DataAtom,
		Data:      n.Data,
		Namespace: n.Namespace,
		Attr:      append([]html.Attribute(nil), n.Attr...),
	}
	if deep {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.AppendChild(cloneNode(ch, true))
		}
	}
	return c
}

// namespaceFor maps a namespace URI to the x/net/html foreign-content namespace
// label ("svg"/"math"), so html.Render emits foreign elements correctly.
func namespaceFor(ns string) string {
	switch ns {
	case "http://www.w3.org/2000/svg":
		return "svg"
	case "http://www.w3.org/1998/Math/MathML":
		return "math"
	}
	return ""
}

// namespaceURIFor maps an x/net/html namespace label ("svg"/"math"/"") back to
// the namespace URI for Element.namespaceURI; the empty label is plain HTML.
func namespaceURIFor(label string) string {
	switch label {
	case "svg":
		return "http://www.w3.org/2000/svg"
	case "math":
		return "http://www.w3.org/1998/Math/MathML"
	}
	return "http://www.w3.org/1999/xhtml"
}

// setAttrMut sets an attribute, records the mutation, and notifies an upgraded
// custom element's attributeChangedCallback when the attribute is observed.
func (b *bridge) setAttrMut(n *html.Node, key, val string) {
	old := getAttr(n, key)
	setAttr(n, key, val)
	if len(key) > 2 && key[0] == 'o' && key[1] == 'n' {
		b.handlerAttrSet(n, key[2:])
	}
	b.onMutate(mutationRecord{typ: "attributes", target: n, attr: key, oldValue: old})
	b.afterAttr(n, key, old, val)
}

// insert places node under parent before ref (or appends when ref is nil),
// handling DocumentFragment by moving its children, recording the mutation, and
// upgrading any newly-connected custom elements.
func (b *bridge) insert(parent, node, ref *html.Node) {
	if node.Type == html.DocumentNode { // fragment sentinel: move its children
		var added []*html.Node
		for c := node.FirstChild; c != nil; {
			next := c.NextSibling
			node.RemoveChild(c)
			if ref != nil && ref.Parent == parent {
				parent.InsertBefore(c, ref)
			} else {
				parent.AppendChild(c)
			}
			added = append(added, c)
			c = next
		}
		for _, c := range added {
			b.connect(c)
		}
		if len(added) > 0 {
			b.onMutate(mutationRecord{typ: "childList", target: parent, added: added})
		}
		return
	}
	detach(node)
	if ref != nil && ref.Parent == parent {
		parent.InsertBefore(node, ref)
	} else {
		parent.AppendChild(node)
	}
	b.connect(node)
	b.onMutate(mutationRecord{typ: "childList", target: parent, added: []*html.Node{node}})
}

// insertAdjacentHTML parses markup and splices it relative to n.
func (b *bridge) insertAdjacentHTML(n *html.Node, position, markup string) {
	ctx := n
	if ctx.Type != html.ElementNode {
		ctx = &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	}
	frag, err := html.ParseFragment(strings.NewReader(markup), ctx)
	if err != nil {
		return
	}
	for _, c := range frag {
		b.insertAdjacentNode(n, position, c)
	}
}

// insertAdjacentNode splices el at one of beforebegin/afterbegin/beforeend/afterend.
func (b *bridge) insertAdjacentNode(n *html.Node, position string, el *html.Node) {
	switch strings.ToLower(position) {
	case "beforebegin":
		if n.Parent != nil {
			b.insert(n.Parent, el, n)
		}
	case "afterbegin":
		b.insert(n, el, n.FirstChild)
	case "beforeend":
		b.insert(n, el, nil)
	case "afterend":
		if n.Parent != nil {
			b.insert(n.Parent, el, n.NextSibling)
		}
	}
}

// namedNodeMap returns the cached, LIVE NamedNodeMap for n.attributes: length,
// indices and names are read from the element on every access. Liveness is
// load-bearing — React clears a singleton element (<html>, <head>, <body>) with
// `for (var a = el.attributes; a.length;) el.removeAttributeNode(a[0])`, which
// never terminates (or throws on the second pass) over a snapshot.
func (b *bridge) namedNodeMap(n *html.Node) goja.Value {
	if o, ok := b.attrMapCache[n]; ok {
		return o
	}
	vm := b.vm
	target := vm.NewObject()
	at := func(i int) goja.Value {
		if i < 0 || i >= len(n.Attr) {
			return nil
		}
		return b.attrNodeCached(n, n.Attr[i].Key)
	}
	named := func(name string) goja.Value {
		if !hasAttr(n, name) {
			return nil
		}
		return b.attrNodeCached(n, name)
	}
	_ = target.Set("item", func(call goja.FunctionCall) goja.Value {
		if v := at(int(call.Argument(0).ToInteger())); v != nil {
			return v
		}
		return goja.Null()
	})
	getNamed := func(call goja.FunctionCall) goja.Value {
		if v := named(call.Argument(len(call.Arguments) - 1).String()); v != nil {
			return v
		}
		return goja.Null()
	}
	_ = target.Set("getNamedItem", getNamed)
	_ = target.Set("getNamedItemNS", getNamed)
	setNamed := func(call goja.FunctionCall) goja.Value {
		a := call.Argument(0)
		if a == nil || goja.IsUndefined(a) || goja.IsNull(a) {
			return goja.Null()
		}
		ao := a.ToObject(vm)
		name, val := ao.Get("name"), ao.Get("value")
		if name == nil || goja.IsUndefined(name) {
			return goja.Null()
		}
		v := ""
		if val != nil && !goja.IsUndefined(val) {
			v = val.String()
		}
		b.setAttrMut(n, name.String(), v)
		return goja.Null()
	}
	_ = target.Set("setNamedItem", setNamed)
	_ = target.Set("setNamedItemNS", setNamed)
	removeNamed := func(call goja.FunctionCall) goja.Value {
		name := call.Argument(len(call.Arguments) - 1).String()
		if !hasAttr(n, name) {
			b.throwDOMException("NotFoundError", "No item with name '"+name+"' was found.")
			return goja.Undefined()
		}
		node := b.attrNodeCached(n, name)
		old := getAttr(n, name)
		removeAttr(n, name)
		b.onMutate(mutationRecord{typ: "attributes", target: n, attr: name, oldValue: old})
		return node
	}
	_ = target.Set("removeNamedItem", removeNamed)
	_ = target.Set("removeNamedItemNS", removeNamed)
	if it := b.indexedIter(); it != nil {
		_ = target.SetSymbol(goja.SymIterator, it)
	}
	handler := &goja.ProxyTrapConfig{
		GetIdx: func(_ *goja.Object, idx int, _ goja.Value) goja.Value {
			if v := at(idx); v != nil {
				return v
			}
			return goja.Undefined()
		},
		Get: func(t *goja.Object, key string, _ goja.Value) goja.Value {
			if key == "length" {
				return vm.ToValue(len(n.Attr))
			}
			if v := t.Get(key); v != nil && !goja.IsUndefined(v) {
				return v
			}
			if v := named(key); v != nil { // attributes.id, attributes['data-x']
				return v
			}
			return goja.Undefined()
		},
		HasIdx: func(_ *goja.Object, idx int) bool { return idx >= 0 && idx < len(n.Attr) },
		Has: func(t *goja.Object, key string) bool {
			if key == "length" || hasAttr(n, key) {
				return true
			}
			v := t.Get(key)
			return v != nil && !goja.IsUndefined(v)
		},
		OwnKeys: func(_ *goja.Object) *goja.Object {
			keys := make([]interface{}, len(n.Attr))
			for i := range n.Attr {
				keys[i] = strconv.Itoa(i)
			}
			return vm.NewArray(keys...)
		},
		GetOwnPropertyDescriptorIdx: func(_ *goja.Object, idx int) goja.PropertyDescriptor {
			v := at(idx)
			if v == nil {
				return goja.PropertyDescriptor{}
			}
			return goja.PropertyDescriptor{Value: v, Writable: goja.FLAG_FALSE, Enumerable: goja.FLAG_TRUE, Configurable: goja.FLAG_TRUE}
		},
	}
	o := vm.ToValue(vm.NewProxy(target, handler)).ToObject(vm)
	b.attrMapCache[n] = o
	return o
}

// attrNodeCached returns the stable Attr object for (n, name), so
// `el.attributes[0] === el.attributes[0]` and an Attr taken from the map can be
// handed back to removeAttributeNode.
func (b *bridge) attrNodeCached(n *html.Node, name string) *goja.Object {
	m := b.attrNodeCache[n]
	if m == nil {
		m = make(map[string]*goja.Object)
		b.attrNodeCache[n] = m
	}
	if o, ok := m[name]; ok {
		return o
	}
	o := b.attrNode(n, name)
	m[name] = o
	return o
}

// datasetFor returns the cached DOMStringMap proxy for n.dataset, mapping camelCase
// keys to data-kebab attributes.
func (b *bridge) datasetFor(n *html.Node) goja.Value {
	if o, ok := b.datasetCache[n]; ok {
		return o
	}
	vm := b.vm
	attrOf := func(key string) string { return "data-" + camelToKebab(key) }
	handler := &goja.ProxyTrapConfig{
		Get: func(_ *goja.Object, key string, _ goja.Value) goja.Value {
			a := attrOf(key)
			if hasAttr(n, a) {
				return vm.ToValue(getAttr(n, a))
			}
			return goja.Undefined()
		},
		Set: func(_ *goja.Object, key string, value goja.Value, _ goja.Value) bool {
			b.setAttrMut(n, attrOf(key), value.String())
			return true
		},
		Has: func(_ *goja.Object, key string) bool { return hasAttr(n, attrOf(key)) },
		DeleteProperty: func(_ *goja.Object, key string) bool {
			removeAttr(n, attrOf(key))
			return true
		},
		GetOwnPropertyDescriptor: func(_ *goja.Object, key string) goja.PropertyDescriptor {
			a := attrOf(key)
			if !hasAttr(n, a) {
				return goja.PropertyDescriptor{}
			}
			return goja.PropertyDescriptor{
				Value:        vm.ToValue(getAttr(n, a)),
				Writable:     goja.FLAG_TRUE,
				Enumerable:   goja.FLAG_TRUE,
				Configurable: goja.FLAG_TRUE,
			}
		},
		OwnKeys: func(_ *goja.Object) *goja.Object {
			var keys []interface{}
			for _, at := range n.Attr {
				if strings.HasPrefix(at.Key, "data-") {
					keys = append(keys, kebabToCamel(strings.TrimPrefix(at.Key, "data-")))
				}
			}
			return vm.NewArray(keys...)
		},
	}
	o := vm.ToValue(vm.NewProxy(vm.NewObject(), handler)).ToObject(vm)
	b.datasetCache[n] = o
	return o
}

func camelToKebab(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			sb.WriteByte('-')
			sb.WriteRune(r + ('a' - 'A'))
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func kebabToCamel(s string) string {
	var sb strings.Builder
	up := false
	for _, r := range s {
		if r == '-' {
			up = true
			continue
		}
		if up && r >= 'a' && r <= 'z' {
			sb.WriteRune(r - ('a' - 'A'))
		} else {
			sb.WriteRune(r)
		}
		up = false
	}
	return sb.String()
}
