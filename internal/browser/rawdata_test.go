package browser_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// serveContent serves a single body with an explicit Content-Type (404ing the
// origin-root metadata probes), for the raw_html/text/data pipeline tests.
func serveContent(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt", "/llms.txt", "/llms-full.txt":
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReadRawHTML(t *testing.T) {
	srv, _ := serveFixture(t, "structured.input.html")
	b := newBrowser(t)
	ctx := context.Background()

	// markdown (default) strips scripts and forms...
	md, err := b.Read(ctx, req(srv.URL), "article", 6000, "")
	if err != nil {
		t.Fatalf("read markdown: %v", err)
	}
	for _, junk := range []string{"<script", "console.log('should be stripped')"} {
		if strings.Contains(md.Markdown, junk) {
			t.Errorf("markdown leaked %q", junk)
		}
	}

	// ...raw_html surfaces them verbatim.
	r, err := b.Read(ctx, browser.Request{URL: srv.URL, Format: "raw_html"}, "", 6000, "")
	if err != nil {
		t.Fatalf("read raw_html: %v", err)
	}
	if r.Mode != "raw_html" {
		t.Errorf("mode = %q, want raw_html", r.Mode)
	}
	for _, want := range []string{"<script", "console.log('should be stripped')", "<form", `id="signup"`} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("raw_html missing %q", want)
		}
	}
}

func TestReadRawHTMLSelector(t *testing.T) {
	srv, _ := serveFixture(t, "structured.input.html")
	b := newBrowser(t)
	r, err := b.Read(context.Background(),
		browser.Request{URL: srv.URL, Format: "raw_html", Selector: "script"}, "", 6000, "")
	if err != nil {
		t.Fatalf("read raw_html selector: %v", err)
	}
	if !strings.Contains(r.Markdown, "console.log('should be stripped')") {
		t.Errorf("selector output missing the script: %q", r.Markdown)
	}
	if strings.Contains(r.Markdown, "<form") {
		t.Errorf("selector output should only contain the script: %q", r.Markdown)
	}
}

func TestReadRawHTMLSelectorNoMatch(t *testing.T) {
	srv, _ := serveFixture(t, "structured.input.html")
	b := newBrowser(t)
	_, err := b.Read(context.Background(),
		browser.Request{URL: srv.URL, Format: "raw_html", Selector: ".nonexistent-xyz"}, "", 6000, "")
	if err == nil {
		t.Fatal("expected an error for a zero-match selector")
	}
}

func TestReadText(t *testing.T) {
	srv, _ := serveFixture(t, "structured.input.html")
	b := newBrowser(t)
	r, err := b.Read(context.Background(), browser.Request{URL: srv.URL, Format: "text"}, "", 6000, "")
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	if r.Mode != "text" {
		t.Errorf("mode = %q, want text", r.Mode)
	}
	for _, want := range []string{"Structured Test Page", "Installation"} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("text missing %q", want)
		}
	}
	for _, bad := range []string{"<script", "console.log", "<h1>"} {
		if strings.Contains(r.Markdown, bad) {
			t.Errorf("text leaked markup %q", bad)
		}
	}
}

func TestReadRawHTMLBinaryGuard(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("not-a-real-png-body")...)
	srv := serveContent(t, "image/png", png)
	b := newBrowser(t)
	if _, err := b.Read(context.Background(),
		browser.Request{URL: srv.URL, Format: "raw_html"}, "", 6000, ""); err == nil {
		t.Fatal("expected an error: raw_html on image content")
	}
}

const dataHTML = `<!doctype html><html><head>
<title>Data Page</title>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"Product","name":"Acme Widget","aggregateRating":{"@type":"AggregateRating","ratingValue":"4.5"}}
</script>
</head><body>
<div itemscope itemtype="https://schema.org/Person"><span itemprop="name">Ada</span></div>
<table><caption>Prices</caption>
<thead><tr><th>Item</th><th>Cost</th></tr></thead>
<tbody><tr><td>Widget</td><td>$9</td></tr></tbody>
</table>
</body></html>`

func TestData(t *testing.T) {
	srv := serveContent(t, "text/html; charset=utf-8", []byte(dataHTML))
	b := newBrowser(t)
	r, err := b.Data(context.Background(), req(srv.URL), "all")
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	if r.Counts.JSONLD != 1 || r.Counts.Tables != 1 || r.Counts.Microdata != 1 {
		t.Fatalf("counts = %+v", r.Counts)
	}
	if m, _ := r.JSONLD[0].(map[string]any); m["name"] != "Acme Widget" {
		t.Errorf("jsonld = %#v", r.JSONLD[0])
	}
	if r.Tables[0].Caption != "Prices" || strings.Join(r.Tables[0].Headers, "|") != "Item|Cost" {
		t.Errorf("table = %+v", r.Tables[0])
	}
	if r.Microdata[0].Type != "https://schema.org/Person" {
		t.Errorf("microdata = %+v", r.Microdata[0])
	}
}

func TestDataNonHTML(t *testing.T) {
	srv := serveContent(t, "application/json", []byte(`{"a":1}`))
	b := newBrowser(t)
	if _, err := b.Data(context.Background(), req(srv.URL), "all"); err == nil {
		t.Fatal("expected an error: data on non-HTML content")
	}
}
