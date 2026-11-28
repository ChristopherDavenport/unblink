package reduce_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/emit"
	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/reduce"
)

// fullMarkdown reduces body in Full mode and returns the emitted Markdown.
func fullMarkdown(t *testing.T, body string) string {
	t.Helper()
	p := newPage(t, body)
	if err := reduce.Full(p, true); err != nil {
		t.Fatalf("Full: %v", err)
	}
	return markdown(t, p)
}

func markdown(t *testing.T, p *page.Page) string {
	t.Helper()
	if err := emit.Markdown(p); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	return p.Markdown
}

// Touching inline elements are separate pieces of text (CSS lays them out
// apart); code stays exactly as written.
func TestFullSeparatesTouchingInlineElements(t *testing.T) {
	md := fullMarkdown(t, `<main>
		<ol><li><a href="/intro"><span>00</span><span>Introduction</span></a></li></ol>
		<div class="grid"><span>Jan</span><span>Mar</span><span>May</span></div>
		<pre><code><span>const</span><span>x</span></code></pre>
	</main>`)
	for _, want := range []string{"[00 Introduction]", "Jan Mar May", "constx"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown missing %q:\n%s", want, md)
		}
	}
}

func TestArticleSeparatesTouchingInlineElements(t *testing.T) {
	p := newPage(t, `<main><article><h1>The Subject</h1>`+prose("BODY", 12)+
		`<p><span>Updated</span><span>Tuesday</span></p></article></main>`)
	if err := reduce.Article(p, true); err != nil {
		t.Fatalf("Article: %v", err)
	}
	if md := markdown(t, p); !strings.Contains(md, "Updated Tuesday") {
		t.Errorf("article Markdown ran the pieces together:\n%s", md)
	}
}

// Minified table markup: a data table becomes a Markdown table, and a table
// the renderer declines still keeps its cells apart.
func TestFullTables(t *testing.T) {
	md := fullMarkdown(t, `<main><table><tr><th>Name</th><th>Size</th></tr><tr><td>alpha</td><td>12</td></tr></table>`+
		`<table role="presentation"><tr><td>12</td><td>34</td></tr></table></main>`)
	for _, want := range []string{"| Name | Size |", "| alpha | 12 |", "12 34"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown missing %q:\n%s", want, md)
		}
	}
}

// A closed <details> keeps its content and is labelled as collapsed; a closed
// <dialog> is not part of the page and stays out.
func TestCollapsedDetailsKeptAndMarked(t *testing.T) {
	body := `<main><h1>FAQ</h1>
		<details><summary>How do I reset it?</summary><p>Hold the button for ten seconds.</p></details>
		<details open><summary>Already open</summary><p>Visible answer.</p></details>
		<dialog><p>DIALOG-TEXT</p></dialog></main>`
	md := fullMarkdown(t, body)
	for _, want := range []string{"▸ How do I reset it?", "Hold the button for ten seconds.", "Already open", "Visible answer."} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "▸ Already open") || strings.Contains(md, "DIALOG-TEXT") {
		t.Errorf("open details marked, or closed dialog emitted:\n%s", md)
	}
}
