package browser_test

// mode=article on a page that is not an article: the fragment readability would
// return is replaced by the page's main region (or the whole page), and the
// result says which and why.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// sectionFront is a news section front: a river of story cards in <main>, and
// the only real prose on the page in the footer.
func sectionFront() string {
	var river strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&river, `<div class="card"><h2><a href="/story/%d">STORY headline number %d about an event that happened today</a></h2><p>Teaser %d.</p></div>`+"\n", i, i, i)
	}
	return `<html><head><title>Latest</title></head><body>
	<nav><a href="/world">World section</a> <a href="/tech">Tech section</a></nav>
	<main><h1>Latest</h1>` + river.String() + `</main>
	<footer><div><p>The Daily Example has been separating the signal from the noise for over twenty-five years. With a unique combination of technical savvy and wide-ranging interest in the arts and sciences, it is a trusted source in a sea of information. After all, you do not need to know everything, only what is important to you and to the people you work with every day.</p></div></footer>
	</body></html>`
}

func TestArticleModeOnHubReturnsMainRegion(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string { return sectionFront() })
	b, err := browser.New(browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	r, err := b.Read(ctx, browser.Request{URL: srv.URL}, "article", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "main" || !r.ArticleFallback || r.ArticleFallbackReason != "partial_article" {
		t.Fatalf("mode=%q fallback=%v reason=%q, want main/true/partial_article", r.Mode, r.ArticleFallback, r.ArticleFallbackReason)
	}
	if !strings.Contains(r.Markdown, "STORY headline number 39") {
		t.Errorf("story river missing:\n%s", r.Markdown)
	}
	if strings.Contains(r.Markdown, "World section") || strings.Contains(r.Markdown, "separating the signal") {
		t.Errorf("page chrome in the main-region fallback:\n%s", r.Markdown)
	}

	// The memoized repeat read reports the same mode and reason.
	again, err := b.Read(ctx, browser.Request{URL: srv.URL}, "article", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.Mode != r.Mode || again.ArticleFallbackReason != r.ArticleFallbackReason || again.Markdown != r.Markdown {
		t.Errorf("repeat read differs: mode=%q reason=%q", again.Mode, again.ArticleFallbackReason)
	}

	// mode=full is still the whole page, and is not a fallback.
	full, err := b.Read(ctx, browser.Request{URL: srv.URL}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if full.Mode != "full" || full.ArticleFallback || full.ArticleFallbackReason != "" {
		t.Errorf("mode=full: mode=%q fallback=%v reason=%q", full.Mode, full.ArticleFallback, full.ArticleFallbackReason)
	}
	if !strings.Contains(full.Markdown, "World section") {
		t.Errorf("mode=full lost the navigation it is meant to keep")
	}
}

// The no-article fallback keeps its contract and now names its reason.
func TestArticleFallbackReasonNoArticle(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return `<html><head><title>app</title></head><body><div id="app"></div><script src="/x.js"></script></body></html>`
	})
	b, err := browser.New(browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	r, err := b.Read(context.Background(), browser.Request{URL: srv.URL}, "article", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "full" || !r.ArticleFallback || r.ArticleFallbackReason != "no_article" {
		t.Errorf("mode=%q fallback=%v reason=%q, want full/true/no_article", r.Mode, r.ArticleFallback, r.ArticleFallbackReason)
	}
}

// A real article is untouched: no fallback, no reason, no chrome.
func TestArticleModeOnArticleUnchanged(t *testing.T) {
	var prose strings.Builder
	for i := 0; i < 14; i++ {
		fmt.Fprintf(&prose, "<p>ESSAY paragraph %d explains, at some length and in plain sentences, what the reader came here to learn about the subject.</p>\n", i)
	}
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return `<html><head><title>Essay</title></head><body><nav><a href="/a">Alpha section</a></nav>
		<main><article><h1>Essay</h1>` + prose.String() + `</article></main><footer>Colophon.</footer></body></html>`
	})
	b, err := browser.New(browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	r, err := b.Read(context.Background(), browser.Request{URL: srv.URL}, "article", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "article" || r.ArticleFallback || r.ArticleFallbackReason != "" {
		t.Errorf("mode=%q fallback=%v reason=%q, want a plain article read", r.Mode, r.ArticleFallback, r.ArticleFallbackReason)
	}
	if !strings.Contains(r.Markdown, "ESSAY paragraph 13") || strings.Contains(r.Markdown, "Alpha section") {
		t.Errorf("article content wrong:\n%s", r.Markdown)
	}
}
