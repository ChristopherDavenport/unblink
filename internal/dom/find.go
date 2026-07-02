package dom

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

// Hit is a single in-page search match: a snippet of surrounding text and the
// heading path (e.g. "Guide > Installation") that locates it in the document.
type Hit struct {
	Snippet     string `json:"snippet"`
	HeadingPath string `json:"heading_path,omitempty"`
	Index       int    `json:"index"`
}

// Find searches the visible text of doc for query (case-insensitive) and returns
// up to maxHits matches, each with a snippet and the heading path under which it
// appears. Script, style, and head content are skipped.
func Find(doc *html.Node, query string, maxHits int) []Hit {
	q := strings.ToLower(strings.TrimSpace(query))
	if doc == nil || q == "" {
		return nil
	}
	if maxHits <= 0 {
		maxHits = 10
	}

	var hits []Hit
	var stack []page.Heading // current heading path, innermost last

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if len(hits) >= maxHits {
			return
		}
		switch n.Type {
		case html.ElementNode:
			switch n.Data {
			case "script", "style", "head", "noscript", "template":
				return
			}
			if lvl := headingLevel(n.Data); lvl > 0 {
				for len(stack) > 0 && stack[len(stack)-1].Level >= lvl {
					stack = stack[:len(stack)-1]
				}
				stack = append(stack, page.Heading{Level: lvl, Text: collapsedText(n)})
			}
		case html.TextNode:
			lower, back := findLower(n.Data)
			if i := strings.Index(lower, q); i >= 0 {
				start, qlen := i, len(q)
				if back != nil {
					// Lowercasing shifted byte offsets (non-ASCII / invalid UTF-8):
					// map the match back to offsets valid in the original text.
					start = back[i]
					qlen = back[i+len(q)] - start
				}
				hits = append(hits, Hit{
					Snippet:     snippet(n.Data, start, qlen),
					HeadingPath: headingPath(stack),
					Index:       len(hits),
				})
				return
			}
		}
		for c := n.FirstChild; c != nil && len(hits) < maxHits; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return hits
}

func headingPath(stack []page.Heading) string {
	parts := make([]string, 0, len(stack))
	for _, h := range stack {
		if t := strings.TrimSpace(h.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " > ")
}

// findLower lowercases s for case-insensitive matching. For pure-ASCII text
// (the common case) byte offsets are unchanged and the returned map is nil.
// Otherwise lowering can change byte lengths — invalid bytes fold to the
// 3-byte U+FFFD, and some case mappings resize — so a byte index found in the
// lowered string is NOT valid in s; the returned slice maps every lowered
// byte offset (plus one-past-end) back to its originating offset in s.
func findLower(s string) (string, []int) {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s), nil
	}
	var b strings.Builder
	b.Grow(len(s))
	back := make([]int, 0, len(s)+1)
	for i, r := range s {
		lr := unicode.ToLower(r)
		for n := utf8.RuneLen(lr); n > 0; n-- {
			back = append(back, i)
		}
		b.WriteRune(lr)
	}
	back = append(back, len(s))
	return b.String(), back
}

// snippet returns ~80 runes of context on each side of a match, whitespace
// collapsed, with ellipses where text was trimmed. Offsets are clamped, never
// trusted — the text comes from an untrusted page.
func snippet(text string, byteIdx, qlen int) string {
	const ctx = 80
	if byteIdx < 0 {
		byteIdx = 0
	}
	if byteIdx > len(text) {
		byteIdx = len(text)
	}
	if qlen < 0 {
		qlen = 0
	}
	if byteIdx+qlen > len(text) {
		qlen = len(text) - byteIdx
	}
	r := []rune(text)
	// Convert byte offsets of the match into rune offsets.
	matchRune := len([]rune(text[:byteIdx]))
	matchLen := len([]rune(text[byteIdx : byteIdx+qlen]))
	start := matchRune - ctx
	if start < 0 {
		start = 0
	}
	end := matchRune + matchLen + ctx
	if end > len(r) {
		end = len(r)
	}
	s := strings.Join(strings.Fields(string(r[start:end])), " ")
	if start > 0 {
		s = "… " + s
	}
	if end < len(r) {
		s = s + " …"
	}
	return s
}
