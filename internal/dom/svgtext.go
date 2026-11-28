package dom

import (
	"strings"

	"golang.org/x/net/html"
)

// svgLabelSep joins the labels of one SVG. A chart's labels are positioned, not
// flowed, so they have no reading-order separator of their own; a middle dot
// keeps "Angular 1 · Angular 2 · Elm" from reading as one phrase.
const svgLabelSep = " · "

// FlattenSVGText replaces every top-level <svg> in the tree with the text a
// reader gets from it: the contents of its <text> elements (chart axis and data
// labels), its <title>s and <desc>s, in document order. Labels are all kept; a
// title or description that only repeats one is dropped.
//
// An SVG's markup is drawing instructions; only those three elements carry
// words. Left to a tag-stripping serializer, sibling <text> elements — each
// placed by coordinates — run together ("EmberReactAngular 1Angular 2"). Here
// each becomes its own label. <tspan>s of one <text> are parts of one label and
// are joined with spaces. An SVG that carries labels becomes a paragraph. One
// that only has a <title> is an icon: the title is kept, inline, when it is the
// only name of the link or button the icon sits in, and dropped otherwise — as
// are SVGs with no words at all.
//
// Callers must pass a tree they own (a reduce clone): it replaces nodes.
func FlattenSVGText(root *html.Node) {
	if root == nil {
		return
	}
	var next *html.Node
	for c := root.FirstChild; c != nil; c = next {
		next = c.NextSibling
		if c.Type != html.ElementNode {
			continue
		}
		if c.Namespace == "svg" && c.Data == "svg" {
			replaceSVG(root, c)
			continue
		}
		FlattenSVGText(c)
	}
}

func replaceSVG(parent, svg *html.Node) {
	type piece struct {
		s     string
		label bool // from <text> or <foreignObject>: always kept
	}
	var pieces []piece
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			if c.Namespace != "svg" {
				// <foreignObject> content is ordinary HTML: keep its text as a label.
				if s := labelText(c); s != "" {
					pieces = append(pieces, piece{s, true})
				}
				continue
			}
			switch c.Data {
			case "style", "script", "defs", "metadata":
				continue
			case "text", "title", "desc":
				if s := labelText(c); s != "" {
					pieces = append(pieces, piece{s, c.Data == "text"})
				}
				if c.Data != "text" {
					continue
				}
			}
			walk(c)
		}
	}
	walk(svg)

	// Every label is kept, repeats included: two bars can both read "100". A
	// <title> or <desc> is a description of the drawing or of one shape, and is
	// dropped when it only repeats a label or an earlier description.
	seen := map[string]bool{}
	hasText := false
	for _, p := range pieces {
		if p.label {
			seen[p.s] = true
			hasText = true
		}
	}
	var labels []string
	for _, p := range pieces {
		if !p.label {
			if seen[p.s] {
				continue
			}
			seen[p.s] = true
		}
		labels = append(labels, p.s)
	}

	// An SVG with no labels is a picture. Its <title> is its accessible name,
	// which matters only where the picture is all a control has to say for
	// itself — an icon-only link or button. Beside visible text ("[icon] 43
	// comments") the name repeats or decorates it, and a browser shows neither.
	if !hasText && !namesItsControl(svg) {
		labels = nil
	}
	if len(labels) == 0 {
		parent.RemoveChild(svg)
		return
	}
	tag := "span"
	if hasText {
		tag = "p"
	}
	repl := &html.Node{Type: html.ElementNode, Data: tag}
	repl.AppendChild(&html.Node{Type: html.TextNode, Data: strings.Join(labels, svgLabelSep)})
	parent.InsertBefore(repl, svg)
	parent.RemoveChild(svg)
}

// namesItsControl reports whether svg sits in a link, button or other control
// that has no text of its own, so the SVG's title is the control's only name.
func namesItsControl(svg *html.Node) bool {
	var control *html.Node
	for p := svg.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode || p.Namespace != "" {
			continue
		}
		switch p.Data {
		case "a", "button", "summary", "label":
			control = p
		}
		if control != nil {
			break
		}
	}
	if control == nil {
		return false
	}
	return !hasTextOutsideSVG(control)
}

// hasTextOutsideSVG reports whether n contains visible text or an image with alt text
// outside any SVG.
func hasTextOutsideSVG(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			if strings.TrimSpace(c.Data) != "" {
				return true
			}
		case html.ElementNode:
			if c.Namespace != "" {
				continue
			}
			if c.Data == "img" && strings.TrimSpace(attr(c, "alt")) != "" {
				return true
			}
			if c.Data == "script" || c.Data == "style" {
				continue
			}
			if hasTextOutsideSVG(c) {
				return true
			}
		}
	}
	return false
}

// labelText is n's text with each child element's text a separate word and
// whitespace collapsed.
func labelText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.TextNode:
				b.WriteString(c.Data)
			case html.ElementNode:
				if c.Data == "title" || c.Data == "desc" || c.Data == "style" || c.Data == "script" {
					continue // a label's own tooltip is collected separately
				}
				b.WriteByte(' ')
				walk(c)
				b.WriteByte(' ')
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
