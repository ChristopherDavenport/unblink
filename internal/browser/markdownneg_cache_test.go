package browser

// White-box for the same reason as revalidate_test.go: revalidation needs an
// expired cache entry, which means backdating it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// An expired Markdown-negotiated entry is revalidated under the Markdown Accept,
// so the origin compares the validator against the representation it belongs to;
// the HTML entry for the same URL is revalidated under the default Accept. A
// shared ETag across the two variants therefore cannot swap one for the other.
func TestMarkdownRevalidationKeepsRepresentation(t *testing.T) {
	const etag = `"same-for-both"`
	var mu sync.Mutex
	var condAccepts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt", "/llms.txt", "/llms-full.txt":
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == etag {
			mu.Lock()
			condAccepts = append(condAccepts, r.Header.Get("Accept"))
			mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		if strings.HasPrefix(r.Header.Get("Accept"), "text/markdown") {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# Doc\n\nMARKDOWN-VARIANT\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>Doc</title></head><body><p>HTML-VARIANT body, with enough prose to survive reduction intact.</p></body></html>"))
	}))
	defer srv.Close()

	b, err := New(WithAllowPrivate(true), WithPreferMarkdown(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	no := false
	readMD := Request{URL: srv.URL}
	readHTML := Request{URL: srv.URL, PreferMarkdown: &no}

	check := func(req Request, kind, sentinel string) {
		t.Helper()
		r, err := b.Read(ctx, req, "full", 0, "")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if r.Kind != kind || !strings.Contains(r.Markdown, sentinel) {
			t.Errorf("kind = %q, markdown = %q; want %s containing %s", r.Kind, r.Markdown, kind, sentinel)
		}
	}
	check(readMD, "markdown", "MARKDOWN-VARIANT")
	check(readHTML, "html", "HTML-VARIANT")

	mdKey := cacheKeyFor(srv.URL, renderOpts{markdown: true})
	htmlKey := cacheKeyFor(srv.URL, renderOpts{})
	if mdKey == htmlKey {
		t.Fatal("Markdown and HTML share a cache key")
	}
	b.cache.mu.Lock()
	for _, key := range []string{mdKey, htmlKey} {
		e, ok := b.cache.entries[key]
		if !ok {
			b.cache.mu.Unlock()
			t.Fatalf("no cache entry under %q", key)
		}
		e.at = time.Now().Add(-2 * DefaultCacheTTL)
		b.cache.entries[key] = e
	}
	b.cache.mu.Unlock()

	check(readMD, "markdown", "MARKDOWN-VARIANT")
	check(readHTML, "html", "HTML-VARIANT")

	mu.Lock()
	defer mu.Unlock()
	if len(condAccepts) != 2 {
		t.Fatalf("conditional requests = %v, want 2", condAccepts)
	}
	if condAccepts[0] != markdownAccept {
		t.Errorf("Markdown entry revalidated with Accept %q, want %q", condAccepts[0], markdownAccept)
	}
	if strings.Contains(condAccepts[1], "markdown") {
		t.Errorf("HTML entry revalidated with a Markdown Accept: %q", condAccepts[1])
	}
}
