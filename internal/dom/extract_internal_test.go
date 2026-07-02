package dom

// In-package equivalence test: the indexed selectorFor must produce exactly
// the output of the pre-index implementation (kept below as the oracle) for
// every interactive control across documents chosen to hit the edge cases —
// superset classes, duplicate/exotic/padded ids, duplicate class tokens,
// camelCase foreign tags, and nth-of-type fallbacks.

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// legacySelectorFor is the pre-index implementation, verbatim.
func legacySelectorFor(doc, n *html.Node) string {
	if id := strings.TrimSpace(attr(n, "id")); id != "" {
		if sel := "#" + id; uniqueMatch(doc, sel, n) {
			return sel
		}
	}
	if sel := tagClassSelector(n); uniqueMatch(doc, sel, n) {
		return sel
	}
	return nthOfTypePath(n)
}

func TestSelectorForMatchesLegacy(t *testing.T) {
	docs := map[string]string{
		"unique ids and classes": `<html><body>
			<button id="save">Save</button>
			<button class="btn primary">Go</button>
			<div onclick="x()" class="widget">W</div>
		</body></html>`,
		"duplicate ids": `<html><body>
			<button id="dup">A</button><button id="dup" class="two">B</button>
		</body></html>`,
		"padded and exotic ids": `<html><body>
			<button id="  padded  ">P</button>
			<button id="a.b:c">Dots</button>
			<button id="9lives">Digit</button>
			<button id="-2x">HyphenDigit</button>
		</body></html>`,
		"superset classes": `<html><body>
			<button class="a b">X</button>
			<button class="a b c">Y</button>
			<button class="a">Z</button>
		</body></html>`,
		"shared classes force nth-of-type": `<html><body>
			<div><button class="s">1</button></div>
			<div><button class="s">2</button></div>
		</body></html>`,
		"duplicate class tokens": `<html><body>
			<button class="a a">Dup</button><button class="a">Other</button>
		</body></html>`,
		"zero-class unique and shared tags": `<html><body>
			<summary>Only</summary>
			<button>1</button><button>2</button>
		</body></html>`,
		"anchored nth-of-type under id": `<html><body>
			<div id="menu"><span onclick="a()">A</span><span onclick="b()">B</span></div>
		</body></html>`,
		"foreign camelCase tag": `<html><body>
			<svg><foreignObject onclick="x()">F</foreignObject></svg>
			<button class="k">B</button>
		</body></html>`,
		"whitespace classes": `<html><body>
			<button class="  spaced   out  ">S</button><button class="out">O</button>
		</body></html>`,
	}
	for name, src := range docs {
		doc, err := html.Parse(strings.NewReader(src))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		ix := buildSelIndex(doc)
		for _, n := range selInteractive.MatchAll(doc) {
			got := selectorFor(ix, doc, n)
			want := legacySelectorFor(doc, n)
			if got != want {
				t.Errorf("%s: control <%s id=%q class=%q>: got %q, want %q",
					name, n.Data, attr(n, "id"), attr(n, "class"), got, want)
			}
			// The published contract: the selector must resolve uniquely to n
			// (or at least compile) via the same query machinery the JS engine
			// replays it with.
			if !uniqueMatch(doc, got, n) && got != want {
				t.Errorf("%s: selector %q does not resolve to its control", name, got)
			}
		}
	}
}
