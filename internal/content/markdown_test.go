package content

import (
	"context"
	"testing"

	"github.com/christopherdavenport/unblink/internal/page"
)

// A Markdown body passes through unfenced, with line endings normalised and the
// title taken from its first level-1 heading.
func TestRenderMarkdownPassthrough(t *testing.T) {
	raw := "\xEF\xBB\xBF# Release Notes\r\n\r\nSome *prose*.\r\n\r\n```go\r\nfmt.Println()\r\n```"
	p := &page.Page{Kind: page.KindMarkdown, Raw: []byte(raw)}
	p.Meta.Title = "notes.md"
	if err := Render(context.Background(), p); err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "# Release Notes\n\nSome *prose*.\n\n```go\nfmt.Println()\n```\n"
	if p.Markdown != want {
		t.Errorf("markdown = %q\nwant      %q", p.Markdown, want)
	}
	if p.Meta.Title != "Release Notes" {
		t.Errorf("title = %q, want it upgraded from the H1", p.Meta.Title)
	}
}

func TestRenderMarkdownKeepsTitleWithoutHeading(t *testing.T) {
	p := &page.Page{Kind: page.KindMarkdown, Raw: []byte("just a paragraph\n")}
	p.Meta.Title = "notes.md"
	if err := Render(context.Background(), p); err != nil {
		t.Fatalf("render: %v", err)
	}
	if p.Meta.Title != "notes.md" || p.Markdown != "just a paragraph\n" {
		t.Errorf("title = %q, markdown = %q", p.Meta.Title, p.Markdown)
	}
}

func TestMarkdownTitle(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"h1", "# Hello World\n\nbody", "Hello World"},
		{"closing hashes", "# Hello #\n", "Hello"},
		{"skips h2", "## Section\n\n# Real Title\n", "Real Title"},
		{"not a heading without space", "#hashtag\n", ""},
		{"ignores fenced code", "```sh\n# a shell comment\n```\n\n# Title\n", "Title"},
		{"ignores longer fence", "````\n```\n# still code\n````\n# Title\n", "Title"},
		{"front matter title", "---\ntitle: \"From Front Matter\"\ndescription: x\n---\n\nbody only\n", "From Front Matter"},
		{"h1 beats front matter", "---\ntitle: FM\n---\n# Heading\n", "Heading"},
		{"front matter comment is not a heading", "---\n# comment\ntitle: FM\n---\n", "FM"},
		{"none", "plain paragraph\n", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := MarkdownTitle([]byte(c.in)); got != c.want {
			t.Errorf("%s: MarkdownTitle = %q, want %q", c.name, got, c.want)
		}
	}
}
