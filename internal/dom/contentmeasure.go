package dom

import (
	"strings"

	"golang.org/x/net/html"
)

// ContentMeasure sizes a document's content for the "is this page an article?"
// check in reduce: how much visible text the page carries, how much of it sits
// outside page chrome, and how much of that is link text. Sizes are bytes of
// visible text with whitespace runs dropped, so markup and indentation never
// count.
type ContentMeasure struct {
	// Main is the document's main landmark (<main> or role=main), or nil when the
	// page declares none or declares more than one.
	Main *html.Node
	// Page is all visible text, chrome included.
	Page int
	// Region is the visible text of the content region — Main when there is one,
	// otherwise the whole body — with page chrome left out: navigation,
	// complementary, banner, contentinfo and search landmarks.
	Region int
	// RegionLinks is the part of Region that is link text.
	RegionLinks int
}

// MeasureContent measures root in one read-only, allocation-free walk (two when
// the page has a main landmark). It never mutates, so it is safe on a shared
// document. Hidden elements (see IsHidden) are not counted.
func MeasureContent(root *html.Node) ContentMeasure {
	return measureContent(root, false)
}

// MeasureDocument is MeasureContent over everything the document holds rather
// than what a reader sees: text inside human-hidden elements counts too. It is
// the measure for "did this document lose its content?" (the render-collapse
// guard), where a script collapsing an accordion or switching a tab — hiding
// text that is still there — must not look like a loss.
func MeasureDocument(root *html.Node) ContentMeasure {
	return measureContent(root, true)
}

func measureContent(root *html.Node, hidden bool) ContentMeasure {
	var m ContentMeasure
	if root == nil {
		return m
	}
	mains := 0
	var first *html.Node
	var page textSize
	measure(root, false, false, false, hidden, &page, func(n *html.Node) {
		mains++
		if first == nil {
			first = n
		}
	})
	m.Page = page.all
	m.Region, m.RegionLinks = page.content, page.contentLinks
	if mains == 1 {
		m.Main = first
		var region textSize
		measure(first, true, false, false, hidden, &region, nil)
		m.Region, m.RegionLinks = region.content, region.contentLinks
	}
	return m
}

// ContentText sizes a content subtree — an extracted article — exactly as
// MeasureContent sizes a page: text is its visible text with chrome landmarks
// inside it left out (comparable to ContentMeasure.Region; a header or footer
// counts as content because the subtree is itself a section), links the part of
// that which is link text, and all every visible byte (comparable to
// ContentMeasure.Page).
func ContentText(n *html.Node) (text, links, all int) {
	if n == nil {
		return 0, 0, 0
	}
	var s textSize
	measure(n, true, false, false, false, &s, nil)
	return s.content, s.contentLinks, s.all
}

type textSize struct {
	all          int // every visible byte
	content      int // visible bytes outside chrome
	contentLinks int // of content, the bytes inside <a>
}

// measure walks n accumulating visible text sizes. scoped is true once inside a
// sectioning element, where header/footer are generic rather than the page's
// banner/contentinfo (ARIA scoping — the same rule landmarkRole applies).
// chrome is true inside a chrome landmark; link is true inside <a>; hidden makes
// human-hidden elements count. onMain, when set, is called for every main
// landmark found outside chrome.
func measure(n *html.Node, scoped, chrome, link, hidden bool, s *textSize, onMain func(*html.Node)) {
	switch n.Type {
	case html.TextNode:
		size := visibleBytes(n.Data)
		s.all += size
		if !chrome {
			s.content += size
			if link {
				s.contentLinks += size
			}
		}
		return
	case html.ElementNode:
		switch n.Data {
		case "script", "style", "head", "noscript", "template":
			return
		}
		if !hidden && IsHidden(n) {
			return
		}
		switch contentRole(n, scoped) {
		case "chrome":
			chrome = true
		case "main":
			if onMain != nil && !chrome {
				onMain(n)
			}
			scoped = true
		case "section":
			scoped = true
		}
		if n.Data == "a" {
			link = true
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		measure(c, scoped, chrome, link, hidden, s, onMain)
	}
}

// contentRole classifies an element for the content measure: "chrome" for a
// navigation/complementary/banner/contentinfo/search landmark, "main" for the
// main landmark, "section" for an element that scopes header/footer, "" for
// anything else. An explicit role attribute wins over the tag, as in ARIA.
func contentRole(n *html.Node, scoped bool) string {
	for _, a := range n.Attr {
		if a.Key != "role" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(a.Val)) {
		case "navigation", "complementary", "banner", "contentinfo", "search":
			return "chrome"
		case "main":
			return "main"
		case "article", "region":
			return "section"
		}
		break
	}
	switch n.Data {
	case "nav", "aside":
		return "chrome"
	case "header", "footer":
		if !scoped {
			return "chrome"
		}
	case "main":
		return "main"
	case "article", "section":
		return "section"
	}
	return ""
}

// visibleBytes is the length of s with all whitespace removed: a stable size
// for "how much text is here" that indentation and line wrapping cannot move.
func visibleBytes(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\f':
		default:
			n++
		}
	}
	return n
}
