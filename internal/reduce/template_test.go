package reduce_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/reduce"
)

// A <template>'s contents are inert and must not be read as page text on the
// static (no-JavaScript) path either.
func TestTemplateContentsAreNotPageText(t *testing.T) {
	const src = `<html><head><title>T</title></head><body><main><h1>Catalogue</h1>
		<p>` + "Visible paragraph with enough words to count as the content of this page for the reader." + `</p>
		<template id="row"><li class="item">TEMPLATE-ONLY-TEXT <a href="/x">hidden link</a></li></template>
		<ul><li>Real item</li></ul></main></body></html>`
	for _, mode := range []string{"full", "article"} {
		final, _ := url.Parse("https://example.com/")
		p := &page.Page{Raw: []byte(src), FinalURL: final}
		if err := dom.Parse(p); err != nil {
			t.Fatal(err)
		}
		var err error
		if mode == "full" {
			err = reduce.Full(p, true)
		} else {
			err = reduce.Article(p, true)
		}
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if strings.Contains(p.Article.ContentHTML, "TEMPLATE-ONLY-TEXT") || strings.Contains(p.Article.ContentHTML, "hidden link") {
			t.Errorf("%s mode emitted template contents:\n%s", mode, p.Article.ContentHTML)
		}
		if !strings.Contains(p.Article.ContentHTML, "Real item") {
			t.Errorf("%s mode lost real content:\n%s", mode, p.Article.ContentHTML)
		}
	}
}
