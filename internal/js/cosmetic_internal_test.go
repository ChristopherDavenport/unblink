package js

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestHidingSelectors(t *testing.T) {
	css := `
		/* a comment { display:none } should be ignored */
		.ad-banner, #sponsored { color: red; display: none !important; }
		.promo { visibility: hidden; }
		.keep { color: blue; }
		@media screen and (min-width: 1px) { .m-ad { display:none } }
		@font-face { font-family: x; }
	`
	got := hidingSelectors(css)
	want := map[string]bool{".ad-banner": true, "#sponsored": true, ".promo": true, ".m-ad": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d selectors", got, len(want))
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected hiding selector %q", s)
		}
	}
}

func TestApplyCosmeticFilters(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body>
		<div class="ad-banner">ad</div>
		<article id="content">keep me</article>
		<div id="sponsored"><p>sponsored</p></div>
		<div class="keep">keep</div>
	</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	b := &bridge{}
	b.addCosmeticCSS(`.ad-banner, #sponsored { display:none } .keep { color:red }`)

	if n := b.applyCosmeticFilters(doc); n != 2 {
		t.Errorf("removed %d nodes, want 2", n)
	}
	var buf strings.Builder
	if err := html.Render(&buf, doc); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "ad-banner") || strings.Contains(out, "sponsored") {
		t.Errorf("hidden nodes should be removed:\n%s", out)
	}
	if !strings.Contains(out, "keep me") || !strings.Contains(out, `class="keep"`) {
		t.Errorf("non-hidden content should survive:\n%s", out)
	}
}

func TestAddCosmeticCSSDeduplicates(t *testing.T) {
	b := &bridge{}
	b.addCosmeticCSS(`.ad { display:none }`)
	b.addCosmeticCSS(`.ad { display:none } .ad2 { visibility:hidden }`)
	if len(b.cosmeticSelectors) != 2 {
		t.Errorf("cosmeticSelectors = %v, want 2 unique", b.cosmeticSelectors)
	}
}

// FuzzHidingSelectors ensures the brace-balancing CSS scanner never panics on
// extension-supplied stylesheet bytes, and that every selector it emits compiles or
// errors cleanly (never panics) through the shared selector cache.
func FuzzHidingSelectors(f *testing.F) {
	f.Add(".ad{display:none}")
	f.Add("@media x { .a, .b { visibility:hidden } }")
	f.Add("/* c */ .x{color:red")
	f.Add("}}}{{{ display:none")
	f.Fuzz(func(t *testing.T, css string) {
		for _, sel := range hidingSelectors(css) {
			_, _ = compileSelector(sel)
		}
	})
}
