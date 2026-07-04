package js

import (
	"strings"

	"golang.org/x/net/html"
)

// Cosmetic filtering: extensions hide ad markup with element-hiding CSS (uBlock's
// `##selector` rules, AdGuard's cosmetic filters, plain content-script stylesheets).
// unblink has no CSSOM — `display:none` is observably a no-op — so it turns a hiding
// rule into *physical removal* of the matched nodes, which is exactly what a semantic
// reducer wants: the ad is gone from the extracted Markdown.
//
// Selectors are gathered from every loaded extension's content-script CSS (and, from
// Phase 2c, scripting.insertCSS) into bridge.cosmeticSelectors during injection, then
// applied once the DOM is frozen — post-Terminate for one-shot renders and on the
// snapshot clone for live sessions — so page JavaScript never observes the removal
// (mirroring how CSS hiding fires no mutation events).

// addCosmeticCSS parses a stylesheet and records the selectors of any rule that hides
// its subjects, de-duplicated against what is already gathered.
func (b *bridge) addCosmeticCSS(css string) {
	for _, sel := range hidingSelectors(css) {
		if !b.seenCosmetic[sel] {
			if b.seenCosmetic == nil {
				b.seenCosmetic = map[string]bool{}
			}
			b.seenCosmetic[sel] = true
			b.cosmeticSelectors = append(b.cosmeticSelectors, sel)
		}
	}
}

// applyCosmeticFilters detaches from doc every element matched by a gathered hiding
// selector, returning how many nodes were removed. Selectors cascadia cannot compile
// (e.g. procedural `:has-text()`) are skipped by queryAll.
func (b *bridge) applyCosmeticFilters(doc *html.Node) int {
	removed := 0
	for _, sel := range b.cosmeticSelectors {
		for _, n := range queryAll(doc, sel) {
			if n.Parent != nil {
				detach(n)
				removed++
			}
		}
	}
	return removed
}

// hidingSelectors returns the selectors of every CSS rule whose declaration block
// hides its subject (display:none / visibility:hidden|collapse). It is a lightweight
// scanner, not a full CSS parser: it balances braces (so @media/@supports rules are
// descended into) and splits selector lists on commas — enough for the simple
// element-hiding stylesheets cosmetic filters ship.
func hidingSelectors(css string) []string {
	css = stripCSSComments(css)
	var out []string
	for i := 0; i < len(css); {
		open := strings.IndexByte(css[i:], '{')
		if open < 0 {
			break
		}
		prelude := strings.TrimSpace(css[i : i+open])
		// Find the matching close brace, honoring nesting.
		depth, j := 1, i+open+1
		for j < len(css) && depth > 0 {
			switch css[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			j++
		}
		// Body excludes the consumed closing brace when the block was balanced. Bounds
		// are clamped so an unterminated block or a trailing '{' can't slice out of range.
		bodyStart, bodyEnd := i+open+1, j
		if depth == 0 {
			bodyEnd = j - 1
		}
		if bodyStart > bodyEnd {
			bodyStart = bodyEnd
		}
		body := css[bodyStart:bodyEnd]
		i = j
		if strings.HasPrefix(prelude, "@") {
			lp := strings.ToLower(prelude)
			if strings.HasPrefix(lp, "@media") || strings.HasPrefix(lp, "@supports") {
				out = append(out, hidingSelectors(body)...)
			}
			continue
		}
		if !declarationHides(body) {
			continue
		}
		for _, s := range strings.Split(prelude, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// declarationHides reports whether a CSS declaration block visually hides its subject.
func declarationHides(decl string) bool {
	d := strings.ReplaceAll(strings.ToLower(decl), " ", "")
	return strings.Contains(d, "display:none") ||
		strings.Contains(d, "visibility:hidden") ||
		strings.Contains(d, "visibility:collapse")
}

// stripCSSComments removes /* ... */ comments.
func stripCSSComments(s string) string {
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+2+j+2:]
	}
}
