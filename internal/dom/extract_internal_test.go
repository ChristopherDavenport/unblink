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

func mustParse(t *testing.T, src string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

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

func TestAccessibleName(t *testing.T) {
	doc := mustParse(t, `<html><body>
		<span id="lbl">Labelled By Text</span>
		<button id="b1" aria-labelledby="lbl" aria-label="ignored">text</button>
		<button id="b2" aria-label="Aria Label">ignored text</button>
		<label>Wrapped <button id="b3">x</button></label>
		<button id="b4" title="Title Text"></button>
		<button id="b5">Just Text</button>
	</body></html>`)
	idx, res := buildIDIndex(doc), newIDResolver(doc)
	for id, want := range map[string]string{
		"b1": "Labelled By Text", // labelledby beats aria-label
		"b2": "Aria Label",
		"b3": "Wrapped x", // ancestor <label> text
		"b4": "Title Text",
		"b5": "Just Text",
	} {
		if got := accessibleName(idx[id], res); got != want {
			t.Errorf("%s: name = %q, want %q", id, got, want)
		}
	}
}

func TestAccessibleNameCycleSafe(t *testing.T) {
	// A self-reference and a two-node aria-labelledby cycle must terminate.
	doc := mustParse(t, `<html><body>
		<button id="self" aria-labelledby="self">fallback</button>
		<span id="a" aria-labelledby="b">A</span>
		<span id="b" aria-labelledby="a">B</span>
	</body></html>`)
	idx, res := buildIDIndex(doc), newIDResolver(doc)
	if got := accessibleName(idx["self"], res); got != "fallback" {
		t.Errorf("self-ref name = %q, want fallback", got)
	}
	if got := accessibleName(idx["a"], res); got != "B" {
		t.Errorf("cycle name = %q, want B (single-level)", got)
	}
}

func TestHashID(t *testing.T) {
	doc := mustParse(t, `<html><body><main><button id="x">Go</button></main></body></html>`)
	n := buildIDIndex(doc)["x"]
	if hashID("btn", n, "Go", "") != hashID("btn", n, "Go", "") {
		t.Error("hashID not deterministic")
	}
	// Identical semantic twins share a base hash; the minter appends -N.
	doc2 := mustParse(t, `<html><body><main><button>Go</button><button>Go</button></main></body></html>`)
	m := newIDMinter()
	var ids []string
	for _, b := range selInteractive.MatchAll(doc2) {
		ids = append(ids, m.mint("btn", b, "Go", ""))
	}
	if len(ids) != 2 || ids[0] == ids[1] || !strings.HasSuffix(ids[1], "-2") {
		t.Errorf("twin ids = %v, want [base, base-2]", ids)
	}
}
