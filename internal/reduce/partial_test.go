package reduce_test

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/reduce"
)

// prose returns n distinct sentences of ordinary article text.
func prose(tag string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "<p>%s paragraph %d explains, at some length and in plain sentences, what the reader came here to learn about the subject at hand.</p>\n", tag, i)
	}
	return b.String()
}

// cards returns n story cards: a linked headline and a one-line teaser, the
// shape of a section front's story river.
func cards(tag string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<div class="card"><h2><a href="/%s/%d">%s headline number %d about an event that happened today</a></h2><p>Teaser %d.</p></div>`+"\n", tag, i, tag, i, i)
	}
	return b.String()
}

const siteChrome = `<header><a href="/">Site</a></header>
<nav><ul><li><a href="/a">Alpha section</a></li><li><a href="/b">Beta section</a></li><li><a href="/c">Gamma section</a></li></ul></nav>`

const aboutFooter = `<footer><div class="about"><p>The Daily Example has been separating the signal from the noise for over twenty-five years. With a unique combination of technical savvy and wide-ranging interest in the arts and sciences, it is a trusted source in a sea of information. After all, you do not need to know everything, only what is important to you and to the people you work with every day.</p></div></footer>`

func reduceArticle(t *testing.T, body string) *page.Article {
	t.Helper()
	p := newPage(t, body)
	if err := reduce.Article(p, true); err != nil {
		t.Fatalf("Article: %v", err)
	}
	return p.Article
}

// A real article — with site chrome, a sidebar and a footer around it — is
// returned as the article. The rule must not cost a true article page anything.
func TestArticleKeptForRealArticle(t *testing.T) {
	a := reduceArticle(t, siteChrome+`<main><article><h1>The Subject</h1>`+prose("ARTICLE", 12)+`</article>
		<aside><h2>Related</h2>`+cards("related", 12)+`</aside></main>`+aboutFooter)
	if a.Source != "readability" || a.Fallback != "" {
		t.Fatalf("Source=%q Fallback=%q, want readability with no fallback", a.Source, a.Fallback)
	}
	if !strings.Contains(a.ContentHTML, "ARTICLE paragraph 11") {
		t.Errorf("article text missing")
	}
	if strings.Contains(a.ContentHTML, "related headline") {
		t.Errorf("sidebar leaked into the article")
	}
}

// A section front: readability keeps the footer "about" blurb — the only real
// prose on the page — and drops the story river. That is not the page.
func TestArticleFallsBackOnHubWithMain(t *testing.T) {
	a := reduceArticle(t, siteChrome+`<main><h1>Latest</h1>`+cards("river", 40)+`</main>`+aboutFooter)
	if a.Source != "main" || a.Fallback != page.FallbackPartialArticle {
		t.Fatalf("Source=%q Fallback=%q, want main / %s", a.Source, a.Fallback, page.FallbackPartialArticle)
	}
	for _, want := range []string{"river headline number 0 ", "river headline number 39 "} {
		if !strings.Contains(a.ContentHTML, want) {
			t.Errorf("main region lost %q", want)
		}
	}
	// The fallback is the main landmark, not the whole page: chrome stays out.
	for _, chrome := range []string{"Alpha section", "separating the signal"} {
		if strings.Contains(a.ContentHTML, chrome) {
			t.Errorf("chrome %q leaked into the main-region fallback", chrome)
		}
	}
}

// A link directory with no landmarks at all: there is no main region to fall
// back to, so the whole page is returned.
func TestArticleFallsBackOnHubWithoutLandmarks(t *testing.T) {
	// Pre-HTML4 markup, as hand-written directories still serve it: bare <li>
	// entries straight under <body>, and a boxed blurb at the bottom.
	var dir strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&dir, `<li><tt><small><b>dir</b> %dk</small></tt> <a href="/author/%d"><b>Author number %d, collected short prose</b></a>`+"\n", i, i, i)
	}
	a := reduceArticle(t, `<center><h1>Directory</h1></center>`+dir.String()+`<hr>
		<table><tr><td><b>18+</b><div>Registration certificate number 77-20625 issued on the twelfth of May. <i>Supported by the federal agency for press and mass communications, which funded the digitisation of this collection over a number of years and continues to do so.</i></div></td></tr></table>`)
	if a.Source != "full" || a.Fallback != page.FallbackPartialArticle {
		t.Fatalf("Source=%q Fallback=%q, want full / %s", a.Source, a.Fallback, page.FallbackPartialArticle)
	}
	if !strings.Contains(a.ContentHTML, "Author number 119,") {
		t.Errorf("directory entries missing from the fallback")
	}
}

// The page's content sits entirely in elements marked up as chrome and
// readability picks a blurb out of one of them. The content region is then
// (nearly) empty, so the extraction is measured against the whole page — and
// the fallback is the whole page too, since <main> is not where the content is.
func TestArticleFromChromeFallsBackToFullPage(t *testing.T) {
	a := reduceArticle(t, `<main><p>Welcome.</p></main>
		<aside><h2>Chapters</h2>`+cards("chapter", 40)+`</aside>`+aboutFooter)
	if a.Source != "full" || a.Fallback != page.FallbackPartialArticle {
		t.Fatalf("Source=%q Fallback=%q, want full / %s", a.Source, a.Fallback, page.FallbackPartialArticle)
	}
	if !strings.Contains(a.ContentHTML, "chapter headline number 39 ") {
		t.Errorf("sidebar listing missing from the full-page fallback")
	}
}

// A short article on a page with heavy chrome stays an article: chrome is not
// part of what the extraction is measured against.
func TestShortArticleWithHeavyChromeKept(t *testing.T) {
	var nav strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&nav, `<li><a href="/topic/%d">Topic number %d in the reference</a></li>`, i, i)
	}
	a := reduceArticle(t, `<nav><ul>`+nav.String()+`</ul></nav><main><article><h1>Note</h1>`+prose("SHORT", 4)+`</article></main>`+aboutFooter)
	if a.Source != "readability" || a.Fallback != "" {
		t.Fatalf("Source=%q Fallback=%q, want readability", a.Source, a.Fallback)
	}
}

// An article followed by a long discussion: what the extraction leaves out is
// prose, not a listing. Between one half and two thirds coverage that is
// tolerated — an article page may carry comments.
func TestArticleWithCommentsKept(t *testing.T) {
	a := reduceArticle(t, `<main><article><h1>Essay</h1>`+prose("ESSAY", 24)+`</article>
		<section class="comments"><h2>Comments</h2>`+strings.ReplaceAll(prose("COMMENT", 14), "<p>", `<div class="comment"><span>`)+`</section></main>`)
	if a.Source != "readability" {
		t.Fatalf("Source=%q Fallback=%q, want readability", a.Source, a.Fallback)
	}
}

// The same coverage band, but what was left out is a river of linked
// headlines: the extraction took the lead story and dropped the rest of a
// section front.
func TestLeadStoryPlusRiverFallsBack(t *testing.T) {
	a := reduceArticle(t, `<main><div class="lead"><h1>Lead story</h1>`+prose("LEAD", 20)+`</div>
		<div class="river">`+strings.ReplaceAll(cards("river", 28), "<p>Teaser", "<p hidden>Teaser")+`</div></main>`)
	if a.Source != "main" || a.Fallback != page.FallbackPartialArticle {
		t.Fatalf("Source=%q Fallback=%q, want main / %s", a.Source, a.Fallback, page.FallbackPartialArticle)
	}
	if !strings.Contains(a.ContentHTML, "river headline number 27 ") || !strings.Contains(a.ContentHTML, "LEAD paragraph 19") {
		t.Errorf("main-region fallback is missing the lead or the river")
	}
}

// Tiny pages never flip: under partialMinDropped of left-out text the loss is
// too small to act on.
func TestSmallPageNeverFallsBack(t *testing.T) {
	a := reduceArticle(t, `<main><article><h1>Hi</h1>`+prose("TINY", 3)+`</article>`+cards("more", 6)+`</main>`)
	if a.Fallback == page.FallbackPartialArticle {
		t.Fatalf("a page with under 1000 bytes outside the article fell back (Source=%q)", a.Source)
	}
}

// No article at all still falls back to the full page, now with a reason.
func TestNoArticleReasonRecorded(t *testing.T) {
	a := reduceArticle(t, `<p>x</p>`)
	if a.Source == "readability" {
		t.Skip("readability extracted the stub; nothing to assert")
	}
	if a.Source != "full" || a.Fallback != page.FallbackNoArticle {
		t.Errorf("Source=%q Fallback=%q, want full / %s", a.Source, a.Fallback, page.FallbackNoArticle)
	}
}

// The fallback must not mutate the shared document.
func TestPartialFallbackLeavesDocUntouched(t *testing.T) {
	p := newPage(t, siteChrome+`<main><h1>Latest</h1>`+cards("river", 40)+`<a href="relative/path">rel</a></main>`+aboutFooter)
	before := renderDoc(t, p)
	if err := reduce.Article(p, true); err != nil {
		t.Fatal(err)
	}
	if p.Article.Source != "main" {
		t.Fatalf("Source=%q, want main", p.Article.Source)
	}
	if !strings.Contains(p.Article.ContentHTML, `href="https://example.com/relative/path"`) {
		t.Errorf("links in the main-region fallback were not absolutized")
	}
	if after := renderDoc(t, p); after != before {
		t.Errorf("Article mutated p.Doc")
	}
}

func renderDoc(t *testing.T, p *page.Page) string {
	t.Helper()
	var b strings.Builder
	if err := html.Render(&b, p.Doc); err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}
