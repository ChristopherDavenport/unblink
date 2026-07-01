package dom_test

import (
	"net/url"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
)

func jsonldDoc(t *testing.T, scripts string) []any {
	t.Helper()
	return dom.JSONLD(parseDoc(t, "<!doctype html><html><head>"+scripts+"</head><body></body></html>"))
}

func TestJSONLDSingleObject(t *testing.T) {
	items := jsonldDoc(t, `<script type="application/ld+json">
		{"@context":"https://schema.org","@type":"Product","name":"Acme Widget"}</script>`)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	m, ok := items[0].(map[string]any)
	if !ok || m["@type"] != "Product" || m["name"] != "Acme Widget" {
		t.Errorf("unexpected item: %#v", items[0])
	}
}

func TestJSONLDArray(t *testing.T) {
	items := jsonldDoc(t, `<script type="application/ld+json">
		[{"@type":"A"},{"@type":"B"}]</script>`)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
}

func TestJSONLDGraphFlattened(t *testing.T) {
	items := jsonldDoc(t, `<script type="application/ld+json">
		{"@context":"x","@graph":[{"@type":"Article"},{"@type":"Organization"}]}</script>`)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (graph flattened)", len(items))
	}
	for _, it := range items {
		if m, ok := it.(map[string]any); !ok || m["@graph"] != nil {
			t.Errorf("wrapper should be dropped, got %#v", it)
		}
	}
}

func TestJSONLDMalformedSkipped(t *testing.T) {
	items := jsonldDoc(t, `
		<script type="application/ld+json">{ this is not json </script>
		<script type="application/ld+json">{"@type":"Valid"}</script>`)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (malformed skipped)", len(items))
	}
	if m, _ := items[0].(map[string]any); m["@type"] != "Valid" {
		t.Errorf("unexpected surviving item: %#v", items[0])
	}
}

func TestJSONLDIgnoresPlainScript(t *testing.T) {
	items := jsonldDoc(t, `<script>{"@type":"NotLD"}</script>`)
	if len(items) != 0 {
		t.Fatalf("plain script should be ignored, got %d items", len(items))
	}
}

func TestMicrodataNested(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	doc := parseDoc(t, `<!doctype html><html><body>
	<div itemscope itemtype="https://schema.org/Person">
	  <span itemprop="name">Ada Lovelace</span>
	  <a itemprop="url homepage" href="/ada">home</a>
	  <div itemprop="address" itemscope itemtype="https://schema.org/PostalAddress">
	    <span itemprop="addressLocality">London</span>
	  </div>
	</div></body></html>`)

	items := dom.Microdata(doc, base)
	if len(items) != 1 {
		t.Fatalf("top-level items = %d, want 1 (nested scope not top-level)", len(items))
	}
	p := items[0]
	if p.Type != "https://schema.org/Person" {
		t.Errorf("type = %q", p.Type)
	}
	if got := p.Properties["name"]; len(got) != 1 || got[0] != "Ada Lovelace" {
		t.Errorf("name = %#v", got)
	}
	// multi-name itemprop populates both keys with the resolved URL
	for _, k := range []string{"url", "homepage"} {
		if got := p.Properties[k]; len(got) != 1 || got[0] != "https://example.com/ada" {
			t.Errorf("%s = %#v", k, got)
		}
	}
	addr := p.Properties["address"]
	if len(addr) != 1 {
		t.Fatalf("address = %#v", addr)
	}
	nested, ok := addr[0].(page.MicrodataItem)
	if !ok {
		t.Fatalf("address value is not a nested item: %T", addr[0])
	}
	if nested.Type != "https://schema.org/PostalAddress" {
		t.Errorf("nested type = %q", nested.Type)
	}
	if got := nested.Properties["addressLocality"]; len(got) != 1 || got[0] != "London" {
		t.Errorf("addressLocality = %#v", got)
	}
}
