package reduce

import (
	"bytes"
	"fmt"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// sanitizePolicy is the allowlist applied to extracted content. We emit Markdown
// for an AI rather than rendering HTML, so this primarily removes scripts,
// styles, event handlers, and other non-content noise — XSS is not the threat
// model, signal-to-noise is.
func sanitizePolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowImages()
	p.AllowAttrs("title").OnElements("abbr", "a")
	return p
}

// sanitizeNode renders an *html.Node to HTML, runs it through the sanitizer, and
// returns the cleaned HTML string.
func sanitizeNode(n *html.Node) (string, error) {
	var buf bytes.Buffer
	if err := html.Render(&buf, n); err != nil {
		return "", fmt.Errorf("render node: %w", err)
	}
	return sanitizePolicy().Sanitize(buf.String()), nil
}
