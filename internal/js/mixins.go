package js

import (
	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// This file implements the DOM Standard's ParentNode and ChildNode mixins, once,
// for every interface that includes them:
//
//	ParentNode: Element, Document, DocumentFragment (and so ShadowRoot)
//	ChildNode:  Element, CharacterData (Text, Comment)
//
// They are mixins in the standard precisely because a fragment or a text node is
// not an Element; installing them on Element.prototype alone leaves
// template.content.querySelector(), fragment.append() and text.remove()
// undefined, which is how every "clone a template, query it, insert it" library
// (htmx, lit-html, Solid, preact-markup) fails.

// nodesFromArgs converts a (...(Node or DOMString)) argument list into the single
// node the "convert nodes into a node" algorithm yields: the node itself for one
// node argument, otherwise a fragment holding all of them in order. Strings
// become Text nodes. It detaches the argument nodes from their current parents.
func (b *bridge) nodesFromArgs(args []goja.Value) *html.Node {
	var nodes []*html.Node
	for _, a := range args {
		if n := b.unwrap(a); n != nil {
			nodes = append(nodes, n)
			continue
		}
		s := ""
		if a != nil && !goja.IsUndefined(a) {
			s = a.String()
		} else if a != nil {
			s = "undefined"
		}
		nodes = append(nodes, &html.Node{Type: html.TextNode, Data: s})
	}
	if len(nodes) == 1 {
		return nodes[0]
	}
	frag := &html.Node{Type: html.DocumentNode}
	for _, n := range nodes {
		if n.Type == html.DocumentNode { // a fragment argument contributes its children
			for n.FirstChild != nil {
				c := n.FirstChild
				n.RemoveChild(c)
				frag.AppendChild(c)
			}
			continue
		}
		b.detachMut(n)
		frag.AppendChild(n)
	}
	return frag
}

// detachMut removes n from its parent, recording the childList mutation.
func (b *bridge) detachMut(n *html.Node) {
	if n.Parent == nil {
		return
	}
	parent := n.Parent
	parent.RemoveChild(n)
	b.onMutate(mutationRecord{typ: "childList", target: parent, removed: []*html.Node{n}})
}

// argNodeSet is the set of nodes named in an argument list, for the "viable
// sibling" search of before/after/replaceWith.
func (b *bridge) argNodeSet(args []goja.Value) map[*html.Node]bool {
	set := make(map[*html.Node]bool, len(args))
	for _, a := range args {
		if n := b.unwrap(a); n != nil {
			set[n] = true
		}
	}
	return set
}

// installParentNode adds the ParentNode mixin to proto. queries also installs
// querySelector/querySelectorAll and the element-child accessors — Element
// already has its own, so it passes false.
func (b *bridge) installParentNode(proto *goja.Object, queries bool) {
	vm := b.vm
	if queries {
		b.protoMethod(proto, "querySelector", func(n *html.Node, call goja.FunctionCall) goja.Value {
			return b.wrap(b.pageQuery(n, call.Argument(0).String()))
		})
		b.protoMethod(proto, "querySelectorAll", func(n *html.Node, call goja.FunctionCall) goja.Value {
			return b.nodeList(b.pageQueryAll(n, call.Argument(0).String()))
		})
		b.protoGetter(proto, "children", func(n *html.Node) goja.Value { return b.htmlCollection(childElements(n)) })
		b.protoGetter(proto, "firstElementChild", func(n *html.Node) goja.Value {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					return b.wrap(c)
				}
			}
			return goja.Null()
		})
		b.protoGetter(proto, "lastElementChild", func(n *html.Node) goja.Value {
			for c := n.LastChild; c != nil; c = c.PrevSibling {
				if c.Type == html.ElementNode {
					return b.wrap(c)
				}
			}
			return goja.Null()
		})
		b.protoGetter(proto, "childElementCount", func(n *html.Node) goja.Value { return vm.ToValue(len(childElements(n))) })
		// NonElementParentNode: Document and DocumentFragment only.
		b.protoMethod(proto, "getElementById", func(n *html.Node, call goja.FunctionCall) goja.Value {
			return b.wrap(findByID(n, call.Argument(0).String()))
		})
	}
	b.protoMethod(proto, "append", func(n *html.Node, call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			b.insert(n, b.nodesFromArgs(call.Arguments), nil)
		}
		return goja.Undefined()
	})
	b.protoMethod(proto, "prepend", func(n *html.Node, call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			node := b.nodesFromArgs(call.Arguments)
			b.insert(n, node, n.FirstChild)
		}
		return goja.Undefined()
	})
	b.protoMethod(proto, "replaceChildren", func(n *html.Node, call goja.FunctionCall) goja.Value {
		node := b.nodesFromArgs(call.Arguments) // detaches the arguments first (they may be current children)
		var removed []*html.Node
		for n.FirstChild != nil {
			c := n.FirstChild
			n.RemoveChild(c)
			removed = append(removed, c)
		}
		if len(removed) > 0 {
			b.onMutate(mutationRecord{typ: "childList", target: n, removed: removed})
		}
		if len(call.Arguments) > 0 {
			b.insert(n, node, nil)
		}
		return goja.Undefined()
	})
}

// installChildNode adds the ChildNode mixin (before/after/replaceWith/remove).
func (b *bridge) installChildNode(proto *goja.Object) {
	b.protoMethod(proto, "remove", func(n *html.Node, _ goja.FunctionCall) goja.Value {
		b.detachMut(n)
		return goja.Undefined()
	})
	b.protoMethod(proto, "before", func(n *html.Node, call goja.FunctionCall) goja.Value {
		parent := n.Parent
		if parent == nil || len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		// viable previous sibling: the nearest one not being moved.
		set := b.argNodeSet(call.Arguments)
		prev := n.PrevSibling
		for prev != nil && set[prev] {
			prev = prev.PrevSibling
		}
		node := b.nodesFromArgs(call.Arguments)
		ref := parent.FirstChild
		if prev != nil {
			ref = prev.NextSibling
		}
		b.insert(parent, node, ref)
		return goja.Undefined()
	})
	b.protoMethod(proto, "after", func(n *html.Node, call goja.FunctionCall) goja.Value {
		parent := n.Parent
		if parent == nil || len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		set := b.argNodeSet(call.Arguments)
		next := n.NextSibling
		for next != nil && set[next] {
			next = next.NextSibling
		}
		b.insert(parent, b.nodesFromArgs(call.Arguments), next)
		return goja.Undefined()
	})
	b.protoMethod(proto, "replaceWith", func(n *html.Node, call goja.FunctionCall) goja.Value {
		parent := n.Parent
		if parent == nil {
			return goja.Undefined()
		}
		set := b.argNodeSet(call.Arguments)
		next := n.NextSibling
		for next != nil && set[next] {
			next = next.NextSibling
		}
		var node *html.Node
		if len(call.Arguments) > 0 {
			node = b.nodesFromArgs(call.Arguments)
		}
		if n.Parent == parent { // n was not itself one of the replacements
			b.detachMut(n)
		}
		if node != nil {
			b.insert(parent, node, next)
		}
		return goja.Undefined()
	})
}

// attrNode builds an Attr-shaped object for owner's attribute name. It is a
// snapshot with a live `value` accessor: reading or writing value goes through
// the element, so code that mutates an Attr it got from getAttributeNode sees
// (and causes) the real change.
func (b *bridge) attrNode(owner *html.Node, name string) *goja.Object {
	vm := b.vm
	it := vm.NewObject()
	_ = it.Set("name", name)
	_ = it.Set("localName", name)
	_ = it.Set("nodeName", name)
	_ = it.Set("nodeType", 2)
	_ = it.Set("specified", true)
	_ = it.Set("namespaceURI", goja.Null())
	_ = it.Set("prefix", goja.Null())
	b.defineGetter(it, "ownerElement", func() goja.Value {
		if owner != nil && hasAttr(owner, name) {
			return b.wrap(owner)
		}
		return goja.Null()
	})
	detached := ""
	get := func() goja.Value {
		if owner != nil && hasAttr(owner, name) {
			return vm.ToValue(getAttr(owner, name))
		}
		return vm.ToValue(detached)
	}
	set := func(v goja.Value) {
		if owner != nil && hasAttr(owner, name) {
			b.setAttrMut(owner, name, v.String())
			return
		}
		detached = v.String()
	}
	if owner != nil {
		detached = getAttr(owner, name)
	}
	b.defineProp(it, "value", get, set)
	b.defineProp(it, "nodeValue", get, set)
	b.defineProp(it, "textContent", get, set)
	return it
}

// installAttrNodes adds the Attr-node half of the Element attribute API
// (getAttributeNode/setAttributeNode/removeAttributeNode and the NS spellings).
// React removes attributes it does not recognise during hydration through
// removeAttributeNode.
func (b *bridge) installAttrNodes(p *goja.Object) {
	vm := b.vm
	b.protoMethod(p, "getAttributeNode", func(n *html.Node, call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		if !hasAttr(n, name) {
			return goja.Null()
		}
		return b.attrNodeCached(n, name)
	})
	b.protoMethod(p, "getAttributeNodeNS", func(n *html.Node, call goja.FunctionCall) goja.Value {
		name := call.Argument(1).String()
		if !hasAttr(n, name) {
			return goja.Null()
		}
		return b.attrNodeCached(n, name)
	})
	setNode := func(n *html.Node, call goja.FunctionCall) goja.Value {
		a := call.Argument(0)
		if a == nil || goja.IsUndefined(a) || goja.IsNull(a) {
			return goja.Null()
		}
		ao := a.ToObject(vm)
		name := ao.Get("name")
		if name == nil || goja.IsUndefined(name) {
			return goja.Null()
		}
		old := goja.Null()
		if hasAttr(n, name.String()) {
			prev := b.attrNode(nil, name.String())
			_ = prev.Set("value", getAttr(n, name.String()))
			old = prev
		}
		val := ""
		if v := ao.Get("value"); v != nil && !goja.IsUndefined(v) {
			val = v.String()
		}
		b.setAttrMut(n, name.String(), val)
		return old
	}
	b.protoMethod(p, "setAttributeNode", setNode)
	b.protoMethod(p, "setAttributeNodeNS", setNode)
	b.protoMethod(p, "removeAttributeNode", func(n *html.Node, call goja.FunctionCall) goja.Value {
		a := call.Argument(0)
		if a == nil || goja.IsUndefined(a) || goja.IsNull(a) {
			return goja.Undefined()
		}
		name := a.ToObject(vm).Get("name")
		if name == nil || goja.IsUndefined(name) || !hasAttr(n, name.String()) {
			b.throwDOMException("NotFoundError", "The node to be removed is not an attribute of this element.")
			return goja.Undefined()
		}
		key := name.String()
		old := getAttr(n, key)
		removeAttr(n, key)
		b.onMutate(mutationRecord{typ: "attributes", target: n, attr: key, oldValue: old})
		return a
	})
}
