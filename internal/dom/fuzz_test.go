package dom_test

import (
	"net/url"
	"os"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
)

// FuzzParseExtract throws arbitrary bytes at the full DOM surface that consumes
// untrusted page markup: parse, structural extraction, structured data, find,
// and visible-text rendering. The invariant is simply "never panic, never hang"
// — x/net/html guarantees a tree for any input, and everything downstream must
// cope with whatever tree that is.
func FuzzParseExtract(f *testing.F) {
	for _, fixture := range []string{
		"../../testdata/article.input.html",
		"../../testdata/structured.input.html",
	} {
		if b, err := os.ReadFile(fixture); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`<table><tr><td rowspan="3" colspan="2">x<td>y<tr><td itemscope itemprop=a>`))
	f.Add([]byte(`<script type="application/ld+json">{"@type":[{]}</script><base href="//">`))
	f.Add([]byte("<a href=\"\x00%zz://[\">t</a><form enctype=multipart/form-data><select><option>"))

	base, _ := url.Parse("https://fuzz.example/dir/page")
	f.Fuzz(func(t *testing.T, data []byte) {
		p := &page.Page{Raw: data}
		if err := dom.Parse(p); err != nil {
			return
		}
		_ = dom.Extract(p)
		_ = dom.Tables(p.Doc)
		_ = dom.JSONLD(p.Doc)
		_ = dom.Microdata(p.Doc, base)
		_ = dom.Find(p.Doc, "a", 5)
		_ = dom.Text(p.Doc, true)
		dom.StripDuplicateBlocks(p.Doc) // mutates, so keep it last
	})
}
