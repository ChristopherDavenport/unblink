package js

import (
	"strings"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// innerText / outerText (HTML Standard, "the innerText and outerText
// properties").
//
// The standard defines the getter over the *rendered* text, which needs CSS
// boxes this engine does not have (no layout, no cascade — ADR 0007). What is
// implemented is the same algorithm with "being rendered" and "block-level"
// decided from the node tree alone:
//
//   - not rendered: <script>/<style>/<template>/<noscript>/<head> and its
//     metadata children, anything with the `hidden` attribute, and anything whose
//     own inline style says display:none. A stylesheet's display:none is NOT
//     seen — such text is included, where a browser would drop it.
//   - block-level: the elements the HTML rendering section gives display:block
//     (or list-item/table-row) by default, chosen by tag name. A <div> restyled
//     inline, or a <span> restyled block, is not detected.
//   - white-space: collapsed everywhere except inside <pre>/<textarea>/<listing>/
//     <plaintext>/<xmp> (and an inline white-space:pre*). text-transform is not
//     applied.
//
// So the result is the standard's for unstyled markup and an approximation
// wherever a stylesheet changes display or white-space. The setter is exact.

// blockTags are elements whose default display puts them on their own line.
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "body": true,
	"center": true, "dd": true, "details": true, "dialog": true, "dir": true, "div": true,
	"dl": true, "dt": true, "fieldset": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hgroup": true, "hr": true, "html": true,
	"legend": true, "li": true, "listing": true, "main": true, "menu": true, "nav": true,
	"ol": true, "optgroup": true, "option": true, "p": true, "plaintext": true, "pre": true,
	"search": true, "section": true, "summary": true, "table": true, "tbody": true,
	"tfoot": true, "thead": true, "tr": true, "caption": true, "ul": true, "xmp": true,
}

// unrenderedTags never produce rendered text.
var unrenderedTags = map[string]bool{
	"script": true, "style": true, "template": true, "noscript": true, "head": true,
	"title": true, "meta": true, "link": true, "base": true, "datalist": true,
	"noframes": true, "noembed": true, "iframe": true, "object": true, "canvas": true,
	"audio": true, "video": true, "area": true, "param": true, "source": true, "track": true,
}

// preTags preserve white space.
var preTags = map[string]bool{"pre": true, "textarea": true, "listing": true, "plaintext": true, "xmp": true}

// inlineStyleDecl returns the value of one declaration in n's inline style
// attribute, lowercased and trimmed ("" when absent).
func inlineStyleDecl(n *html.Node, prop string) string {
	style := getAttr(n, "style")
	if style == "" {
		return ""
	}
	val := ""
	for _, decl := range strings.Split(style, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok || strings.ToLower(strings.TrimSpace(k)) != prop {
			continue
		}
		v = strings.ToLower(strings.TrimSpace(v))
		v = strings.TrimSpace(strings.TrimSuffix(v, "!important"))
		val = v // the last declaration wins
	}
	return val
}

// notRendered reports whether element n (and so its subtree) produces no
// rendered text, as far as the node tree can tell.
func notRendered(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if unrenderedTags[n.Data] || hasAttr(n, "hidden") {
		return true
	}
	return inlineStyleDecl(n, "display") == "none"
}

// textItems accumulates the standard's "items": strings and required line
// break counts.
type textItems struct {
	parts []textItem
}

type textItem struct {
	text string
	brk  int // > 0: a required line break count instead of text
}

func (t *textItems) lineBreak(count int) {
	// Collapsible white space at the end of a line is dropped.
	if k := len(t.parts); k > 0 && t.parts[k-1].brk == 0 {
		t.parts[k-1].text = strings.TrimRight(t.parts[k-1].text, " ")
		if t.parts[k-1].text == "" {
			t.parts = t.parts[:k-1]
		}
	}
	t.parts = append(t.parts, textItem{brk: count})
}

// atLineStart reports whether the next collapsed text would start a line (so
// its leading white space is dropped) or follow text that already ends in a
// space.
func (t *textItems) atLineStart() bool {
	k := len(t.parts)
	if k == 0 || t.parts[k-1].brk > 0 {
		return true
	}
	return strings.HasSuffix(t.parts[k-1].text, " ") || strings.HasSuffix(t.parts[k-1].text, "\n")
}

func (t *textItems) text(s string) {
	if s == "" {
		return
	}
	if k := len(t.parts); k > 0 && t.parts[k-1].brk == 0 {
		t.parts[k-1].text += s
		return
	}
	t.parts = append(t.parts, textItem{text: s})
}

// collapseSpace applies white-space:normal collapsing to s: every run of
// ASCII white space becomes one space.
func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

func (t *textItems) collect(n *html.Node, pre bool) {
	switch n.Type {
	case html.TextNode:
		if pre {
			t.text(strings.ReplaceAll(n.Data, "\r\n", "\n"))
			return
		}
		s := collapseSpace(n.Data)
		if t.atLineStart() {
			s = strings.TrimLeft(s, " ")
		}
		t.text(s)
		return
	case html.ElementNode:
	default:
		return
	}
	if notRendered(n) {
		return
	}
	switch n.Data {
	case "br":
		t.lineBreakText()
		return
	case "select":
		// A <select> renders its options, each on a line.
	}
	if ws := inlineStyleDecl(n, "white-space"); ws != "" {
		pre = strings.HasPrefix(ws, "pre") || ws == "break-spaces"
	} else if preTags[n.Data] {
		pre = true
	}
	block := blockTags[n.Data]
	switch inlineStyleDecl(n, "display") {
	case "block", "flex", "grid", "list-item", "table", "flow-root":
		block = true
	case "inline", "inline-block", "inline-flex", "contents":
		block = false
	}
	para := n.Data == "p"
	switch {
	case para:
		t.lineBreak(2)
	case block:
		t.lineBreak(1)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		t.collect(c, pre)
		// Table cells are separated by a tab, rows by a line break (the row is
		// block-level above).
		if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") && nextCell(c) != nil {
			t.text("\t")
		}
	}
	switch {
	case para:
		t.lineBreak(2)
	case block:
		t.lineBreak(1)
	}
}

// lineBreakText appends the literal "\n" a <br> contributes (text, not a
// required line break: consecutive <br>s do not collapse).
func (t *textItems) lineBreakText() {
	if k := len(t.parts); k > 0 && t.parts[k-1].brk == 0 {
		t.parts[k-1].text = strings.TrimRight(t.parts[k-1].text, " ") + "\n"
		return
	}
	t.parts = append(t.parts, textItem{text: "\n"})
}

func nextCell(n *html.Node) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode && (s.Data == "td" || s.Data == "th") {
			return s
		}
	}
	return nil
}

// innerText computes the innerText getter for element n.
func innerText(n *html.Node) string {
	// "If this element is not being rendered, return the same value as textContent."
	for a := n; a != nil; a = a.Parent {
		if notRendered(a) {
			return textContent(n)
		}
	}
	pre := false
	for a := n; a != nil; a = a.Parent {
		if a.Type == html.ElementNode && preTags[a.Data] {
			pre = true
			break
		}
	}
	var t textItems
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		t.collect(c, pre)
		if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") && nextCell(c) != nil {
			t.text("\t")
		}
	}
	// Trailing collapsible space at the very end.
	if k := len(t.parts); k > 0 && t.parts[k-1].brk == 0 && !pre {
		t.parts[k-1].text = strings.TrimRight(t.parts[k-1].text, " ")
	}
	// Drop empty strings, then leading/trailing break counts, then turn each run
	// of counts into that many newlines (the maximum of the run).
	items := t.parts[:0]
	for _, p := range t.parts {
		if p.brk == 0 && p.text == "" {
			continue
		}
		items = append(items, p)
	}
	for len(items) > 0 && items[0].brk > 0 {
		items = items[1:]
	}
	for len(items) > 0 && items[len(items)-1].brk > 0 {
		items = items[:len(items)-1]
	}
	var b strings.Builder
	for i := 0; i < len(items); i++ {
		if items[i].brk == 0 {
			b.WriteString(items[i].text)
			continue
		}
		maxBrk := items[i].brk
		for i+1 < len(items) && items[i+1].brk > 0 {
			i++
			if items[i].brk > maxBrk {
				maxBrk = items[i].brk
			}
		}
		b.WriteString(strings.Repeat("\n", maxBrk))
	}
	return b.String()
}

// renderedTextFragment builds the nodes the innerText/outerText setters insert:
// the text with each line break (LF, CR, or CRLF) replaced by a <br>.
func renderedTextFragment(s string) []*html.Node {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var out []*html.Node
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			out = append(out, &html.Node{Type: html.ElementNode, Data: "br", DataAtom: atom.Br})
		}
		if line != "" {
			out = append(out, &html.Node{Type: html.TextNode, Data: line})
		}
	}
	return out
}

// installInnerText adds innerText and outerText to HTMLElement.prototype.
func (b *bridge) installInnerText(p *goja.Object) {
	vm := b.vm
	b.protoProp(p, "innerText",
		func(n *html.Node) goja.Value { return vm.ToValue(innerText(n)) },
		func(n *html.Node, v goja.Value) {
			var removed []*html.Node
			for n.FirstChild != nil {
				c := n.FirstChild
				n.RemoveChild(c)
				removed = append(removed, c)
			}
			added := renderedTextFragment(nullToEmptyString(v))
			for _, c := range added {
				n.AppendChild(c)
			}
			b.onMutate(mutationRecord{typ: "childList", target: n, added: added, removed: removed})
		})
	b.protoProp(p, "outerText",
		func(n *html.Node) goja.Value { return vm.ToValue(innerText(n)) },
		func(n *html.Node, v goja.Value) {
			parent := n.Parent
			if parent == nil {
				b.throwDOMException("NoModificationAllowedError", "The element has no parent.")
				return
			}
			added := renderedTextFragment(nullToEmptyString(v))
			if len(added) == 0 {
				added = []*html.Node{{Type: html.TextNode, Data: ""}}
			}
			for _, c := range added {
				parent.InsertBefore(c, n)
			}
			parent.RemoveChild(n)
			b.onMutate(mutationRecord{typ: "childList", target: parent, added: added, removed: []*html.Node{n}})
		})
}
