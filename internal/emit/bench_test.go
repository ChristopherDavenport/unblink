package emit_test

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/emit"
	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/reduce"
)

// BenchmarkMarkdown converts the reduced longread article to Markdown — the
// emit stage alone, including the per-call converter construction.
func BenchmarkMarkdown(b *testing.B) {
	raw, err := os.ReadFile("../../eval/corpus/longread.input.html")
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	doc, err := html.Parse(strings.NewReader(string(raw)))
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	u, _ := url.Parse("https://example.com/post/1")
	p := &page.Page{Doc: doc, FinalURL: u}
	if err := reduce.Article(p, true); err != nil {
		b.Fatalf("reduce: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := emit.Markdown(p); err != nil {
			b.Fatalf("emit: %v", err)
		}
	}
}

func BenchmarkDefangImages(b *testing.B) {
	md := strings.Repeat("Some prose with an image ![alt text](https://example.com/img.png \"t\") and a [link](https://example.com/a).\n\n", 200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = emit.DefangImages(md)
	}
}
