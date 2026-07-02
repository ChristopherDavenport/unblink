package dom_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
)

// controlHeavyHTML builds a large document dense with interactive controls —
// the worst case for Extract's per-control unique-selector search, which today
// runs full-document queries per control.
func controlHeavyHTML() string {
	var sb strings.Builder
	sb.WriteString("<html><head><title>Control heavy</title></head><body>")
	for i := 0; i < 300; i++ {
		switch i % 3 {
		case 0:
			fmt.Fprintf(&sb, `<button class="btn action-%d">Action %d</button>`, i, i)
		case 1:
			fmt.Fprintf(&sb, `<div class="widget" onclick="go(%d)" tabindex="0">Widget %d</div>`, i, i)
		default:
			fmt.Fprintf(&sb, `<input type="text" name="field-%d" class="field">`, i)
		}
		fmt.Fprintf(&sb, `<p>Filler paragraph %d with a <a href="/link/%d">link %d</a> and some prose.</p>`, i, i, i)
		if i%20 == 0 {
			fmt.Fprintf(&sb, `<h2>Section %d</h2>`, i)
		}
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

func benchParsedPage(b *testing.B, src string) *page.Page {
	b.Helper()
	u, _ := url.Parse("https://example.com/")
	p := &page.Page{Raw: []byte(src), RequestURL: u, FinalURL: u}
	if err := dom.Parse(p); err != nil {
		b.Fatalf("parse: %v", err)
	}
	return p
}

func BenchmarkExtract(b *testing.B) {
	p := benchParsedPage(b, controlHeavyHTML())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := dom.Extract(p); err != nil {
			b.Fatalf("extract: %v", err)
		}
	}
}

// BenchmarkParse is the html.Parse baseline for the same document (useful for
// subtracting parse cost from benchmarks that must re-parse per iteration).
func BenchmarkParse(b *testing.B) {
	raw := []byte(controlHeavyHTML())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := &page.Page{Raw: raw}
		if err := dom.Parse(p); err != nil {
			b.Fatalf("parse: %v", err)
		}
	}
}

// BenchmarkStripDuplicateBlocks includes a per-iteration parse (the pass
// mutates the tree, so docs are single-use); subtract BenchmarkParse for the
// dedupe-only cost.
func BenchmarkStripDuplicateBlocks(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("<html><body>")
	nav := `<nav><ul>`
	for i := 0; i < 60; i++ {
		nav += fmt.Sprintf(`<li><a href="/s/%d">Navigation section entry %d</a></li>`, i, i)
	}
	nav += `</ul></nav>`
	sb.WriteString(nav)
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, `<p>Body paragraph %d with enough words to build real text signatures at every level.</p>`, i)
	}
	sb.WriteString(nav) // duplicated link-dense block (the mobile-drawer twin)
	sb.WriteString("</body></html>")
	src := sb.String()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := html.Parse(strings.NewReader(src))
		if err != nil {
			b.Fatalf("parse: %v", err)
		}
		dom.StripDuplicateBlocks(doc)
	}
}
