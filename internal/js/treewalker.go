package js

import (
	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// TreeWalker — enough of the DOM TreeWalker for lit-html, which walks a cloned
// template fragment with createTreeWalker(root, SHOW_ELEMENT|SHOW_COMMENT) and
// pulls nodes in document order via nextNode(), tracking currentNode.

// nodeFilterBit maps a node to its NodeFilter SHOW_* bit.
func nodeFilterBit(n *html.Node) int {
	switch n.Type {
	case html.ElementNode:
		return 0x1 // SHOW_ELEMENT
	case html.TextNode:
		return 0x4 // SHOW_TEXT
	case html.CommentNode:
		return 0x80 // SHOW_COMMENT
	default:
		return 0
	}
}

// preOrderNext returns the next node after n in a document-order (pre-order) walk
// bounded by root, or nil at the end.
func preOrderNext(n, root *html.Node) *html.Node {
	if n.FirstChild != nil {
		return n.FirstChild
	}
	for cur := n; cur != nil && cur != root; cur = cur.Parent {
		if cur.NextSibling != nil {
			return cur.NextSibling
		}
	}
	return nil
}

// nodeFilterAccepts builds the acceptance test shared by TreeWalker and
// NodeIterator from a whatToShow bitmask (0 / -1 mean "show all") and an optional
// NodeFilter (a function, or an object with acceptNode). Only FILTER_ACCEPT
// passes; for these flat walks FILTER_REJECT and FILTER_SKIP are equivalent.
func (b *bridge) nodeFilterAccepts(whatToShow int64, filter goja.Value) func(*html.Node) bool {
	vm := b.vm
	showAll := whatToShow == 0 || whatToShow == -1 || whatToShow == 0xFFFFFFFF
	var filterFn goja.Callable
	if filter != nil && !goja.IsUndefined(filter) && !goja.IsNull(filter) {
		if fo := filter.ToObject(vm); fo != nil {
			if fn, ok := goja.AssertFunction(fo.Get("acceptNode")); ok {
				filterFn = fn
			} else if fn, ok := goja.AssertFunction(filter); ok {
				filterFn = fn
			}
		}
	}
	return func(n *html.Node) bool {
		if !showAll && whatToShow&int64(nodeFilterBit(n)) == 0 {
			return false
		}
		if filterFn != nil {
			v, err := filterFn(goja.Undefined(), b.wrap(n))
			if err == nil && v != nil && v.ToInteger() != 1 { // 1 = FILTER_ACCEPT
				return false
			}
		}
		return true
	}
}

// createNodeIterator builds a NodeIterator over root's subtree (root included),
// in document order. The iterator's position is a reference node plus a
// before/after flag, as in the DOM Standard, so nextNode() then previousNode()
// returns the same node. Angular's hydration walks the SSR DOM for its comment
// markers with one.
func (b *bridge) createNodeIterator(root *html.Node, whatToShow int64, filter goja.Value) *goja.Object {
	vm := b.vm
	it := vm.NewObject()
	accepts := b.nodeFilterAccepts(whatToShow, filter)
	ref, before := root, true

	// following/preceding within root's inclusive subtree.
	following := func(n *html.Node) *html.Node { return preOrderNext(n, root) }
	preceding := func(n *html.Node) *html.Node {
		if n == root {
			return nil
		}
		if n.PrevSibling != nil {
			p := n.PrevSibling
			for p.LastChild != nil {
				p = p.LastChild
			}
			return p
		}
		return n.Parent
	}
	step := func(forward bool) goja.Value {
		node, beforeNode := ref, before
		for {
			if forward {
				if !beforeNode {
					if node = following(node); node == nil {
						return goja.Null()
					}
				}
				beforeNode = false
			} else {
				if beforeNode {
					if node = preceding(node); node == nil {
						return goja.Null()
					}
				}
				beforeNode = true
			}
			if accepts(node) {
				ref, before = node, beforeNode
				return b.wrap(node)
			}
		}
	}
	_ = it.Set("root", b.wrap(root))
	_ = it.Set("whatToShow", whatToShow)
	_ = it.Set("filter", filter)
	b.defineGetter(it, "referenceNode", func() goja.Value { return b.wrap(ref) })
	b.defineGetter(it, "pointerBeforeReferenceNode", func() goja.Value { return vm.ToValue(before) })
	_ = it.Set("nextNode", func(goja.FunctionCall) goja.Value { return step(true) })
	_ = it.Set("previousNode", func(goja.FunctionCall) goja.Value { return step(false) })
	_ = it.Set("detach", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	return it
}

// createTreeWalker builds a TreeWalker rooted at root. whatToShow is a NodeFilter
// bitmask (0 / -1 mean "show all").
func (b *bridge) createTreeWalker(root *html.Node, whatToShow int64, filter goja.Value) *goja.Object {
	vm := b.vm
	tw := vm.NewObject()
	current := root

	accepts := b.nodeFilterAccepts(whatToShow, filter)

	advance := func(forward bool) goja.Value {
		n := current
		for {
			var next *html.Node
			if forward {
				next = preOrderNext(n, root)
			} else {
				next = preOrderPrev(n, root)
			}
			if next == nil {
				return goja.Null()
			}
			n = next
			if accepts(n) {
				current = n
				return b.wrap(n)
			}
		}
	}

	b.defineProp(tw, "currentNode",
		func() goja.Value { return b.wrap(current) },
		func(v goja.Value) {
			if nn := b.unwrap(v); nn != nil {
				current = nn
			}
		})
	_ = tw.Set("root", b.wrap(root))
	_ = tw.Set("whatToShow", whatToShow)
	_ = tw.Set("nextNode", func(goja.FunctionCall) goja.Value { return advance(true) })
	_ = tw.Set("previousNode", func(goja.FunctionCall) goja.Value { return advance(false) })
	_ = tw.Set("parentNode", func(goja.FunctionCall) goja.Value {
		if current.Parent != nil && current != root {
			current = current.Parent
			return b.wrap(current)
		}
		return goja.Null()
	})
	_ = tw.Set("firstChild", func(goja.FunctionCall) goja.Value {
		if current.FirstChild != nil {
			current = current.FirstChild
			return b.wrap(current)
		}
		return goja.Null()
	})
	_ = tw.Set("nextSibling", func(goja.FunctionCall) goja.Value {
		if current.NextSibling != nil {
			current = current.NextSibling
			return b.wrap(current)
		}
		return goja.Null()
	})
	return tw
}

// preOrderPrev returns the previous node before n in document order, bounded by root.
func preOrderPrev(n, root *html.Node) *html.Node {
	if n == root {
		return nil
	}
	if n.PrevSibling != nil {
		p := n.PrevSibling
		for p.LastChild != nil {
			p = p.LastChild
		}
		return p
	}
	if n.Parent != nil && n.Parent != root {
		return n.Parent
	}
	return nil
}

// installNodeFilter adds the NodeFilter global with SHOW_* constants.
func (b *bridge) installNodeFilter() {
	nf := b.vm.NewObject()
	for k, v := range map[string]int64{
		"SHOW_ALL": 0xFFFFFFFF, "SHOW_ELEMENT": 0x1, "SHOW_TEXT": 0x4,
		"SHOW_COMMENT": 0x80, "SHOW_DOCUMENT": 0x100, "SHOW_DOCUMENT_FRAGMENT": 0x400,
		"FILTER_ACCEPT": 1, "FILTER_REJECT": 2, "FILTER_SKIP": 3,
	} {
		_ = nf.Set(k, v)
	}
	_ = b.vm.Set("NodeFilter", nf)
}
