package js

import (
	"github.com/andybalholm/cascadia"
	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// maxComposeDepth bounds compose recursion against pathological shadow/slot nesting.
const maxComposeDepth = 1000

// Shadow DOM composition + shadow-piercing resolution (Phase 23).
//
// A shadow root's content lives in a detached subtree (see attachShadow), so page-JS
// document queries respect the boundary. unblink's OWN tooling — interact gestures,
// wait_for, and extraction — must instead see THROUGH the boundary. The piercing
// resolvers here provide that, and the compose pass flattens shadow content (resolving
// <slot> distribution) into the light tree for extraction.

// queryPierce resolves sel against the light tree under root and, on a miss, into every
// shadow subtree hosted under root (recursively, light-first). This deliberately pierces
// the encapsulation boundary that page-facing document.querySelector respects, so an
// agent can target a control a component rendered inside its shadow root.
func (b *bridge) queryPierce(root *html.Node, sel string) *html.Node {
	cs, err := compileSelector(sel)
	if err != nil {
		return nil
	}
	return b.queryPierceCompiled(root, cs)
}

func (b *bridge) queryPierceCompiled(root *html.Node, cs cascadia.Selector) *html.Node {
	if m := cascadia.Query(root, cs); m != nil {
		return m
	}
	var found *html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if sr, ok := b.shadowRoots[n]; ok {
			if srn := b.objNode[sr]; srn != nil {
				if m := b.queryPierceCompiled(srn, cs); m != nil {
					found = m
					return
				}
			}
		}
		for c := n.FirstChild; c != nil && found == nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

// shadowText returns the concatenated visible text of every shadow subtree hosted under
// root (recursively), so a text wait can also match content a component rendered into a
// shadow root. Light-tree text is checked separately by the caller, so slotted content
// (which lives in the light tree) is not double-counted here.
func (b *bridge) shadowText(root *html.Node) string {
	var out []byte
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if sr, ok := b.shadowRoots[n]; ok {
			if srn := b.objNode[sr]; srn != nil {
				out = append(out, textContent(srn)...)
				walk(srn) // nested shadow hosts inside this shadow subtree
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return string(out)
}

// --- slot distribution + compose (the browser "flattened tree") ---

// shallowClone copies a node without its children (Type/Data/Attr/Namespace), matching
// dom.CloneTree's per-node copy.
func shallowClone(n *html.Node) *html.Node {
	c := &html.Node{Type: n.Type, DataAtom: n.DataAtom, Data: n.Data, Namespace: n.Namespace}
	if len(n.Attr) > 0 {
		c.Attr = make([]html.Attribute, len(n.Attr))
		copy(c.Attr, n.Attr)
	}
	return c
}

// groupBySlot buckets a host's light children by slot name: the `slot` attribute for
// elements, "" (the default slot) for text and unslotted elements. Order within a bucket
// is light-DOM order.
func groupBySlot(host *html.Node) map[string][]*html.Node {
	m := map[string][]*html.Node{}
	for c := host.FirstChild; c != nil; c = c.NextSibling {
		name := ""
		if c.Type == html.ElementNode {
			name = getAttr(c, "slot")
		}
		m[name] = append(m[name], c)
	}
	return m
}

// flattenNode returns a fresh composed clone of n — the browser flattened tree. A shadow
// host is replaced by its shadow subtree with each <slot> resolved to the host's assigned
// light children (or the slot's fallback); a plain node recurses over its children. A
// slotted light node may itself be a host, so composition recurses through it.
func (b *bridge) flattenNode(n *html.Node, depth int) *html.Node {
	clone := shallowClone(n)
	if depth > maxComposeDepth {
		return clone
	}
	if sr, ok := b.shadowRoots[n]; ok {
		if srn := b.objNode[sr]; srn != nil {
			light := groupBySlot(n)
			for c := srn.FirstChild; c != nil; c = c.NextSibling {
				b.appendComposedShadow(clone, c, light, depth+1)
			}
			return clone
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		clone.AppendChild(b.flattenNode(c, depth+1))
	}
	return clone
}

// appendComposedShadow copies a shadow-side node sc into dst, replacing a <slot> with its
// assigned light nodes (or the slot's fallback children when nothing is assigned) and
// recursing so nested hosts and nested slots compose correctly.
func (b *bridge) appendComposedShadow(dst, sc *html.Node, light map[string][]*html.Node, depth int) {
	if depth > maxComposeDepth {
		return
	}
	if sc.Type == html.ElementNode && sc.Data == "slot" {
		assigned := light[getAttr(sc, "name")]
		if len(assigned) == 0 { // fallback content
			for fc := sc.FirstChild; fc != nil; fc = fc.NextSibling {
				b.appendComposedShadow(dst, fc, light, depth+1)
			}
			return
		}
		for _, a := range assigned {
			dst.AppendChild(b.flattenNode(a, depth+1))
		}
		return
	}
	if _, ok := b.shadowRoots[sc]; ok { // nested host inside the shadow tree
		dst.AppendChild(b.flattenNode(sc, depth+1))
		return
	}
	c := shallowClone(sc)
	dst.AppendChild(c)
	for gc := sc.FirstChild; gc != nil; gc = gc.NextSibling {
		b.appendComposedShadow(c, gc, light, depth+1)
	}
}

// ComposeShadowInto flattens all shadow content into doc in place, replacing each element
// child of doc with its composed clone. Destructive — for one-shot Render only (the
// runtime is discarded afterward). No-op without shadow roots, so non-shadow pages pay
// nothing.
func (b *bridge) ComposeShadowInto(doc *html.Node) {
	if doc == nil || len(b.shadowRoots) == 0 {
		return
	}
	var children []*html.Node
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	for _, c := range children {
		if c.Type != html.ElementNode {
			continue
		}
		flat := b.flattenNode(c, 0)
		doc.InsertBefore(flat, c)
		doc.RemoveChild(c)
	}
}

// flattenCloneDoc returns a composed clone of the live document WITHOUT mutating it, so a
// live session's separate shadow subtrees survive for the next Dispatch. When no shadow
// roots exist it returns the live doc unchanged (the caller serializes it as before).
func (b *bridge) flattenCloneDoc() *html.Node {
	if b.doc == nil || len(b.shadowRoots) == 0 {
		return b.doc
	}
	clone := &html.Node{Type: b.doc.Type, DataAtom: b.doc.DataAtom, Data: b.doc.Data}
	for c := b.doc.FirstChild; c != nil; c = c.NextSibling {
		clone.AppendChild(b.flattenNode(c, 0))
	}
	return clone
}

// slotAssigned implements <slot>.assignedNodes/assignedElements: it resolves the slot's
// host across the shadow boundary and returns the host's light children matching this
// slot's name; with {flatten:true} and nothing assigned, the slot's fallback children.
func (b *bridge) slotAssigned(slot *html.Node, opts goja.Value, elementsOnly bool) goja.Value {
	var nodes []*html.Node
	if slot.Type == html.ElementNode && slot.Data == "slot" {
		root := slot
		for root.Parent != nil {
			root = root.Parent
		}
		if host, ok := b.shadowHostOf[root]; ok {
			nodes = groupBySlot(host)[getAttr(slot, "name")]
		}
		if len(nodes) == 0 && flattenOpt(b.vm, opts) {
			for fc := slot.FirstChild; fc != nil; fc = fc.NextSibling {
				nodes = append(nodes, fc)
			}
		}
	}
	out := make([]interface{}, 0, len(nodes))
	for _, nd := range nodes {
		if elementsOnly && nd.Type != html.ElementNode {
			continue
		}
		out = append(out, b.wrap(nd))
	}
	return b.vm.NewArray(out...)
}

func flattenOpt(vm *goja.Runtime, opts goja.Value) bool {
	if opts == nil || goja.IsUndefined(opts) || goja.IsNull(opts) {
		return false
	}
	if oo := opts.ToObject(vm); oo != nil {
		return boolProp(oo, "flatten")
	}
	return false
}
