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

// ugcPolicy is built once and shared: policy construction compiles a large
// allowlist/regexp set, and a bluemonday policy is safe for concurrent Sanitize
// as long as it is never modified after construction. Any future per-request
// customization must clone/build its own policy, not mutate this one.
var ugcPolicy = sanitizePolicy()

// sanitizeNode renders an *html.Node to HTML, runs it through the sanitizer, and
// returns the cleaned HTML string.
func sanitizeNode(n *html.Node) (string, error) {
	var buf bytes.Buffer
	if err := html.Render(&buf, n); err != nil {
		return "", fmt.Errorf("render node: %w", err)
	}
	return ugcPolicy.Sanitize(buf.String()), nil
}
