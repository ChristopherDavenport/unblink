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

const cosmeticPage = `<html><body>
	<div class="ad-banner">AD-BANNER-TEXT</div>
	<div id="sponsored">SPONSORED-TEXT</div>
	<div class="promo">PROMO-TEXT</div>
	<div id="dynamic-ad">DYNAMIC-AD-TEXT</div>
	<div id="cs-marker">original</div>
	<article id="content">REAL-CONTENT</article>
</body></html>`

func renderWithExtension(t *testing.T, pageHTML, baseURL string, bundle *webext.Bundle) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(baseURL)
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return doc
}

func TestContentScriptAndCosmeticFiltering(t *testing.T) {
	bundle, err := webext.Load("../webext/testdata/cosmetic-mini")
	if err != nil {
		t.Fatal(err)
	}

	// On a matching host, content-script JS runs and cosmetic CSS strips ad markup —
	// even though the page has no <script> of its own.
	doc := renderWithExtension(t, cosmeticPage, "https://www.example.com/page", bundle)
	if got := divText(t, doc, "cs-marker"); got != "Cosmetic Mini" {
		t.Errorf("#cs-marker = %q, want Cosmetic Mini (content-script JS + i18n)", got)
	}
	if got := elemAttr(t, doc, "cs-marker", "data-ext"); got != bundle.ID {
		t.Errorf("#cs-marker data-ext = %q, want the extension id %q", got, bundle.ID)
	}
	out := serialize(t, doc)
	for _, gone := range []string{"AD-BANNER-TEXT", "SPONSORED-TEXT", "PROMO-TEXT", "DYNAMIC-AD-TEXT"} {
		if strings.Contains(out, gone) {
			t.Errorf("expected %q to be removed:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "REAL-CONTENT") {
		t.Errorf("real content should survive:\n%s", out)
	}

	// On an excluded host, the content script does not run and nothing is stripped.
	doc2 := renderWithExtension(t, cosmeticPage, "https://noads.example.com/page", bundle)
	if got := divText(t, doc2, "cs-marker"); got != "original" {
		t.Errorf("#cs-marker on excluded host = %q, want unchanged 'original'", got)
	}
	if !strings.Contains(serialize(t, doc2), "AD-BANNER-TEXT") {
		t.Error("ad markup should survive on an excluded host")
	}
}

func serialize(t *testing.T, doc *html.Node) string {
	t.Helper()
	var b strings.Builder
	if err := html.Render(&b, doc); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func elemAttr(t *testing.T, doc *html.Node, id, attr string) string {
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
		t.Fatalf("#%s not found", id)
	}
	for _, a := range el.Attr {
		if a.Key == attr {
			return a.Val
		}
	}
	return ""
}
