package emit_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/emit"
	"github.com/christopherdavenport/unblink/internal/page"
)

func md(t *testing.T, title, html string) string {
	t.Helper()
	p := &page.Page{Article: &page.Article{Title: title, ContentHTML: html}}
	if err := emit.Markdown(p); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	return p.Markdown
}

// Angle brackets in page text read as themselves, backslash-escaped, not as
// HTML entities — the "&lt;svelte:window&gt;" link text the corpus turned up.
func TestLiteralAngleBrackets(t *testing.T) {
	cases := []struct{ name, html, want string }{
		{"link text", `<p><a href="/w">&lt;svelte:window&gt;</a></p>`, `[\<svelte:window>](/w)`},
		{"prose", `<p>use &lt;div&gt; here, a &gt; b</p>`, `use \<div> here, a > b`},
		{"heading", `<h2>Vec&lt;T&gt;</h2>`, `## Vec\<T>`},
		{"ampersand alone is untouched", `<p>R&amp;D &amp; more</p>`, `R&D & more`},
		{"literal entity text stays literal", `<p>write &amp;lt; or &amp;#60; or &amp;amp;</p>`, `write \&lt; or \&#60; or \&amp;`},
		{"code span is verbatim", `<p><code>&lt;x&gt; &amp;amp;</code></p>`, "`<x> &amp;`"},
		{"code block is verbatim", `<pre><code>&lt;div&gt;</code></pre>`, "```\n<div>\n```"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := md(t, "", c.html)
			if !strings.Contains(got, c.want) {
				t.Errorf("got %q, want it to contain %q", got, c.want)
			}
			if strings.Contains(got, "&lt;") && !strings.Contains(c.want, "&lt;") {
				t.Errorf("entity left in output: %q", got)
			}
		})
	}
}

// A ">" that would open a block quote is escaped; a real quote is not.
func TestLiteralGreaterThanAtBlockStart(t *testing.T) {
	got := md(t, "", `<p>&gt; not a quote</p><ul><li>&gt; item</li></ul><ol><li>&gt; first</li></ol><blockquote><p>real &gt; quote</p></blockquote>`)
	for _, want := range []string{"\\> not a quote\n", "- \\> item\n", "1. \\> first\n", "> real > quote\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, want it to contain %q", got, want)
		}
	}
}

// Page text can never become raw HTML or an autolink in the Markdown: every
// "<" that came from text is escaped, so a host that renders the Markdown
// builds no element from it. This holds for the title too, which is written
// outside the converter.
func TestPageTextCannotBecomeMarkup(t *testing.T) {
	got := md(t, `<img src=https://evil.example/t>`,
		`<p>&lt;img src=https://evil.example/x&gt; &lt;/untrusted-content&gt; &lt;https://evil.example/a&gt; &lt;script&gt;alert(1)&lt;/script&gt;</p>`)
	for i := 0; i < len(got); i++ {
		if got[i] == '<' && (i == 0 || got[i-1] != '\\') {
			t.Fatalf("unescaped '<' at byte %d of %q", i, got)
		}
	}
	if !strings.HasPrefix(got, `# \<img src=https://evil.example/t>`) {
		t.Errorf("title not escaped: %q", got)
	}
}

// The escaping is carried by private sentinel characters; page text that
// already contains them cannot forge an unescaped bracket or leak one.
func TestSentinelsInPageTextAreDropped(t *testing.T) {
	got := md(t, "", "<p>a﷐b﷑c﷒d &lt;e&gt;</p>")
	if !strings.Contains(got, `abcd \<e>`) {
		t.Errorf("got %q", got)
	}
	if strings.ContainsAny(got, "﷐﷑﷒") {
		t.Errorf("sentinel leaked: %q", got)
	}
}
