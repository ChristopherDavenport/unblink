package js_test

import (
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// evalIn renders a page whose body is bodyHTML and whose trailing classic
// script is the body of a function: its return value (or `THROW <err>`), or
// whatever it later passes to done(v), is the result. head is extra <head>
// markup. It returns the result and the render diagnostics.
func evalIn(t *testing.T, head, bodyHTML, script string, env js.Env) (string, js.RenderResult) {
	t.Helper()
	var diag js.RenderResult
	env.Diag = &diag
	page := `<html><head>` + head + `</head><body>` + bodyHTML + `<pre id="__r"></pre><script>(function(){
  var r = document.getElementById('__r');
  function done(v){ r.setAttribute('data-v', String(v)); }
  window.done = done;
  var v;
  try { v = (function(){` + script + `
  })(); } catch (e) { v = 'THROW ' + e; }
  if (v !== undefined) done(v);
})();</script></body></html>`
	out := renderWithEnv(t, page, env)
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	var res string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "pre" {
			for _, a := range n.Attr {
				if a.Key == "id" && a.Val == "__r" {
					for _, b := range n.Attr {
						if b.Key == "data-v" {
							res = b.Val
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return res, diag
}

// evalJS is evalIn with no head markup and no transport.
func evalJS(t *testing.T, bodyHTML, script string) string {
	t.Helper()
	res, _ := evalIn(t, "", bodyHTML, script, js.Env{})
	return res
}

// wantJS asserts that script evaluates to want.
func wantJS(t *testing.T, bodyHTML, script, want string) {
	t.Helper()
	if got := evalJS(t, bodyHTML, script); got != want {
		t.Errorf("script:\n%s\n got %q\nwant %q", script, got, want)
	}
}
