package js_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/webext"
)

// TestExtensionBlocksFetch drives a full render with a loaded extension and asserts a
// page-JS fetch to a rule-matched host is cancelled end-to-end (never reaches the
// transport), while an unrelated fetch still succeeds.
func TestExtensionBlocksFetch(t *testing.T) {
	rules, err := webext.ParseRules([]byte(`[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"||ads.example.com^","resourceTypes":["xmlhttprequest"]}}]`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := webext.NewRuleMatcher(rules)
	if err != nil {
		t.Fatal(err)
	}
	bundle := &webext.Bundle{Net: m}

	// The #done marker text comes only from the fetched body / the catch handler, so it
	// reflects actual runtime behavior — unlike the fetch() argument strings, which
	// survive in the serialized <script> and would defeat a naive substring check.
	doc, err := html.Parse(strings.NewReader(`<html><body>
		<div id="ok">pending</div><div id="bad">pending</div>
		<script>
		  fetch('https://ok.example.com/data')
		    .then(function(r){ return r.text(); })
		    .then(function(t){ document.getElementById('ok').textContent = t; });
		  fetch('https://ads.example.com/beacon')
		    .then(function(){ document.getElementById('bad').textContent = 'network-reached'; })
		    .catch(function(){ document.getElementById('bad').textContent = 'cancelled'; });
		</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.com/page")
	tr := &stubTransport{routes: map[string]string{"ok.example.com": "ALLOWED-OK"}}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
		t.Fatalf("render: %v", err)
	}

	// The blocked request never reaches the inner transport: only the allowed fetch does.
	if tr.count != 1 {
		t.Errorf("inner transport saw %d requests, want 1 (the blocked request must not reach the network)", tr.count)
	}
	// Read the resulting DOM to confirm each fetch's observed outcome.
	if got := divText(t, doc, "ok"); got != "ALLOWED-OK" {
		t.Errorf("#ok = %q, want the fetched body ALLOWED-OK", got)
	}
	if got := divText(t, doc, "bad"); got != "cancelled" {
		t.Errorf("#bad = %q, want cancelled (the blocked fetch's catch handler ran)", got)
	}
}

// divText returns the text content of the element with the given id.
func divText(t *testing.T, doc *html.Node, id string) string {
	t.Helper()
	var find func(*html.Node) *html.Node
	find = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "id" && a.Val == id {
					return n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if r := find(c); r != nil {
				return r
			}
		}
		return nil
	}
	el := find(doc)
	if el == nil {
		t.Fatalf("element #%s not found", id)
	}
	var sb strings.Builder
	for c := el.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return sb.String()
}
