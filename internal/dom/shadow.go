package dom

import "golang.org/x/net/html"

// ComposeDeclarativeShadow flattens declarative Shadow DOM in place. For each element with
// a direct `<template shadowrootmode="open|closed">` (or the legacy `shadowroot`) child,
// the host's children are replaced by the template's content with each `<slot>` resolved
// to the host's other (light) children by slot name (or the slot's fallback content). SSR
// web components thus render on the static, no-JS path — otherwise their content is inert
// inside a `<template>`, which every extraction walk skips. Nested declarative shadow roots
// compose depth-first. A no-op when the document has no declarative shadow template.
//
// This mirrors the imperative compose in internal/js (slots.go); dom must not import js, so
// the small slot-distribution walk is duplicated here. Under `--js` the imperative
// attachShadow path owns shadow composition, so this runs only on the no-JS path.
func ComposeDeclarativeShadow(doc *html.Node) {
	if doc != nil {
		composeDSD(doc, 0)
	}
}

// maxDSDDepth bounds declarative-shadow recursion against pathological nesting.
const maxDSDDepth = 1000

// composeDSD promotes a declarative shadow template on n (if any) after recursing into its
// subtree, so nested shadow roots are already composed when their host is processed.
func composeDSD(n *html.Node, depth int) {
	if depth > maxDSDDepth {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		composeDSD(c, depth+1)
	}
	if n.Type != html.ElementNode {
		return
	}
	tmpl := declarativeShadowTemplate(n)
	if tmpl == nil {
		return
	}
	light := groupLightBySlot(n, tmpl)
	var composed []*html.Node
	for c := tmpl.FirstChild; c != nil; c = c.NextSibling {
		composed = append(composed, composeDSDNode(c, light, depth+1)...)
	}
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
	for _, c := range composed {
		n.AppendChild(c)
	}
}

// declarativeShadowTemplate returns host's direct `<template shadowrootmode|shadowroot>`
// child, or nil.
func declarativeShadowTemplate(host *html.Node) *html.Node {
	for c := host.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "template" &&
			(hasAttr(c, "shadowrootmode") || hasAttr(c, "shadowroot")) {
			return c
		}
	}
	return nil
}

// groupLightBySlot buckets the host's children (all except the shadow template) by slot
// name — the `slot` attribute for elements, "" (the default slot) for everything else.
func groupLightBySlot(host, tmpl *html.Node) map[string][]*html.Node {
	m := map[string][]*html.Node{}
	for c := host.FirstChild; c != nil; c = c.NextSibling {
		if c == tmpl {
			continue
		}
		name := ""
		if c.Type == html.ElementNode {
			name = attr(c, "slot")
		}
		m[name] = append(m[name], c)
	}
	return m
}

// composeDSDNode returns the composed replacement(s) for a template-side node: a `<slot>`
// becomes clones of its assigned light nodes (or its composed fallback content when nothing
// is assigned); any other node is shallow-cloned with its children composed, since slots may
// nest arbitrarily deep inside the shadow template.
func composeDSDNode(sc *html.Node, light map[string][]*html.Node, depth int) []*html.Node {
	if depth > maxDSDDepth {
		return nil
	}
	if sc.Type == html.ElementNode && sc.Data == "slot" {
		assigned := light[attr(sc, "name")]
		if len(assigned) == 0 { // fallback content
			var out []*html.Node
			for fc := sc.FirstChild; fc != nil; fc = fc.NextSibling {
				out = append(out, composeDSDNode(fc, light, depth+1)...)
			}
			return out
		}
		out := make([]*html.Node, 0, len(assigned))
		for _, a := range assigned {
			out = append(out, CloneTree(a))
		}
		return out
	}
	c := &html.Node{Type: sc.Type, DataAtom: sc.DataAtom, Data: sc.Data, Namespace: sc.Namespace}
	if len(sc.Attr) > 0 {
		c.Attr = append([]html.Attribute(nil), sc.Attr...)
	}
	for gc := sc.FirstChild; gc != nil; gc = gc.NextSibling {
		for _, cc := range composeDSDNode(gc, light, depth+1) {
			c.AppendChild(cc)
		}
	}
	return []*html.Node{c}
}
