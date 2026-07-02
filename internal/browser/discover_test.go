package browser_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/search"
)

// discoverServers builds a primary host (robots + sitemap + interlinked pages +
// an off-origin redirector) and a second "other" origin. The sitemap and the home
// page both reference the other origin so the tests can prove same-origin filtering.
func discoverServers(t *testing.T) (base, other string) {
	t.Helper()

	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><head><title>Other</title></head><body>`+
			`<h1>Other origin</h1><p>Content on a different host.</p></body></html>`)
	}))
	t.Cleanup(otherSrv.Close)
	other = otherSrv.URL

	var self string // primary base, set after the server starts
	page := func(title, body string) string {
		return `<!doctype html><html><head><title>` + title + `</title></head><body>` + body + `</body></html>`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "User-agent: *\nDisallow: /private\nSitemap: %s/sitemap.xml\n", self)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+
			`<url><loc>%s/a</loc></url>`+
			`<url><loc>%s/b</loc></url>`+
			`<url><loc>%s/x</loc></url>`+ // cross-origin loc: must be dropped
			`</urlset>`, self, self, other)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page("A", `<h1>Page A</h1><a href="/c">to C</a>`))
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page("B", `<h1>Page B</h1><p>leaf</p>`))
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page("C", `<h1>Page C</h1><p>deep leaf</p>`))
	})
	mux.HandleFunc("/out", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other+"/sink", http.StatusFound) // off-origin redirect
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page("Home",
			`<h1>Home</h1><a href="/a">A</a><a href="/b">B</a><a href="/out">out</a>`+
				`<a href="`+other+`/ext">external</a>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	self = srv.URL
	return srv.URL, other
}

func TestMapSitemapAndCrawl(t *testing.T) {
	base, other := discoverServers(t)
	b := newBrowser(t)

	res, err := b.Map(context.Background(), req(base), 100, 3, nil)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if res.Origin != base {
		t.Errorf("Origin = %q, want %q", res.Origin, base)
	}
	if res.Count != len(res.URLs) {
		t.Errorf("Count %d != len(URLs) %d", res.Count, len(res.URLs))
	}

	byURL := map[string]browser.MapEntry{}
	for _, e := range res.URLs {
		byURL[e.URL] = e
		if !strings.HasPrefix(e.URL, base) {
			t.Errorf("URL %q is not same-origin as %q", e.URL, base)
		}
		if strings.HasPrefix(e.URL, other) {
			t.Errorf("cross-origin URL leaked into map: %q", e.URL)
		}
	}
	// Sitemap-declared, crawl-discovered, and the deep leaf must all be present.
	for _, want := range []string{base + "/a", base + "/b", base + "/c"} {
		if _, ok := byURL[want]; !ok {
			t.Errorf("map missing %q (have %v)", want, keysOf(byURL))
		}
	}
	if byURL[base+"/a"].Source != "sitemap" {
		t.Errorf("/a source = %q, want sitemap", byURL[base+"/a"].Source)
	}
	if byURL[base+"/c"].Source != "crawl" {
		t.Errorf("/c source = %q, want crawl", byURL[base+"/c"].Source)
	}
	// The off-origin redirect target must never appear.
	if _, ok := byURL[other+"/sink"]; ok {
		t.Error("off-origin redirect target leaked into map")
	}
	// The sitemap consulted should be recorded.
	foundSitemap := false
	for _, s := range res.Sitemaps {
		if s == base+"/sitemap.xml" {
			foundSitemap = true
		}
	}
	if !foundSitemap {
		t.Errorf("sitemap not recorded: %v", res.Sitemaps)
	}
}

func TestMapRespectsCap(t *testing.T) {
	base, _ := discoverServers(t)
	b := newBrowser(t)
	res, err := b.Map(context.Background(), req(base), 1, 3, nil)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if res.Count > 1 {
		t.Errorf("Count = %d, want <= 1", res.Count)
	}
	if !res.Truncated {
		t.Error("Truncated should be set when max_urls binds")
	}
}

func TestMapRequiresURL(t *testing.T) {
	b := newBrowser(t)
	if _, err := b.Map(context.Background(), browser.Request{}, 0, 0, nil); err == nil {
		t.Error("expected error for missing url")
	}
}

func TestSearchDisabled(t *testing.T) {
	b := newBrowser(t)
	if b.SearchEnabled() {
		t.Error("SearchEnabled should be false without a provider")
	}
	_, err := b.Search(context.Background(), "hello", 5, "")
	if !errors.Is(err, browser.ErrSearchDisabled) {
		t.Fatalf("err = %v, want ErrSearchDisabled", err)
	}
}

func TestSearchConfigured(t *testing.T) {
	fake := fakeProvider{results: []search.Result{
		{Title: "One", URL: "https://a.example/1", Snippet: "s1"},
		{Title: "Two", URL: "https://a.example/2", Snippet: "s2"},
	}}
	b, err := browser.New(browser.WithAllowPrivate(true), browser.WithSearchProvider(fake))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !b.SearchEnabled() {
		t.Error("SearchEnabled should be true with a provider")
	}
	res, err := b.Search(context.Background(), "  hello  ", 5, "a.example")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Provider != "fake" || res.Query != "hello" || res.Count != 2 || len(res.Results) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := b.Search(context.Background(), "   ", 5, ""); err == nil {
		t.Error("expected error for empty query")
	}
}

type fakeProvider struct{ results []search.Result }

func (fakeProvider) Name() string { return "fake" }
func (f fakeProvider) Search(_ context.Context, _ string, o search.Options) ([]search.Result, error) {
	if o.Count > 0 && o.Count < len(f.results) {
		return f.results[:o.Count], nil
	}
	return f.results, nil
}

func keysOf(m map[string]browser.MapEntry) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
