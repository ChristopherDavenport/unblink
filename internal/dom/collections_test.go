package dom_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
)

// collFields turns a detected collection's schema back into dom.Records input, so
// tests can prove the emitted {root, fields} round-trips through the executor.
func collFields(c page.Collection) map[string]dom.FieldSpec {
	m := make(map[string]dom.FieldSpec, len(c.Fields))
	for _, f := range c.Fields {
		m[f.Name] = dom.FieldSpec{Selector: f.Selector, Attr: f.Attr}
	}
	return m
}

func fieldNameByAttr(c page.Collection, attr string) (string, bool) {
	for _, f := range c.Fields {
		if f.Attr == attr {
			return f.Name, true
		}
	}
	return "", false
}

func hasFieldName(c page.Collection, name string) bool {
	for _, f := range c.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

const productList = `<main><ul>
  <li class="product"><h2 class="name">Widget</h2><span class="price">$9.99</span><a class="buy" href="/p/1" data-sku="W-1">Buy</a></li>
  <li class="product"><h2 class="name">Gadget</h2><span class="price">$19.99</span><a class="buy" href="/p/2" data-sku="G-2">Buy</a></li>
  <li class="product"><h2 class="name">Gizmo</h2><span class="price">$4.50</span><a class="buy" href="/p/3" data-sku="Z-3">Buy</a></li>
</ul></main>`

func TestCollectionsProductList(t *testing.T) {
	p := extractInline(t, productList)
	cs := p.Meta.Collections
	if len(cs) != 1 {
		t.Fatalf("got %d collections, want 1: %+v", len(cs), cs)
	}
	c := cs[0]
	if c.Region != "main" {
		t.Errorf("region = %q, want main", c.Region)
	}
	recs, _, err := dom.Records(p.Doc, c.Root, collFields(c), 200)
	if err != nil {
		t.Fatalf("root %q does not round-trip: %v", c.Root, err)
	}
	if len(recs) != 3 || c.Count != 3 {
		t.Fatalf("root %q -> %d records (count %d), want 3", c.Root, len(recs), c.Count)
	}
	for _, name := range []string{"name", "price"} {
		if !hasFieldName(c, name) {
			t.Errorf("missing field %q; fields=%+v", name, c.Fields)
		}
	}
	// The data-sku on the buy link must be captured even though <a> is a link.
	sku, ok := fieldNameByAttr(c, "data-sku")
	if !ok {
		t.Fatalf("no data-sku field; fields=%+v", c.Fields)
	}
	if recs[0][sku] != "W-1" || recs[1][sku] != "G-2" {
		t.Errorf("sku values = %q/%q, want W-1/G-2", recs[0][sku], recs[1][sku])
	}
	// A verbatim href field (relative, not URL-resolved).
	href, ok := fieldNameByAttr(c, "href")
	if !ok || recs[0][href] != "/p/1" {
		t.Errorf("href field = %q ok=%v, want /p/1", recs[0][href], ok)
	}
	if n, ok := fieldNameByAttr(c, ""); ok {
		_ = n // text fields exist (name/price); nothing more to assert here
	}
}

func TestCollectionsRegionRanking(t *testing.T) {
	// A nav link-list must NOT win over (or even join) a main product list.
	p := extractInline(t, `
	  <nav><ul>
	    <li><a href="/a">Alpha</a></li><li><a href="/b">Bravo</a></li>
	    <li><a href="/c">Charlie</a></li><li><a href="/d">Delta</a></li>
	    <li><a href="/e">Echo</a></li>
	  </ul></nav>`+productList)
	cs := p.Meta.Collections
	if len(cs) != 1 {
		t.Fatalf("got %d collections, want 1 (only the main list): %+v", len(cs), cs)
	}
	if cs[0].Region != "main" {
		t.Errorf("winning region = %q, want main", cs[0].Region)
	}
}

func TestCollectionsAttrFields(t *testing.T) {
	p := extractInline(t, `<main><ul>
	  <li class="item"><img class="thumb" src="/a.png"><time class="when" datetime="2020-01-01">Jan</time><span class="tag" data-id="7">news</span></li>
	  <li class="item"><img class="thumb" src="/b.png"><time class="when" datetime="2020-02-01">Feb</time><span class="tag" data-id="8">blog</span></li>
	  <li class="item"><img class="thumb" src="/c.png"><time class="when" datetime="2020-03-01">Mar</time><span class="tag" data-id="9">news</span></li>
	</ul></main>`)
	if len(p.Meta.Collections) != 1 {
		t.Fatalf("got %d collections, want 1: %+v", len(p.Meta.Collections), p.Meta.Collections)
	}
	c := p.Meta.Collections[0]
	recs, _, err := dom.Records(p.Doc, c.Root, collFields(c), 200)
	if err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	dt, ok := fieldNameByAttr(c, "datetime")
	if !ok || recs[0][dt] != "2020-01-01" {
		t.Errorf("datetime field = %q ok=%v, want 2020-01-01", recs[0][dt], ok)
	}
	id, ok := fieldNameByAttr(c, "data-id")
	if !ok || recs[2][id] != "9" {
		t.Errorf("data-id field = %q ok=%v, want 9", recs[2][id], ok)
	}
	if _, ok := fieldNameByAttr(c, "src"); !ok {
		t.Errorf("no image src field; fields=%+v", c.Fields)
	}
}

func TestCollectionsCrossRecordPruning(t *testing.T) {
	// .badge appears on only one card → below the coverage threshold → dropped.
	p := extractInline(t, `<main><ul>
	  <li class="card"><h3 class="t">A</h3><span class="v">1</span><em class="badge">SALE</em></li>
	  <li class="card"><h3 class="t">B</h3><span class="v">2</span></li>
	  <li class="card"><h3 class="t">C</h3><span class="v">3</span></li>
	  <li class="card"><h3 class="t">D</h3><span class="v">4</span></li>
	  <li class="card"><h3 class="t">E</h3><span class="v">5</span></li>
	</ul></main>`)
	if len(p.Meta.Collections) != 1 {
		t.Fatalf("got %d collections, want 1", len(p.Meta.Collections))
	}
	c := p.Meta.Collections[0]
	if !hasFieldName(c, "t") || !hasFieldName(c, "v") {
		t.Errorf("expected fields t and v; got %+v", c.Fields)
	}
	if hasFieldName(c, "badge") {
		t.Errorf("badge (1/5 coverage) should have been pruned; got %+v", c.Fields)
	}
}

func TestCollectionsShapeFallback(t *testing.T) {
	// Class-less repeats grouped by structural shape, not class.
	p := extractInline(t, `<main><div>
	  <article><h3>Title One</h3><p>A description paragraph with enough words to count.</p><a href="/1">Read</a></article>
	  <article><h3>Title Two</h3><p>Another description paragraph, also sufficiently long.</p><a href="/2">Read</a></article>
	  <article><h3>Title Three</h3><p>Third description paragraph, likewise plenty long.</p><a href="/3">Read</a></article>
	</div></main>`)
	if len(p.Meta.Collections) != 1 {
		t.Fatalf("got %d collections, want 1: %+v", len(p.Meta.Collections), p.Meta.Collections)
	}
	c := p.Meta.Collections[0]
	recs, _, err := dom.Records(p.Doc, c.Root, collFields(c), 200)
	if err != nil {
		t.Fatalf("class-less root %q does not round-trip: %v", c.Root, err)
	}
	if len(recs) != 3 || c.Count != 3 {
		t.Fatalf("root %q -> %d records (count %d), want 3", c.Root, len(recs), c.Count)
	}
	if !hasFieldName(c, "title") {
		t.Errorf("expected a title field from the heading; got %+v", c.Fields)
	}
}

func TestCollectionsMinCountRejection(t *testing.T) {
	p := extractInline(t, `<main><ul>
	  <li class="product"><h2 class="name">Widget</h2><span class="price">$9.99</span></li>
	  <li class="product"><h2 class="name">Gadget</h2><span class="price">$19.99</span></li>
	</ul></main>`)
	if len(p.Meta.Collections) != 0 {
		t.Errorf("2 records should not form a collection; got %+v", p.Meta.Collections)
	}
}

func TestCollectionsThinRejection(t *testing.T) {
	// Pagination: short link-only records with no content field.
	p := extractInline(t, `<div><ul>
	  <li><a href="?p=1">1</a></li><li><a href="?p=2">2</a></li>
	  <li><a href="?p=3">3</a></li><li><a href="?p=4">4</a></li>
	</ul></div>`)
	if len(p.Meta.Collections) != 0 {
		t.Errorf("thin pagination should not form a collection; got %+v", p.Meta.Collections)
	}
}

func TestCollectionsRootRoundTripsInvariant(t *testing.T) {
	// Every emitted collection's schema must round-trip through the executor with
	// its reported count — the core "a suggested schema always works" guarantee.
	page := `<header><nav><ul><li><a href="/1">One</a></li><li><a href="/2">Two</a></li><li><a href="/3">Three</a></li></ul></nav></header>` +
		productList +
		`<aside><ul>
		  <li class="post"><h4 class="hd">Post A</h4><p class="ex">excerpt one here</p></li>
		  <li class="post"><h4 class="hd">Post B</h4><p class="ex">excerpt two here</p></li>
		  <li class="post"><h4 class="hd">Post C</h4><p class="ex">excerpt three here</p></li>
		</ul></aside>`
	p := extractInline(t, page)
	if len(p.Meta.Collections) == 0 {
		t.Fatal("expected at least one collection")
	}
	for _, c := range p.Meta.Collections {
		recs, _, err := dom.Records(p.Doc, c.Root, collFields(c), 200)
		if err != nil {
			t.Errorf("collection root %q errored: %v", c.Root, err)
			continue
		}
		if len(recs) != c.Count {
			t.Errorf("root %q -> %d records, but Count = %d", c.Root, len(recs), c.Count)
		}
		if len(c.Fields) == 0 {
			t.Errorf("collection %q has no fields", c.Root)
		}
	}
}

func TestCollectionsFieldCap(t *testing.T) {
	// A field-rich record must not exceed the per-collection field cap (8).
	body := `<main><ul>`
	for i := 0; i < 3; i++ {
		body += `<li class="rich">` +
			`<span class="a">a</span><span class="b">b</span><span class="c">c</span>` +
			`<span class="d">d</span><span class="e">e</span><span class="f">f</span>` +
			`<span class="g">g</span><span class="h">h</span><span class="i">i</span>` +
			`<span class="j">j</span></li>`
	}
	body += `</ul></main>`
	p := extractInline(t, body)
	if len(p.Meta.Collections) != 1 {
		t.Fatalf("got %d collections, want 1", len(p.Meta.Collections))
	}
	if n := len(p.Meta.Collections[0].Fields); n > 8 {
		t.Errorf("field count = %d, want <= 8 (cap)", n)
	}
}
