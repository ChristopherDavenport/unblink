package reduce

// In-package benchmarks — a deliberate deviation from the external _test
// convention: BenchmarkSanitize needs the unexported sanitizeNode to isolate
// the per-call sanitizer-policy construction from the rest of the reduction.

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

func benchPage(b *testing.B, src string) *page.Page {
	b.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	u, _ := url.Parse("https://example.com/post/1")
	return &page.Page{Doc: doc, FinalURL: u}
}

func loadLongread(b *testing.B) string {
	b.Helper()
	data, err := os.ReadFile("../../eval/corpus/longread.input.html")
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	return string(data)
}

// syntheticLargePage builds a big, article-shaped page (the eval fixtures are
// small): ~2,000 paragraphs with periodic links inside an <article>, wrapped in
// duplicated link-dense nav blocks so full-mode dedupe has work to do.
func syntheticLargePage() string {
	var sb strings.Builder
	sb.WriteString("<html><head><title>Large synthetic page</title></head><body>")
	nav := func() {
		sb.WriteString(`<nav><ul>`)
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&sb, `<li><a href="/section/%d">Section %d navigation entry</a></li>`, i, i)
		}
		sb.WriteString(`</ul></nav>`)
	}
	nav()
	sb.WriteString(`<article><h1>Large synthetic page</h1>`)
	for i := 0; i < 2000; i++ {
		if i%10 == 0 {
			fmt.Fprintf(&sb, `<p>Paragraph %d has some body text and a <a href="/ref/%d">reference link</a> plus enough words to look like real prose content for scoring purposes.</p>`, i, i)
		} else {
			fmt.Fprintf(&sb, `<p>Paragraph %d carries plain sentence content with enough length that readability treats the block as genuine article prose.</p>`, i)
		}
	}
	sb.WriteString(`</article>`)
	nav() // the mobile-drawer twin: a verbatim duplicate for StripDuplicateBlocks
	sb.WriteString("</body></html>")
	return sb.String()
}

func benchArticle(b *testing.B, src string) {
	b.Helper()
	p := benchPage(b, src)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Article(p, true); err != nil {
			b.Fatalf("article: %v", err)
		}
	}
}

func benchFull(b *testing.B, src string) {
	b.Helper()
	p := benchPage(b, src)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Full(p, true); err != nil {
			b.Fatalf("full: %v", err)
		}
	}
}

func BenchmarkArticleLongread(b *testing.B) { benchArticle(b, loadLongread(b)) }
func BenchmarkArticleLarge(b *testing.B)    { benchArticle(b, syntheticLargePage()) }
func BenchmarkFullLongread(b *testing.B)    { benchFull(b, loadLongread(b)) }
func BenchmarkFullLarge(b *testing.B)       { benchFull(b, syntheticLargePage()) }

// BenchmarkSanitize isolates sanitizeNode: html.Render + policy + Sanitize.
func BenchmarkSanitize(b *testing.B) {
	doc, err := html.Parse(strings.NewReader(loadLongread(b)))
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := sanitizeNode(doc); err != nil {
			b.Fatalf("sanitize: %v", err)
		}
	}
}
