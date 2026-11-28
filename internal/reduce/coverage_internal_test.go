package reduce

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func sentences(tag string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "<p>%s sentence %d says one plain thing about the subject, at ordinary length.</p>", tag, i)
	}
	return b.String()
}

// find returns the first element with the given id.
func find(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, id); f != nil {
			return f
		}
	}
	return nil
}

// An accepted extraction reports its coverage only when it left a noticeable
// part of the content region out.
func TestPartialArticleCoverage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		article     int // sentences inside the extraction
		rest        int // sentences of prose in the content region outside it
		wantPartial bool
		wantCov     float64 // 0 = not reported
	}{
		{"the article is the page", 40, 0, false, 0},
		{"a short aside is not worth a note", 40, 5, false, 0},
		{"a second section is left out", 40, 16, false, 40.0 / 56},
		{"just above the note threshold", 40, 6, false, 0},
		{"a fragment is a fallback, with no coverage", 10, 40, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := html.Parse(strings.NewReader(`<html><body><nav><a href="/">Home</a></nav><main>` +
				`<article id="a">` + sentences("ART", tc.article) + `</article>` +
				`<section>` + sentences("REST", tc.rest) + `</section></main></body></html>`))
			if err != nil {
				t.Fatal(err)
			}
			_, partial, cov := partialArticle(doc, find(doc, "a"))
			if partial != tc.wantPartial {
				t.Fatalf("partial = %t, want %t", partial, tc.wantPartial)
			}
			if tc.wantCov == 0 && cov != 0 {
				t.Errorf("coverage = %.3f, want none reported", cov)
			}
			if tc.wantCov != 0 && math.Abs(cov-tc.wantCov) > 0.03 {
				t.Errorf("coverage = %.3f, want about %.3f", cov, tc.wantCov)
			}
		})
	}
}
