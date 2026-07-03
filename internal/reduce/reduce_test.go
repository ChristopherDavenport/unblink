package reduce_test

import (
	"fmt"
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

// Every human-hiding channel the heuristic claims to cover is actually
// stripped — and near-miss lookalikes are kept (no false positives).
func TestFullStripHiddenVariants(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		stripped bool
	}{
		{"hidden attribute", `<div hidden>SMUGGLED</div>`, true},
		{"aria-hidden true", `<span aria-hidden="true">SMUGGLED</span>`, true},
		{"visibility hidden", `<p style="visibility: hidden">SMUGGLED</p>`, true},
		{"offscreen left", `<p style="position:absolute; left: -9999px">SMUGGLED</p>`, true},
		{"offscreen em", `<p style="text-indent:-999em">SMUGGLED</p>`, true},
		{"clip rect", `<p style="clip: rect(0,0,0,0);position:absolute">SMUGGLED</p>`, true},
		{"aria-hidden false is visible", `<span aria-hidden="false">SMUGGLED</span>`, false},
		{"ordinary positioning is visible", `<p style="position:absolute; left: 10px">SMUGGLED</p>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPage(t, `<p>Visible anchor paragraph.</p>`+tc.body)
			if err := reduce.Full(p, true); err != nil {
				t.Fatalf("Full: %v", err)
			}
			got := strings.Contains(p.Article.ContentHTML, "SMUGGLED")
			if got == tc.stripped {
				t.Errorf("stripped=%v, want %v:\n%s", !got, tc.stripped, p.Article.ContentHTML)
			}
		})
	}
}

// Full emits a page's repeated boilerplate region (desktop nav + mobile-drawer
// twin) only once, while distinct content is untouched.
func TestFullStripsDuplicateBlocks(t *testing.T) {
	var nav strings.Builder
	nav.WriteString(`<nav><p>NAVDUP menu covering world, politics, sports and business coverage.</p>`)
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&nav, `<a href="/hub/%d">Hub %d with a reasonably long section label</a> `, i, i)
	}
	nav.WriteString(`</nav>`)

	p := newPage(t, `<header class="desktop">`+nav.String()+`</header>`+
		`<main><p>Unique page content.</p></main>`+
		`<div class="mobile-drawer">`+nav.String()+`</div>`)
	if err := reduce.Full(p, true); err != nil {
		t.Fatalf("Full: %v", err)
	}
	if got := strings.Count(p.Article.ContentHTML, "NAVDUP menu"); got != 1 {
		t.Errorf("duplicated nav should be emitted once, got %d:\n%s", got, p.Article.ContentHTML)
	}
	if !strings.Contains(p.Article.ContentHTML, "Unique page content.") {
		t.Errorf("unique content lost:\n%s", p.Article.ContentHTML)
	}
}

// Article picks the main prose over boilerplate and records Source
// "readability"; a contentless SPA shell falls back to Full and says so.
func TestArticleSourceReporting(t *testing.T) {
	long := strings.Repeat("Substantial article prose that readability should keep, with detail. ", 20)
	article := newPage(t, `<nav><a href="/">Home</a><a href="/about">About</a></nav>`+
		`<article><h1>Real Story</h1><p>`+long+`</p><p>`+long+`</p></article>`+
		`<footer>Copyright boilerplate footer</footer>`)
	if err := reduce.Article(article, true); err != nil {
		t.Fatalf("Article: %v", err)
	}
	if article.Article.Source != "readability" {
		t.Errorf("Source = %q, want readability", article.Article.Source)
	}
	if !strings.Contains(article.Article.ContentHTML, "Substantial article prose") {
		t.Errorf("article body lost:\n%.300s", article.Article.ContentHTML)
	}

	shell := newPage(t, `<div id="app"></div><script src="/bundle.js"></script>`)
	if err := reduce.Article(shell, true); err != nil {
		t.Fatalf("Article(shell): %v", err)
	}
	if shell.Article.Source != "full" {
		t.Errorf("SPA shell Source = %q, want full (fallback must be reported)", shell.Article.Source)
	}
}

// articleAround wraps an injected block in enough real prose that readability
// keeps it as the main content (asserted via Source), so the hidden-strip is
// exercised on the article path — not silently on the Full fallback.
func articleAround(inject string) string {
	prose := strings.Repeat("Substantial article prose that readability keeps as the dominant content region. ", 20)
	return `<nav><a href="/">Home</a><a href="/about">About</a></nav>` +
		`<article><h1>Real Story</h1>` +
		`<p>` + prose + `</p>` +
		inject +
		`<p>` + prose + `</p></article>` +
		`<footer>Copyright boilerplate footer</footer>`
}

// The article path strips every human-hiding channel — the regression the
// crossbench content-boundary probe surfaced was off-screen text surviving
// readability (which drops the inline style, erasing the signal a post-
// extraction strip relied on). Each case must run on the readability path.
func TestArticleStripHiddenVariants(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		stripped bool
	}{
		{"display none", `<div style="display:none">SMUGGLED-INJECT</div>`, true},
		{"hidden attribute", `<div hidden>SMUGGLED-INJECT</div>`, true},
		{"aria-hidden true", `<span aria-hidden="true">SMUGGLED-INJECT</span>`, true},
		{"visibility hidden", `<p style="visibility: hidden">SMUGGLED-INJECT</p>`, true},
		{"offscreen left", `<p style="position:absolute; left: -9999px">SMUGGLED-INJECT</p>`, true},
		{"offscreen top", `<div style="position:absolute;left:-9999px;top:-9999px">SMUGGLED-INJECT</div>`, true},
		{"offscreen em", `<p style="text-indent:-999em">SMUGGLED-INJECT</p>`, true},
		{"clip rect", `<p style="clip: rect(0,0,0,0);position:absolute">SMUGGLED-INJECT</p>`, true},
		{"ordinary positioning is visible", `<p style="position:absolute; left: 10px">SMUGGLED-INJECT</p>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPage(t, articleAround(tc.body))
			if err := reduce.Article(p, true); err != nil {
				t.Fatalf("Article: %v", err)
			}
			if p.Article.Source != "readability" {
				t.Fatalf("Source = %q, want readability (test must exercise the article path, not the Full fallback)", p.Article.Source)
			}
			leaked := strings.Contains(p.Article.ContentHTML, "SMUGGLED-INJECT")
			if leaked == tc.stripped {
				t.Errorf("leaked=%v, want stripped=%v:\n%s", leaked, tc.stripped, p.Article.ContentHTML)
			}
		})
	}
}

// The stripHidden opt-out leaves hidden text in the article output, confirming
// the pre-extraction strip is gated on the flag (parity with Full).
func TestArticleStripHiddenOptOut(t *testing.T) {
	p := newPage(t, articleAround(`<div style="position:absolute;left:-9999px">SMUGGLED-INJECT</div>`))
	if err := reduce.Article(p, false); err != nil {
		t.Fatalf("Article: %v", err)
	}
	if !strings.Contains(p.Article.ContentHTML, "SMUGGLED-INJECT") {
		t.Errorf("stripHidden=false should retain hidden text:\n%s", p.Article.ContentHTML)
	}
}
