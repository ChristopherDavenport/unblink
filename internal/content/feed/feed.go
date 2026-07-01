// Package feed converts RSS, Atom, and JSON feeds into a Markdown summary. It
// parses via gofeed and must not import the HTML parser (x/net/html); item
// summaries are cleaned with a lightweight tag stripper and the stdlib html
// entity decoder instead.
package feed

import (
	"bytes"
	"fmt"
	"html"
	"net/url"
	"strings"

	"github.com/mmcdole/gofeed"
)

// maxItems caps how many feed items are rendered before tokens.Paginate chunks
// further, keeping a huge feed from blowing the budget on a single read.
const maxItems = 50

// Convert parses an RSS/Atom/JSON feed and renders it as Markdown: the feed
// title/description followed by a bulleted list of recent items. base is reserved
// for resolving relative item links (feed links are usually absolute).
func Convert(raw []byte, base *url.URL) (markdown, title string, err error) {
	fp := gofeed.NewParser()
	f, err := fp.Parse(bytes.NewReader(raw))
	if err != nil {
		return "", "", fmt.Errorf("content/feed: parse: %w", err)
	}

	var b strings.Builder
	title = strings.TrimSpace(f.Title)
	if title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	if d := clean(f.Description); d != "" {
		fmt.Fprintf(&b, "%s\n\n", d)
	}
	if link := strings.TrimSpace(f.Link); link != "" {
		fmt.Fprintf(&b, "Feed: %s\n\n", link)
	}

	total := len(f.Items)
	items := f.Items
	if total > maxItems {
		items = items[:maxItems]
	}
	fmt.Fprintf(&b, "## Items (%d)\n\n", total)
	for _, it := range items {
		writeItem(&b, it)
	}
	if total > maxItems {
		fmt.Fprintf(&b, "\n_Showing first %d of %d items._\n", maxItems, total)
	}
	return b.String(), title, nil
}

func writeItem(b *strings.Builder, it *gofeed.Item) {
	t := strings.TrimSpace(it.Title)
	if t == "" {
		t = "(untitled)"
	}
	if strings.TrimSpace(it.Link) != "" {
		fmt.Fprintf(b, "- [%s](%s)", t, strings.TrimSpace(it.Link))
	} else {
		fmt.Fprintf(b, "- %s", t)
	}
	switch {
	case it.PublishedParsed != nil:
		fmt.Fprintf(b, " — %s", it.PublishedParsed.Format("2006-01-02"))
	case strings.TrimSpace(it.Published) != "":
		fmt.Fprintf(b, " — %s", strings.TrimSpace(it.Published))
	}
	b.WriteString("\n")
	if s := itemSummary(it); s != "" {
		fmt.Fprintf(b, "  %s\n", s)
	}
}

func itemSummary(it *gofeed.Item) string {
	s := it.Description
	if strings.TrimSpace(s) == "" {
		s = it.Content
	}
	return truncate(clean(s), 280)
}

// clean strips HTML tags and decodes entities without pulling in x/net/html.
func clean(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	// Collapse runs of whitespace and decode entities (stdlib html, not x/net/html).
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
