package js

import "golang.org/x/net/html"

// A <template>'s children are not its children. In a browser the parser puts
// them in the template's content DocumentFragment: the element itself has no
// child nodes, a selector query never reaches inside, and a tree walk steps over
// it. The x/net/html tree keeps them under the element, and this bridge used to
// move them to a fragment only when page code first read `.content` — so until
// then they were visible as children, and a library that walks the document
// before reading `.content` initialised nodes that were about to be pulled out
// from under it (Alpine: every expression inside an x-teleport/x-if template then
// lost its scope and threw a ReferenceError).
//
// sealTemplates makes the browser's shape true from the start: it runs over the
// document when it is bound and over every tree parsed afterwards.

// sealTemplates moves the children of every <template> at or under root into its
// content fragment. Declarative shadow roots (shadowrootmode) are left for the
// extraction-side composer, which reads them in place.
func (b *bridge) sealTemplates(root *html.Node) {
	if root == nil {
		return
	}
	if isInertTemplate(root) {
		if root.FirstChild != nil || b.templateContentCache[root] == nil {
			if frag := b.unwrap(b.templateContent(root)); frag != nil {
				b.sealTemplates(frag)
			}
		}
		return
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		b.sealTemplates(c)
	}
}

func isInertTemplate(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "template" && n.Namespace == "" && !hasAttr(n, "shadowrootmode")
}

// cloneTree is cloneNode for trees page code can hold: a deep clone of a
// <template> clones its content fragment too, which the plain node copy cannot
// see once the content has been moved there.
func (b *bridge) cloneTree(n *html.Node, deep bool) *html.Node {
	c := cloneNode(n, false)
	if !deep {
		return c
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.AppendChild(b.cloneTree(ch, true))
	}
	if isInertTemplate(n) {
		if o, ok := b.templateContentCache[n]; ok {
			if src, dst := b.unwrap(o), b.unwrap(b.templateContent(c)); src != nil && dst != nil {
				for ch := src.FirstChild; ch != nil; ch = ch.NextSibling {
					dst.AppendChild(b.cloneTree(ch, true))
				}
			}
		}
	}
	return c
}

// withTemplateContents runs fn with every sealed template under root holding its
// content as children again, which is the shape html.Render serializes; the
// contents go back to their fragments before it returns. Loop goroutine only.
func (b *bridge) withTemplateContents(root *html.Node, fn func()) {
	type moved struct{ tpl, frag *html.Node }
	var undo []moved
	var open func(n *html.Node)
	open = func(n *html.Node) {
		if isInertTemplate(n) {
			if o, ok := b.templateContentCache[n]; ok {
				if frag := b.unwrap(o); frag != nil && frag.FirstChild != nil {
					for frag.FirstChild != nil {
						c := frag.FirstChild
						frag.RemoveChild(c)
						n.AppendChild(c)
					}
					undo = append(undo, moved{n, frag})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			open(c)
		}
	}
	open(root)
	fn()
	for i := len(undo) - 1; i >= 0; i-- {
		for undo[i].tpl.FirstChild != nil {
			c := undo[i].tpl.FirstChild
			undo[i].tpl.RemoveChild(c)
			undo[i].frag.AppendChild(c)
		}
	}
}

// innerHTMLOf and outerHTMLOf serialize as page code sees the tree: a template's
// markup is its content.
func (b *bridge) innerHTMLOf(n *html.Node) (out string) {
	b.withTemplateContents(n, func() { out = innerHTML(n) })
	return out
}

func (b *bridge) outerHTMLOf(n *html.Node) (out string) {
	b.withTemplateContents(n, func() { out = outerHTML(n) })
	return out
}
