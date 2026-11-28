package dom

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// SeparateInlineSiblings inserts a single space between adjacent inline element
// siblings whose text would otherwise run together in the emitted Markdown:
//
//	<span>Jan</span><span>Mar</span>            -> "Jan Mar", not "JanMar"
//	<a><span>01</span><span>Intro</span></a>    -> "01 Intro"
//
// Such markup is laid out by CSS (flex or grid items, margins, inline-blocks), so
// a browser shows the pieces apart and its innerText separates them, while a
// serializer that only knows tags concatenates them. unblink has no layout, so
// the rule is structural: two inline-level elements that touch — no text node,
// not even whitespace, between them — are separate pieces of text, not one word.
// A word styled in parts has text on at least one side of an element
// (un<b>believ</b>able), which this never matches.
//
// It is deliberately conservative about what it separates:
//
//   - nothing inside pre/code/kbd/samp/var, ruby, SVG, MathML, or a subtree whose
//     class names it as highlighted code or typeset maths, where touching spans
//     are the tokens of one expression;
//   - not across a boundary where either side is CJK or Thai text, which is
//     written without spaces;
//   - not digit-to-digit (a number rendered digit group by digit group), not
//     before closing or after opening punctuation, and not after a lone letter
//     (a drop cap, or a word animated letter by letter).
//
// Callers must pass a tree they own (a reduce clone): it inserts text nodes.
func SeparateInlineSiblings(root *html.Node) {
	if root == nil {
		return
	}
	separateInline(root)
}

func separateInline(n *html.Node) {
	if n.Type == html.ElementNode && sepOpaque(n) {
		return
	}
	var prev *html.Node // previous element sibling carrying text, with no text node since
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			if c.Data != "" {
				prev = nil
			}
			continue
		case html.ElementNode:
		default:
			continue
		}
		separateInline(c)
		if !sepInline(c) {
			prev = nil
			continue
		}
		first, ok := firstTextRune(c)
		if !ok {
			continue // empty element (an icon): look through it
		}
		if prev != nil {
			last, _ := lastTextRune(prev)
			if needsSpace(last, first) && !loneLetter(prev) {
				n.InsertBefore(&html.Node{Type: html.TextNode, Data: " "}, c)
			}
		}
		prev = c
	}
}

// needsSpace decides from the two runes that would touch.
func needsSpace(last, first rune) bool {
	if unicode.IsSpace(last) || unicode.IsSpace(first) {
		return false
	}
	if isCJK(last) || isCJK(first) {
		return false
	}
	wordish := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	if !wordish(last) && !strings.ContainsRune(".,:;!?)]}%", last) {
		return false // "(" "$" "#" "@" "/" "-" … bind to what follows
	}
	if !wordish(first) && !strings.ContainsRune("([{$#@", first) {
		return false // closing punctuation binds to what precedes
	}
	if unicode.IsDigit(first) && (unicode.IsDigit(last) || strings.ContainsRune(".,:", last)) {
		return false // 36 939, 3 . 5, 12 : 30 rendered piece by piece
	}
	return true
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Thai)
}

// sepOpaque: subtrees where adjacency is meaningful and must not be touched.
func sepOpaque(n *html.Node) bool {
	if n.Namespace != "" {
		return true // svg, math
	}
	switch n.Data {
	case "pre", "code", "kbd", "samp", "var", "tt", "ruby", "script", "style", "textarea", "select", "math", "svg", "template":
		return true
	}
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, c := range strings.Fields(a.Val) {
			switch {
			case c == "katex", c == "MathJax", c == "hljs", c == "shiki", c == "highlight",
				c == "CodeMirror", c == "cm-line", c == "token-line", c == "monaco-editor",
				strings.HasPrefix(c, "language-"), strings.HasPrefix(c, "prism-"):
				return true
			}
		}
	}
	return false
}

func sepInline(n *html.Node) bool {
	if n.Namespace != "" {
		return false
	}
	switch n.Data {
	case "a", "abbr", "b", "bdi", "bdo", "button", "cite", "data", "dfn", "em", "i",
		"label", "mark", "q", "s", "small", "span", "strong", "time", "u", "ins", "del", "font":
		return true
	}
	return strings.Contains(n.Data, "-") // custom elements are inline unless styled
}

// loneLetter reports whether n's text is a single letter: a drop cap, or one
// letter of a word animated letter by letter.
func loneLetter(n *html.Node) bool {
	var count int
	var only rune
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.TextNode {
			for _, r := range n.Data {
				if unicode.IsSpace(r) {
					continue
				}
				count++
				only = r
				if count > 1 {
					return false
				}
			}
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if !walk(c) {
				return false
			}
		}
		return true
	}
	return walk(n) && count == 1 && unicode.IsLetter(only)
}

func firstTextRune(n *html.Node) (rune, bool) {
	if n.Type == html.TextNode {
		if n.Data == "" {
			return 0, false
		}
		r, _ := utf8.DecodeRuneInString(n.Data)
		return r, true
	}
	if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "template") {
		return 0, false
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r, ok := firstTextRune(c); ok {
			return r, true
		}
	}
	return 0, false
}

func lastTextRune(n *html.Node) (rune, bool) {
	if n.Type == html.TextNode {
		if n.Data == "" {
			return 0, false
		}
		r, _ := utf8.DecodeLastRuneInString(n.Data)
		return r, true
	}
	if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "template") {
		return 0, false
	}
	for c := n.LastChild; c != nil; c = c.PrevSibling {
		if r, ok := lastTextRune(c); ok {
			return r, true
		}
	}
	return 0, false
}
