package dom_test

import (
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/dom"
)

// sepText parses a body fragment, applies the rule and returns the body's text.
func sepText(t *testing.T, body string) string {
	t.Helper()
	doc := parse(t, `<html><body>`+body+`</body></html>`)
	dom.SeparateInlineSiblings(doc)
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}

func TestSeparateInlineSiblings(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// Pieces laid out apart by CSS: separated.
		{"grid labels", `<div><span>Jan</span><span>Mar</span><span>May</span></div>`, "Jan Mar May"},
		{"number then title in a link", `<a href="/x"><span>01</span><span>Getting Started</span></a>`, "01 Getting Started"},
		{"adjacent links", `<nav><a href="/a">Docs</a><a href="/b">Blog</a></nav>`, "Docs Blog"},
		{"through an empty icon", `<p><span>v5</span><span class="icon"></span><span>latest</span></p>`, "v5 latest"},
		{"after closing punctuation", `<p><span>Size:</span><span>large</span></p>`, "Size: large"},
		{"custom elements", `<div><x-tag>new</x-tag><x-tag>beta</x-tag></div>`, "new beta"},
		{"nested", `<div><span><b>GitHub stars</b></span><span><b>234 contributors</b></span></div>`, "GitHub stars 234 contributors"},

		{"table cells, even digits", `<table><tr><td>12</td><td>34</td><th>x</th></tr></table>`, "12 34 x"},

		// One word, or one expression, styled in parts: untouched.
		{"text on one side", `<p>un<b>believ</b>able</p>`, "unbelievable"},
		{"already spaced", `<p><span>a</span> <span>b</span></p>`, "a b"},
		{"drop cap", `<p><span>T</span><span>he start</span></p>`, "The start"},
		{"letter animation", `<h1><span>H</span><span>e</span><span>y</span></h1>`, "Hey"},
		{"digit groups", `<p><span>36</span><span>939</span></p>`, "36939"},
		{"decimal in parts", `<p><span>3</span><span>.</span><span>5</span></p>`, "3.5"},
		{"closing punctuation", `<p><span>done</span><span>.</span></p>`, "done."},
		{"opening punctuation", `<p><span>$</span><span>10</span></p>`, "$10"},
		{"parenthesised", `<p><span>(</span><span>beta</span><span>)</span></p>`, "(beta)"},
		{"cjk", `<p><span>日本</span><span>語</span></p>`, "日本語"},
		{"sup is not a piece", `<p><span>x</span><sup>2</sup></p>`, "x2"},

		// Code and typeset maths: tokens of one expression.
		{"pre", `<pre><span>foo</span><span>bar</span></pre>`, "foobar"},
		{"code", `<p><code><span>a</span><span>b</span></code></p>`, "ab"},
		{"highlighted div", `<div class="shiki"><span>const</span><span>x</span></div>`, "constx"},
		{"katex", `<span class="katex"><span>a</span><span>b</span></span>`, "ab"},
		{"ruby", `<ruby><span>漢</span><span>字</span></ruby>`, "漢字"},
		{"svg", `<svg><text><tspan>ab</tspan><tspan>cd</tspan></text></svg>`, "abcd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sepText(t, c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSeparateInlineSiblingsNil(t *testing.T) {
	dom.SeparateInlineSiblings(nil) // must not panic
}
