package reduce_test

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/reduce"
)

func newPage(t *testing.T, body string) *page.Page {
	t.Helper()
	doc, err := html.Parse(strings.NewReader("<html><body>" + body + "</body></html>"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	u, _ := url.Parse("https://example.com/")
	return &page.Page{Doc: doc, FinalURL: u}
}

func TestFullStripHidden(t *testing.T) {
	body := `<p>Visible paragraph.</p>
		<div style="display:none">INJECT-hidden-instruction</div>
		<!-- INJECT-comment -->`

	strip := newPage(t, body)
	if err := reduce.Full(strip, true); err != nil {
		t.Fatalf("Full(strip): %v", err)
	}
	if !strings.Contains(strip.Article.ContentHTML, "Visible paragraph.") {
		t.Errorf("visible content lost:\n%s", strip.Article.ContentHTML)
	}
	if strings.Contains(strip.Article.ContentHTML, "INJECT-hidden-instruction") {
		t.Errorf("hidden text survived stripHidden:\n%s", strip.Article.ContentHTML)
	}
	if strings.Contains(strip.Article.ContentHTML, "INJECT-comment") {
		t.Errorf("comment survived stripHidden:\n%s", strip.Article.ContentHTML)
	}

	// With stripHidden=false, the hidden text passes through (bluemonday keeps tag
	// text), confirming the opt-out restores prior behavior.
	keep := newPage(t, body)
	if err := reduce.Full(keep, false); err != nil {
		t.Fatalf("Full(keep): %v", err)
	}
	if !strings.Contains(keep.Article.ContentHTML, "INJECT-hidden-instruction") {
		t.Errorf("stripHidden=false should retain hidden text:\n%s", keep.Article.ContentHTML)
	}
}
