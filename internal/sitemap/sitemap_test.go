package sitemap_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/sitemap"
)

const urlsetXML = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/a</loc><lastmod>2026-06-01</lastmod></url>
  <url><loc>https://example.com/b</loc></url>
  <url><loc>  https://example.com/c  </loc></url>
  <url><loc></loc></url>
</urlset>`

const indexXML = `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sitemap-1.xml</loc><lastmod>2026-06-02</lastmod></sitemap>
  <sitemap><loc>https://example.com/sitemap-2.xml</loc></sitemap>
</sitemapindex>`

// A prefixed-namespace variant to prove local-name matching.
const nsPrefixedXML = `<?xml version="1.0"?>
<sm:urlset xmlns:sm="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sm:url><sm:loc>https://example.com/x</sm:loc></sm:url>
</sm:urlset>`

func TestParseURLSet(t *testing.T) {
	doc, err := sitemap.Parse([]byte(urlsetXML), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Kind != sitemap.KindURLSet {
		t.Fatalf("Kind = %v, want KindURLSet", doc.Kind)
	}
	// The blank <loc> is skipped; the whitespace one is trimmed.
	want := []sitemap.Loc{
		{URL: "https://example.com/a", LastMod: "2026-06-01"},
		{URL: "https://example.com/b"},
		{URL: "https://example.com/c"},
	}
	if len(doc.URLs) != len(want) {
		t.Fatalf("got %d urls, want %d: %+v", len(doc.URLs), len(want), doc.URLs)
	}
	for i, w := range want {
		if doc.URLs[i] != w {
			t.Errorf("url[%d] = %+v, want %+v", i, doc.URLs[i], w)
		}
	}
	if len(doc.Sitemaps) != 0 {
		t.Errorf("Sitemaps should be empty for a urlset, got %+v", doc.Sitemaps)
	}
}

func TestParseIndex(t *testing.T) {
	doc, err := sitemap.Parse([]byte(indexXML), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Kind != sitemap.KindIndex {
		t.Fatalf("Kind = %v, want KindIndex", doc.Kind)
	}
	if len(doc.Sitemaps) != 2 || len(doc.URLs) != 0 {
		t.Fatalf("got %d sitemaps / %d urls, want 2 / 0", len(doc.Sitemaps), len(doc.URLs))
	}
	if doc.Sitemaps[0].URL != "https://example.com/sitemap-1.xml" || doc.Sitemaps[0].LastMod != "2026-06-02" {
		t.Errorf("sitemap[0] = %+v", doc.Sitemaps[0])
	}
}

func TestParseNamespacePrefixed(t *testing.T) {
	doc, err := sitemap.Parse([]byte(nsPrefixedXML), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Kind != sitemap.KindURLSet || len(doc.URLs) != 1 || doc.URLs[0].URL != "https://example.com/x" {
		t.Fatalf("prefixed namespace not parsed: %+v", doc)
	}
}

func TestParseGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(urlsetXML)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	doc, err := sitemap.Parse(buf.Bytes(), 0)
	if err != nil {
		t.Fatalf("Parse gzip: %v", err)
	}
	if doc.Kind != sitemap.KindURLSet || len(doc.URLs) != 3 {
		t.Fatalf("gzip sitemap not parsed: %+v", doc)
	}
}

func TestParseMaxEntries(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<urlset>`)
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, `<url><loc>https://example.com/p%d</loc></url>`, i)
	}
	b.WriteString(`</urlset>`)
	doc, err := sitemap.Parse([]byte(b.String()), 10)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.URLs) != 10 {
		t.Fatalf("maxEntries not honored: got %d, want 10", len(doc.URLs))
	}
}

func TestParseBogusRoot(t *testing.T) {
	if _, err := sitemap.Parse([]byte(`<html><body>not a sitemap</body></html>`), 0); err == nil {
		t.Fatal("expected error for non-sitemap root")
	}
}

func TestParseMalformedTailTolerated(t *testing.T) {
	// A well-formed root and one entry, then a truncated tail.
	partial := `<urlset><url><loc>https://example.com/a</loc></url><url><loc>https://example.com/b`
	doc, err := sitemap.Parse([]byte(partial), 0)
	if err != nil {
		t.Fatalf("expected lenient parse, got error: %v", err)
	}
	if doc.Kind != sitemap.KindURLSet || len(doc.URLs) < 1 || doc.URLs[0].URL != "https://example.com/a" {
		t.Fatalf("partial parse dropped valid entries: %+v", doc)
	}
}
