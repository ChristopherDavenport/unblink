package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

const productListFixture = `<!doctype html><html><body>
<ul>
  <li class="product"><h2 class="name">Widget</h2><span class="price">$9.99</span><a class="buy" href="/p/1" data-sku="W-1">Buy</a></li>
  <li class="product"><h2 class="name">Gadget</h2><span class="price">$19.99</span><a class="buy" href="/p/2" data-sku="G-2">Buy</a></li>
  <li class="product"><h2 class="name">Gizmo</h2><a class="buy" href="/p/3">Buy</a></li>
</ul>
</body></html>`

func TestRecordsRepeatedRoot(t *testing.T) {
	doc := parseDoc(t, productListFixture)
	recs, truncated, err := dom.Records(doc, "li.product", map[string]dom.FieldSpec{
		"name":  {Selector: ".name"},
		"price": {Selector: ".price"},
		"sku":   {Selector: "a.buy", Attr: "data-sku"},
	}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if truncated {
		t.Errorf("truncated = true, want false")
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	if recs[0]["name"] != "Widget" || recs[0]["price"] != "$9.99" || recs[0]["sku"] != "W-1" {
		t.Errorf("record 0 = %v", recs[0])
	}
	if recs[1]["name"] != "Gadget" || recs[1]["sku"] != "G-2" {
		t.Errorf("record 1 = %v", recs[1])
	}
	// Third product has no .price and no data-sku → those fields omitted.
	if _, ok := recs[2]["price"]; ok {
		t.Errorf("record 2 price should be omitted, got %q", recs[2]["price"])
	}
	if _, ok := recs[2]["sku"]; ok {
		t.Errorf("record 2 sku should be omitted, got %q", recs[2]["sku"])
	}
	if recs[2]["name"] != "Gizmo" {
		t.Errorf("record 2 name = %q, want Gizmo", recs[2]["name"])
	}
}

func TestRecordsWholeDocument(t *testing.T) {
	doc := parseDoc(t, productListFixture)
	recs, _, err := dom.Records(doc, "", map[string]dom.FieldSpec{
		"first": {Selector: ".name"},
	}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1 (whole document)", len(recs))
	}
	// First .name in document order.
	if recs[0]["first"] != "Widget" {
		t.Errorf("first = %q, want Widget", recs[0]["first"])
	}
}

func TestRecordsFirstMatchWins(t *testing.T) {
	doc := parseDoc(t, `<div><p>one</p><p>two</p><p>three</p></div>`)
	recs, _, err := dom.Records(doc, "div", map[string]dom.FieldSpec{"p": {Selector: "p"}}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 1 || recs[0]["p"] != "one" {
		t.Errorf("got %v, want single record with p=one", recs)
	}
}

func TestRecordsMissingFieldOmitted(t *testing.T) {
	doc := parseDoc(t, `<div><h1>title</h1></div>`)
	recs, _, err := dom.Records(doc, "div", map[string]dom.FieldSpec{
		"title": {Selector: "h1"},
		"price": {Selector: ".price"},
	}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0]["title"] != "title" {
		t.Errorf("title = %q", recs[0]["title"])
	}
	if _, ok := recs[0]["price"]; ok {
		t.Errorf("price should be omitted, got %q", recs[0]["price"])
	}
}

func TestRecordsAttributeAbsentOmitted(t *testing.T) {
	doc := parseDoc(t, `<div><a>no href here</a></div>`)
	recs, _, err := dom.Records(doc, "div", map[string]dom.FieldSpec{
		"href": {Selector: "a", Attr: "href"},
	}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if _, ok := recs[0]["href"]; ok {
		t.Errorf("absent attribute should be omitted, got %q", recs[0]["href"])
	}
}

func TestRecordsAttributeEmptyKept(t *testing.T) {
	doc := parseDoc(t, `<div><a data-x="">present but empty</a></div>`)
	recs, _, err := dom.Records(doc, "div", map[string]dom.FieldSpec{
		"x": {Selector: "a", Attr: "data-x"},
	}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	v, ok := recs[0]["x"]
	if !ok {
		t.Fatalf("present-but-empty attribute should be kept")
	}
	if v != "" {
		t.Errorf("x = %q, want empty string", v)
	}
}

func TestRecordsLimitTruncates(t *testing.T) {
	doc := parseDoc(t, `<div class="r">a</div><div class="r">b</div><div class="r">c</div><div class="r">d</div>`)
	recs, truncated, err := dom.Records(doc, "div.r", map[string]dom.FieldSpec{"v": {Selector: "div.r"}}, 2)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if !truncated {
		t.Errorf("truncated = false, want true")
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (limit)", len(recs))
	}
	// Document order preserved.
	if recs[0]["v"] != "a" || recs[1]["v"] != "b" {
		t.Errorf("records = %v, want a,b in order", recs)
	}
}

func TestRecordsWhitespaceCollapsed(t *testing.T) {
	doc := parseDoc(t, "<div><p>  hello \n\t  world  </p></div>")
	recs, _, err := dom.Records(doc, "div", map[string]dom.FieldSpec{"p": {Selector: "p"}}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if recs[0]["p"] != "hello world" {
		t.Errorf("p = %q, want %q", recs[0]["p"], "hello world")
	}
}

func TestRecordsInvalidRootSelector(t *testing.T) {
	doc := parseDoc(t, `<div>x</div>`)
	_, _, err := dom.Records(doc, "li:::bogus", map[string]dom.FieldSpec{"v": {Selector: "div"}}, 50)
	if err == nil {
		t.Fatalf("want error for invalid root selector, got nil")
	}
	if !strings.Contains(err.Error(), "root selector") {
		t.Errorf("error = %v, want it to mention the root selector", err)
	}
}

func TestRecordsInvalidFieldSelector(t *testing.T) {
	doc := parseDoc(t, `<div>x</div>`)
	_, _, err := dom.Records(doc, "div", map[string]dom.FieldSpec{"bad": {Selector: ">>>"}}, 50)
	if err == nil {
		t.Fatalf("want error for invalid field selector, got nil")
	}
	if !strings.Contains(err.Error(), `"bad"`) {
		t.Errorf("error = %v, want it to name field \"bad\"", err)
	}
}

func TestRecordsNestedRoots(t *testing.T) {
	// An outer container that also matches the root selector, wrapping inner ones.
	doc := parseDoc(t, `<section class="box"><span class="v">outer</span><section class="box"><span class="v">inner</span></section></section>`)
	recs, _, err := dom.Records(doc, "section.box", map[string]dom.FieldSpec{"v": {Selector: ".v"}}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 (outer + nested)", len(recs))
	}
	// Outer first (document order); the outer scope's first .v is "outer".
	if recs[0]["v"] != "outer" || recs[1]["v"] != "inner" {
		t.Errorf("records = %v, want outer then inner", recs)
	}
}

func TestRecordsNoMatchesEmpty(t *testing.T) {
	doc := parseDoc(t, `<div>x</div>`)
	recs, truncated, err := dom.Records(doc, "li.nope", map[string]dom.FieldSpec{"v": {Selector: "span"}}, 50)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if truncated {
		t.Errorf("truncated = true, want false")
	}
	if len(recs) != 0 {
		t.Errorf("got %d records, want 0", len(recs))
	}
}
