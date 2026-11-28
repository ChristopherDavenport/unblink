package reduce

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

// The rule on numbers alone. Sizes are bytes of visible text.
func TestIsPartial(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		kept, keptLinks, keptAll int
		m                        dom.ContentMeasure
		partial, wholePage       bool
	}{
		{
			name: "article is nearly the whole content region",
			kept: 9000, keptAll: 9000,
			m: dom.ContentMeasure{Region: 9500, Page: 14000},
		},
		{
			name: "a fragment of a hub: under half",
			kept: 900, keptAll: 900,
			m:       dom.ContentMeasure{Region: 7000, RegionLinks: 3000, Page: 9000},
			partial: true,
		},
		{
			name: "under two thirds, the rest is prose (comments)",
			kept: 6000, keptAll: 6000,
			m: dom.ContentMeasure{Region: 10000, RegionLinks: 300, Page: 12000},
		},
		{
			name: "under two thirds, the rest is a river of links",
			kept: 6000, keptLinks: 200, keptAll: 6000,
			m:       dom.ContentMeasure{Region: 10000, RegionLinks: 3800, Page: 12000},
			partial: true,
		},
		{
			name: "two thirds or more is an article even beside a link list",
			kept: 7000, keptAll: 7000,
			m: dom.ContentMeasure{Region: 10000, RegionLinks: 3000, Page: 12000},
		},
		{
			name: "small loss is never acted on",
			kept: 300, keptAll: 300,
			m: dom.ContentMeasure{Region: 1200, RegionLinks: 900, Page: 1500},
		},
		{
			name: "extraction came from chrome and is a sliver of the page",
			kept: 450, keptAll: 450,
			m:       dom.ContentMeasure{Region: 40, Page: 3000},
			partial: true, wholePage: true,
		},
		{
			// A page whose <main> wraps one small widget while the real content
			// (and a table of contents marked up as <nav>) sits outside it. The
			// extraction holds nearly everything visible, so it stands — it is
			// compared with the page on equal terms, chrome counted on both sides.
			name: "extraction outside a mislabelled main holds nearly the whole page",
			kept: 420, keptAll: 10000,
			m:         dom.ContentMeasure{Region: 280, Page: 10300},
			wholePage: true,
		},
		{
			name: "short article, heavy chrome, no main landmark trouble",
			kept: 400, keptAll: 400,
			m: dom.ContentMeasure{Region: 420, Page: 9000},
		},
		{
			name: "empty page",
			m:    dom.ContentMeasure{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			partial, wholePage := isPartial(tc.kept, tc.keptLinks, tc.keptAll, tc.m)
			if partial != tc.partial || wholePage != tc.wholePage {
				t.Errorf("isPartial = %v, %v; want %v, %v", partial, wholePage, tc.partial, tc.wholePage)
			}
		})
	}
}
