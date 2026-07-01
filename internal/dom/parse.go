// Package dom is the only package that imports golang.org/x/net/html (and, in
// later phases, goquery/cascadia). It turns fetched bytes into the canonical
// *html.Node tree and will grow traversal/extraction helpers over it.
package dom

import (
	"bytes"
	"fmt"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

// Parse parses p.Raw into the canonical document tree, filling p.Doc.
func Parse(p *page.Page) error {
	if len(p.Raw) == 0 {
		return fmt.Errorf("dom: empty response body")
	}
	doc, err := html.Parse(bytes.NewReader(p.Raw))
	if err != nil {
		return fmt.Errorf("dom: parse html: %w", err)
	}
	p.Doc = doc
	return nil
}
