package feed_test

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/christopherdavenport/unblink/internal/content/feed"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// lines returns the non-blank lines of md with surrounding whitespace trimmed,
// so assertions pin content and ordering rather than indentation or spacing.
func lines(md string) []string {
	var out []string
	for _, l := range strings.Split(md, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestConvertFormats pins the rendering of each feed dialect gofeed normalises
// for us. The expected lines are the whole document, in order: a dependency
// bump that changes field mapping (which date wins, summary vs. content, how
// links are picked) shows up here as a one-line diff.
func TestConvertFormats(t *testing.T) {
	tests := []struct {
		name      string
		file      string
		wantTitle string
		want      []string
	}{
		{
			name:      "rss 2.0",
			file:      "rss2.xml",
			wantTitle: "Example RSS Feed", // surrounding whitespace trimmed
			want: []string{
				"# Example RSS Feed",
				// Tags stripped, entities decoded.
				"News & updates from Example",
				"Feed: https://example.com/",
				"## Items (5)",
				// Link + parsed RFC 1123 date; HTML summary flattened to one line.
				"- [Full Item](https://example.com/posts/full) — 2006-01-02",
				"Hello & welcome to the feed",
				// No link, date, or description: a bare bullet.
				"- Bare Item",
				// No title: placeholder, still linked.
				"- [(untitled)](https://example.com/posts/untitled)",
				"Only a description here",
				// An unparseable date is shown verbatim rather than dropped.
				"- [Odd Date Item](https://example.com/posts/odd-date) — sometime last spring",
				// No <description>: falls back to content:encoded.
				"- [Content Only Item](https://example.com/posts/content-only)",
				"Body from content:encoded",
			},
		},
		{
			name:      "atom",
			file:      "atom.xml",
			wantTitle: "Example Atom Feed",
			want: []string{
				"# Example Atom Feed",
				"An Atom subtitle",
				// The alternate link, not rel=self.
				"Feed: https://atom.example.org/",
				"## Items (3)",
				// <published> wins over <updated>.
				"- [Published Entry](https://atom.example.org/published) — 2024-03-09",
				"Emphasised summary text",
				// Only <updated>: still dated; summary falls back to <content>.
				"- [Updated Only Entry](https://atom.example.org/updated-only) — 2024-02-01",
				"Body from Atom content",
				"- No Link Entry — 2024-01-01",
			},
		},
		{
			name:      "json feed",
			file:      "jsonfeed.json",
			wantTitle: "Example JSON Feed",
			want: []string{
				"# Example JSON Feed",
				"A JSON Feed description",
				// home_page_url, not feed_url.
				"Feed: https://json.example.net/",
				"## Items (3)",
				// summary wins over content_html.
				"- [JSON Item One](https://json.example.net/one) — 2023-11-05",
				"Plain summary one",
				"- [JSON Item Two](https://json.example.net/two)",
				"HTML body & more",
				"- (untitled)",
				"Text-only item with no title, url, or date",
			},
		},
		{
			name:      "empty feed",
			file:      "empty_rss.xml",
			wantTitle: "Empty Feed",
			want: []string{
				"# Empty Feed",
				// The empty <description> contributes no line.
				"Feed: https://empty.example.com/",
				"## Items (0)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, err := url.Parse("https://base.example/feed")
			if err != nil {
				t.Fatal(err)
			}
			md, title, err := feed.Convert(fixture(t, tt.file), base)
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			got := lines(md)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d lines, want %d:\n%s", len(got), len(tt.want), md)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
			if strings.ContainsAny(md, "<>") {
				t.Errorf("markup leaked into output:\n%s", md)
			}
		})
	}
}

// TestConvertMinimalFeed covers a feed with no title, description, or link:
// it must still produce the items section and report an empty title so the
// caller keeps whatever title it already had.
func TestConvertMinimalFeed(t *testing.T) {
	for _, in := range []string{
		`<rss version="2.0"></rss>`,
		`<rss version="2.0"><channel><title>  </title><description> <p> </p> </description></channel></rss>`,
		`<feed xmlns="http://www.w3.org/2005/Atom"/>`,
	} {
		md, title, err := feed.Convert([]byte(in), nil)
		if err != nil {
			t.Errorf("Convert(%q): %v", in, err)
			continue
		}
		if title != "" {
			t.Errorf("Convert(%q) title = %q, want empty", in, title)
		}
		if got := lines(md); len(got) != 1 || got[0] != "## Items (0)" {
			t.Errorf("Convert(%q) = %q, want only the items heading", in, md)
		}
	}
}

func rssWithItems(n int, description string) []byte {
	var b strings.Builder
	b.WriteString(`<rss version="2.0"><channel><title>Big</title>`)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "<item><title>Item %03d</title><link>https://example.com/%d</link><description>%s</description></item>", i, i, description)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

// TestConvertItemCap pins the 50-item cap: the heading reports the true total,
// only the first 50 are rendered, and the reader is told the list was cut.
func TestConvertItemCap(t *testing.T) {
	tests := []struct {
		total, wantRendered int
		wantNotice          bool
	}{
		{total: 49, wantRendered: 49},
		{total: 50, wantRendered: 50},
		{total: 51, wantRendered: 50, wantNotice: true},
		{total: 120, wantRendered: 50, wantNotice: true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.total), func(t *testing.T) {
			md, _, err := feed.Convert(rssWithItems(tt.total, ""), nil)
			if err != nil {
				t.Fatalf("Convert: %v", err)
			}
			if want := fmt.Sprintf("## Items (%d)", tt.total); !strings.Contains(md, want) {
				t.Errorf("missing %q", want)
			}
			if got := strings.Count(md, "- [Item "); got != tt.wantRendered {
				t.Errorf("rendered %d items, want %d", got, tt.wantRendered)
			}
			// Document order is kept: the cut drops the tail, not the head.
			if !strings.Contains(md, "[Item 001]") {
				t.Error("first item missing")
			}
			if strings.Contains(md, "[Item 051]") {
				t.Error("item past the cap was rendered")
			}
			notice := fmt.Sprintf("Showing first 50 of %d items", tt.total)
			if got := strings.Contains(md, notice); got != tt.wantNotice {
				t.Errorf("truncation notice present = %v, want %v", got, tt.wantNotice)
			}
		})
	}
}

// TestConvertSummaryTruncation pins the 280-rune summary cap. The body is
// multi-byte so a byte-indexed cut would split a rune and fail the UTF-8 check.
func TestConvertSummaryTruncation(t *testing.T) {
	summaryOf := func(t *testing.T, description string) string {
		t.Helper()
		md, _, err := feed.Convert(rssWithItems(1, description), nil)
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		if !utf8.ValidString(md) {
			t.Fatalf("output is not valid UTF-8:\n%q", md)
		}
		got := lines(md)
		return got[len(got)-1]
	}

	t.Run("at the limit is untouched", func(t *testing.T) {
		in := strings.Repeat("é", 280)
		if got := summaryOf(t, in); got != in {
			t.Errorf("summary has %d runes, want the 280 unchanged", utf8.RuneCountInString(got))
		}
	})
	t.Run("over the limit is cut with an ellipsis", func(t *testing.T) {
		got := summaryOf(t, strings.Repeat("é", 281))
		if want := strings.Repeat("é", 280) + "…"; got != want {
			t.Errorf("summary = %d runes ending %q, want 280 runes + ellipsis",
				utf8.RuneCountInString(got), got[max(0, len(got)-8):])
		}
	})
	t.Run("no space left before the ellipsis", func(t *testing.T) {
		// Rune 280 is a space; it must not survive as "word …".
		got := summaryOf(t, strings.Repeat("a", 279)+" tail that is cut off")
		if want := strings.Repeat("a", 279) + "…"; got != want {
			t.Errorf("summary ends %q, want %q", got[len(got)-8:], want[len(want)-8:])
		}
	})
}

// TestConvertMalformed feeds input that is not a usable feed. The contract the
// caller (content.Render) relies on is an error with empty results — it then
// falls back to showing the raw bytes as text — and never a panic.
func TestConvertMalformed(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"nil", nil},
		{"empty", []byte("")},
		{"whitespace", []byte(" \n\t ")},
		{"plain text", []byte("not a feed")},
		{"html document", []byte("<!doctype html><html><body><p>hi</p></body></html>")},
		{"binary", []byte("\x00\xff\xfe\x89PNG")},
		{"unterminated root tag", []byte("<rss")},
		{"truncated rss", fixture(t, "truncated_rss.xml")},
		{"truncated json", []byte(`{"version":"https://jsonfeed.org/version/1","items":[{"id":"1"`)},
		{"json items wrong type", []byte(`{"version":"https://jsonfeed.org/version/1","items":"nope"}`)},
		{"json null item", []byte(`{"version":"https://jsonfeed.org/version/1","items":[null]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, title, err := feed.Convert(tt.in, nil)
			if err == nil {
				t.Fatalf("expected an error, got markdown:\n%s", md)
			}
			if md != "" || title != "" {
				t.Errorf("error result should be empty, got md=%q title=%q", md, title)
			}
			if !strings.HasPrefix(err.Error(), "content/feed: parse: ") {
				t.Errorf("error %q lacks the package prefix", err)
			}
		})
	}
}
