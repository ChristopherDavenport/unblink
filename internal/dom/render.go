package dom

import (
	"fmt"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// maxSelectorMatches caps how many nodes a raw_html selector serializes, so a
// broad selector (e.g. "div") can't produce an unbounded blob.
const maxSelectorMatches = 256

// Render serializes n (typically a whole document or a subtree) back to HTML.
// It is read-only — it never mutates the tree or rewrites URLs, so callers get
// the source verbatim.
func Render(n *html.Node) (string, error) {
	var sb strings.Builder
	if err := html.Render(&sb, n); err != nil {
		return "", fmt.Errorf("dom: render: %w", err)
	}
	return sb.String(), nil
}

// RenderSelector compiles selector, matches it within doc (document order), and
// returns the outerHTML of the matched nodes joined by newlines together with
// the number of matches. A bad selector is an error; zero matches returns
// ("", 0, nil) so the caller can surface a clear "matched no elements" message.
// Read-only (no mutation, no URL rewriting); output is capped at
// maxSelectorMatches nodes.
func RenderSelector(doc *html.Node, selector string) (string, int, error) {
	sel, err := cascadia.Compile(selector)
	if err != nil {
		return "", 0, fmt.Errorf("dom: invalid selector %q: %w", selector, err)
	}
	matches := sel.MatchAll(doc)
	if len(matches) == 0 {
		return "", 0, nil
	}
	total := len(matches)
	if len(matches) > maxSelectorMatches {
		matches = matches[:maxSelectorMatches]
	}
	var sb strings.Builder
	for i, m := range matches {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if err := html.Render(&sb, m); err != nil {
			return "", 0, fmt.Errorf("dom: render selector match: %w", err)
		}
	}
	return sb.String(), total, nil
}

// textBlockTags are elements that introduce a paragraph break in Text output, so
// the plain text keeps a readable block structure instead of one long line.
var textBlockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "aside": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "tr": true, "table": true,
	"blockquote": true, "pre": true, "hr": true, "figure": true, "figcaption": true,
	"header": true, "footer": true, "main": true, "nav": true,
}

// Text returns the visible plain text of doc: descendant text with inline runs
// of whitespace collapsed and a blank line inserted at block boundaries,
// skipping script/style/head/noscript/template. It is the escape hatch for
// "format=text" — unlike collapsedText (which flattens a whole subtree to a
// single line), it preserves paragraph structure so the result paginates well.
// When skipHidden is set, elements hidden from a human (see IsHidden) are omitted
// too, so injected off-screen/hidden text does not reach the output. It is
// read-only (no mutation), so it is safe to call on a shared document.
func Text(doc *html.Node, skipHidden bool) string {
	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.ElementNode:
			switch n.Data {
			case "script", "style", "head", "noscript", "template":
				return
			case "br":
				sb.WriteByte('\n')
				return
			}
			if skipHidden && IsHidden(n) {
				return
			}
			block := textBlockTags[n.Data]
			if block {
				sb.WriteString("\n\n")
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			if block {
				sb.WriteString("\n\n")
			}
		case html.TextNode:
			if s := strings.Join(strings.Fields(n.Data), " "); s != "" {
				sb.WriteString(s)
				sb.WriteByte(' ')
			}
		default:
			// DocumentNode and other containers: recurse into children.
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
	}
	walk(doc)
	return normalizeText(sb.String())
}

// normalizeText trims each line and collapses runs of blank lines to one.
func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			if blank == 0 {
				out = append(out, "")
			}
			blank++
			continue
		}
		blank = 0
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
