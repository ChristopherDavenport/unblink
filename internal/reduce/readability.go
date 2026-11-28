// Package reduce performs unblink's semantic reduction: it strips visual-only
// junk (scripts, styles, nav, ads, hidden elements) and extracts the meaningful
// content of a page. The primary strategy (Article) is a Readability port; Full
// keeps the whole page. Both produce a sanitized HTML subtree that the emit stage
// serializes to Markdown, with links rewritten to absolute URLs.
package reduce

import (
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
// distinct article body, or one that holds only a small part of the page's
// content (a hub or listing — see partialArticle), Article falls back to the
// page's main landmark or to Full and records why in Article.Fallback. When stripHidden is set,
// human-hidden subtrees and comments are removed before sanitizing, so injected
// off-screen/hidden text does not reach the emitted Markdown.
func Article(p *page.Page, stripHidden bool) error {
	if p.Doc == nil {
		return fmt.Errorf("reduce: page has no parsed document")
	}

	// Strip hidden subtrees BEFORE readability extracts. Readability drops inline
	// style attributes while cleaning, which erases the off-screen-positioning
	// signal (left:-9999px, clip:rect(0…)) that StripHidden keys on — so if the
	// strip only ran after extraction (on art.Node), off-screen "screen-reader"
	// text would survive with its style gone and its prose intact. Removing
	// hidden nodes up front closes that gap. p.Doc is shared with the cached/
	// session page (Read passes a shallow page copy), so strip a clone, never
	// p.Doc itself. readability clones its input internally, so p.Doc stays
	// canonical either way. Clone only when the page actually has hidden nodes —
	// the common page carries none and skips the copy entirely.
	src := p.Doc
	if stripHidden && dom.HasHidden(p.Doc) {
		src = dom.CloneTree(p.Doc)
		dom.StripHidden(src)
	}

	art, err := readability.FromDocument(src, p.FinalURL)
	if err != nil || art.Node == nil {
		return fallback(p, stripHidden, nil, page.FallbackNoArticle)
	}

	// Readability always returns its best-scoring block, even on a page that is
	// not an article: on a section front or a link directory that block is one
	// card or the footer blurb. Check the extraction against the page before
	// trusting it (see partialArticle).
	if main, partial := partialArticle(src, art.Node); partial {
		return fallback(p, stripHidden, main, page.FallbackPartialArticle)
	}

	// art.Node is readability's clone; rewriting/pruning it in place is safe. The
	// post-extraction strip is defence-in-depth: the pre-strip already removed
	// hidden nodes, but readability can restructure the tree, so re-run it on the
	// small extracted subtree.
	dom.AbsolutizeURLs(art.Node, dom.BaseURL(p))
	if stripHidden {
		dom.StripHidden(art.Node)
	}
	dom.FlattenSVGText(art.Node)
	dom.MarkCollapsedDetails(art.Node)
	dom.SeparateInlineSiblings(art.Node)

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

// The partial-article rule. An extraction is trusted as "the article" only if
// it accounts for most of the page's content; otherwise the page is a hub,
// listing or index and returning the fragment would silently drop the rest.
//
// "The page's content" is the content region measured by dom.MeasureContent:
// the main landmark when the page declares one, otherwise the body, with page
// chrome (navigation, complementary, banner, contentinfo, search) left out — so
// a true article is never penalised for the site's menus, sidebar or footer.
const (
	// partialCoverage: an extraction holding under half of the content region is
	// not the page's article, whatever the rest is.
	partialCoverage = 0.50
	// listingCoverage / listingLinkShare: one holding under two thirds is still
	// not, when what it left out is mostly link text — the rest of a story river
	// or a file listing. Left-out prose in that band (comments, a second section)
	// is tolerated: an article page can legitimately carry it.
	listingCoverage  = 2.0 / 3
	listingLinkShare = 0.50
	// partialMinDropped: below this much left-out text (~250 tokens) the loss is
	// too small to act on, and tiny pages must not flip on measurement noise.
	partialMinDropped = 1000
	// chromeSlack: an extraction more than this much larger than the whole
	// content region did not come from it — readability picked a block inside
	// chrome (a footer "about" blurb, a sidebar). It is then measured against
	// all of the page's visible text instead.
	chromeSlack = 1.2
)

// partialArticle reports whether art, extracted from doc, fails the rule above.
// When it does and the page has a main landmark that holds the content, main is
// that landmark (a node of doc); a nil main means "fall back to the whole page".
func partialArticle(doc, art *html.Node) (main *html.Node, partial bool) {
	m := dom.MeasureContent(doc)
	kept, keptLinks, keptAll := dom.ContentText(art)
	partial, wholePage := isPartial(kept, keptLinks, keptAll, m)
	if !partial || wholePage {
		return nil, partial
	}
	return m.Main, true
}

// isPartial is the arithmetic of the rule, separated so it reads on numbers
// alone. kept/keptLinks are the extraction's content text and its link share,
// keptAll every visible byte of it. wholePage reports that the extraction came
// from page chrome, so the content region is not where the content is and the
// fallback must be the whole page.
func isPartial(kept, keptLinks, keptAll int, m dom.ContentMeasure) (partial, wholePage bool) {
	region, regionLinks := m.Region, m.RegionLinks
	if float64(kept) > chromeSlack*float64(region) {
		// The region does not contain the extraction, so it cannot be what the
		// extraction is a part of. Compare everything the extraction holds with
		// everything visible on the page, chrome included on both sides.
		kept, region, wholePage = keptAll, m.Page, true
	}
	dropped := region - kept
	if dropped < partialMinDropped {
		return false, wholePage
	}
	coverage := float64(kept) / float64(region)
	if coverage < partialCoverage {
		return true, wholePage
	}
	if wholePage || coverage >= listingCoverage {
		return false, wholePage
	}
	droppedLinks := regionLinks - keptLinks
	return float64(droppedLinks) >= listingLinkShare*float64(dropped), false
}

// fallback produces the wider reduction for an article request that could not
// be honoured: the page's main landmark when main is set, otherwise the whole
// page. why is recorded on the Article so the caller can report it.
func fallback(p *page.Page, stripHidden bool, main *html.Node, why string) error {
	var err error
	if main != nil {
		err = mainRegion(p, main, stripHidden)
	} else {
		err = Full(p, stripHidden)
	}
	if err == nil && p.Article != nil {
		p.Article.Fallback = why
	}
	return err
}

// mainRegion reduces just the page's main landmark into p.Article: the page
// minus its chrome, for a page that has content but no single article. main is
// cloned, so neither p.Doc nor a stripped copy of it is mutated.
func mainRegion(p *page.Page, main *html.Node, stripHidden bool) error {
	clone := dom.CloneTree(main)
	dom.AbsolutizeURLs(clone, dom.BaseURL(p))
	if stripHidden {
		dom.StripHidden(clone)
	}
	dom.StripDuplicateBlocks(clone)
	dom.FlattenSVGText(clone)
	dom.MarkCollapsedDetails(clone)
	dom.SeparateInlineSiblings(clone)

	cleanHTML, err := sanitizeNode(clone)
	if err != nil {
		return fmt.Errorf("reduce: sanitize main: %w", err)
	}
	p.Article = &page.Article{
		Title:       p.Meta.Title,
		ContentHTML: cleanHTML,
		ContentNode: clone,
		TextLength:  len(cleanHTML),
		Source:      "main",
	}
	return nil
}

// Full reduces the entire page (no main-content extraction) into p.Article. It
// deep-clones p.Doc into an independent tree so the canonical document is never
// mutated by URL rewriting. When stripHidden is set, human-hidden subtrees and
// comments are removed before sanitizing. Large link-dense regions that repeat
// verbatim (a desktop nav plus its mobile-drawer twin) are emitted once.
func Full(p *page.Page, stripHidden bool) error {
	if p.Doc == nil {
		return fmt.Errorf("reduce: page has no parsed document")
	}

	clone := dom.CloneTree(p.Doc)
	dom.AbsolutizeURLs(clone, dom.BaseURL(p))
	if stripHidden {
		dom.StripHidden(clone)
	}
	dom.StripDuplicateBlocks(clone)
	dom.FlattenSVGText(clone)
	dom.MarkCollapsedDetails(clone)
	dom.SeparateInlineSiblings(clone)

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
