// Package reduce performs unblink's semantic reduction: it strips visual-only
// junk (scripts, styles, nav, ads, hidden elements) and extracts the meaningful
// content of a page. The primary strategy (Article) is a Readability port; Full
// keeps the whole page. Both produce a sanitized HTML subtree that the emit stage
// serializes to Markdown, with links rewritten to absolute URLs.
package reduce

import (
	"bytes"
	"fmt"

	readability "codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Article extracts the main readable content of p.Doc into p.Article.
//
// Readability operates on a clone of the document, so the canonical p.Doc is left
// untouched and remains available to other stages. If readability finds no
// distinct article body, Article falls back to Full. When stripHidden is set,
// human-hidden subtrees and comments are removed before sanitizing, so injected
// off-screen/hidden text does not reach the emitted Markdown.
func Article(p *page.Page, stripHidden bool) error {
	if p.Doc == nil {
		return fmt.Errorf("reduce: page has no parsed document")
	}

	art, err := readability.FromDocument(p.Doc, p.FinalURL)
	if err != nil || art.Node == nil {
		return Full(p, stripHidden)
	}

	// art.Node is readability's clone; rewriting/pruning it in place is safe.
	dom.AbsolutizeURLs(art.Node, dom.BaseURL(p))
	if stripHidden {
		dom.StripHidden(art.Node)
	}

	cleanHTML, err := sanitizeNode(art.Node)
	if err != nil {
		return fmt.Errorf("reduce: sanitize article: %w", err)
	}

	p.Article = &page.Article{
		Title:       art.Title(),
		Byline:      art.Byline(),
		Excerpt:     art.Excerpt(),
		SiteName:    art.SiteName(),
		ContentHTML: cleanHTML,
		ContentNode: art.Node,
		TextLength:  len(cleanHTML),
		Source:      "readability",
	}
	if p.Meta.Title == "" {
		p.Meta.Title = art.Title()
	}
	return nil
}

// Full reduces the entire page (no main-content extraction) into p.Article. It
// renders and re-parses p.Doc into an independent tree so the canonical document
// is never mutated by URL rewriting. When stripHidden is set, human-hidden subtrees
// and comments are removed before sanitizing. Large link-dense regions that repeat
// verbatim (a desktop nav plus its mobile-drawer twin) are emitted once.
func Full(p *page.Page, stripHidden bool) error {
	if p.Doc == nil {
		return fmt.Errorf("reduce: page has no parsed document")
	}

	var buf bytes.Buffer
	if err := html.Render(&buf, p.Doc); err != nil {
		return fmt.Errorf("reduce: render document: %w", err)
	}
	clone, err := html.Parse(&buf)
	if err != nil {
		return fmt.Errorf("reduce: reparse document: %w", err)
	}
	dom.AbsolutizeURLs(clone, dom.BaseURL(p))
	if stripHidden {
		dom.StripHidden(clone)
	}
	dom.StripDuplicateBlocks(clone)

	cleanHTML, err := sanitizeNode(clone)
	if err != nil {
		return fmt.Errorf("reduce: sanitize full: %w", err)
	}
	p.Article = &page.Article{
		Title:       p.Meta.Title,
		ContentHTML: cleanHTML,
		ContentNode: clone,
		TextLength:  len(cleanHTML),
		Source:      "full",
	}
	return nil
}
