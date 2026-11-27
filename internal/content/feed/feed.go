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
// title/description followed by a bulleted list of recent items. Relative item
// links are resolved against base when it is non-nil.
func Convert(raw []byte, base *url.URL) (markdown, title string, err error) {
	fp := gofeed.NewParser()
	f, err := fp.Parse(bytes.NewReader(raw))
	if err != nil {
		return "", "", fmt.Errorf("content/feed: parse: %w", err)
	}

	var b strings.Builder
	title = oneLine(f.Title)
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
		writeItem(&b, it, base)
	}
	if total > maxItems {
		fmt.Fprintf(&b, "\n_Showing first %d of %d items._\n", maxItems, total)
	}
	return b.String(), title, nil
}

func writeItem(b *strings.Builder, it *gofeed.Item, base *url.URL) {
	t := oneLine(it.Title)
	if t == "" {
		t = "(untitled)"
	}
	if link := itemLink(it.Link, base); link != "" {
		fmt.Fprintf(b, "- [%s](%s)", linkText.Replace(t), link)
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

// oneLine collapses all whitespace (including newlines, which would end a
// Markdown heading or list item early) to single spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// linkText escapes the characters that would close a Markdown link label early.
var linkText = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)

// linkDest percent-encodes the characters that would end a Markdown link
// destination early or split it.
var linkDest = strings.NewReplacer(" ", "%20", "\t", "%09", "\n", "", "\r", "", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E")

// itemLink returns a Markdown-safe link destination, resolving a relative link
// against base when one is known.
func itemLink(raw string, base *url.URL) string {
	link := strings.TrimSpace(raw)
	if link == "" {
		return ""
	}
	if base != nil {
		if u, err := base.Parse(link); err == nil {
			link = u.String()
		}
	}
	return linkDest.Replace(link)
}

// blockTags are the elements whose boundaries separate text: stripping them
// must leave a space, or "<h2>Heading</h2><p>Body</p>" reads "HeadingBody".
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "br": true,
	"dd": true, "div": true, "dl": true, "dt": true, "figcaption": true, "figure": true,
	"footer": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"header": true, "hr": true, "li": true, "ol": true, "p": true, "pre": true,
	"section": true, "table": true, "td": true, "th": true, "tr": true, "ul": true,
}

// clean strips HTML tags and decodes entities without pulling in x/net/html.
// A '<' only opens a tag when it looks like one (followed by a letter, '/',
// '!' or '?', and closed by a later '>'); any other '<' is text, so plain-text
// content such as "if a < b then c > d" survives intact.
func clean(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '<' {
			b.WriteByte(c)
			continue
		}
		end := tagEnd(s, i)
		if end < 0 {
			b.WriteByte(c)
			continue
		}
		if blockTags[tagName(s[i+1:end])] {
			b.WriteByte(' ')
		}
		i = end
	}
	// Collapse runs of whitespace and decode entities (stdlib html, not x/net/html).
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}

// tagEnd returns the index of the '>' closing the tag that starts at s[i]
// ('<'), or -1 when s[i] does not start a tag.
func tagEnd(s string, i int) int {
	if i+1 >= len(s) {
		return -1
	}
	n := s[i+1]
	if !isLetter(n) && n != '/' && n != '!' && n != '?' {
		return -1
	}
	if n == '/' && (i+2 >= len(s) || !isLetter(s[i+2])) {
		return -1
	}
	rel := strings.IndexByte(s[i+1:], '>')
	if rel < 0 {
		return -1
	}
	return i + 1 + rel
}

// tagName returns the lower-cased element name of a tag body such as
// `/P class="x"` or `br/`.
func tagName(body string) string {
	body = strings.TrimPrefix(body, "/")
	n := 0
	for n < len(body) && (isLetter(body[n]) || (n > 0 && body[n] >= '0' && body[n] <= '9')) {
		n++
	}
	return strings.ToLower(body[:n])
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
