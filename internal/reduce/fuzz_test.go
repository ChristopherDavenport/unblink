package reduce_test

import (
	"net/url"
	"os"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/emit"
	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/reduce"
)

// FuzzReduce drives arbitrary markup through both reductions (readability +
// sanitizer, hidden-strip on) and the Markdown emitter — the exact pipeline an
// untrusted page's bytes reach on every read. Invariant: no panic, no hang.
func FuzzReduce(f *testing.F) {
	for _, fixture := range []string{
		"../../testdata/article.input.html",
		"../../testdata/structured.input.html",
	} {
		if b, err := os.ReadFile(fixture); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`<article><h1>t</h1><p style="display:none">hidden</p><img src=x onerror=y></article>`))

	final, _ := url.Parse("https://fuzz.example/a")
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, mode := range []func(*page.Page, bool) error{reduce.Article, reduce.Full} {
			p := &page.Page{Raw: data, FinalURL: final}
			if err := dom.Parse(p); err != nil {
				return
			}
			if err := mode(p, true); err != nil {
				continue
			}
			_ = emit.Markdown(p)
			_ = emit.DefangImages(p.Markdown)
		}
	})
}
