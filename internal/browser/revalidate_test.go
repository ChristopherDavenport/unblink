package browser

// White-box: revalidation needs an *expired* cache entry, and the only honest
// way to get one without a multi-minute sleep is to backdate the entry, which
// requires reaching into the cache. Everything else goes through the public API.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// An expired cache entry with an ETag is revalidated with a conditional GET: a
// 304 reuses the parsed page (no body re-served, entry fresh again); a changed
// body replaces it.
func TestStatelessRevalidate(t *testing.T) {
	var mu sync.Mutex
	etag := `"v1"`
	version := "version-one"
	fullServes, condHits := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt", "/llms.txt", "/llms-full.txt":
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			condHits++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fullServes++
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><head><title>Rev</title></head><body><p>the %s body, with enough prose to survive reduction intact.</p></body></html>", version)
	}))
	defer srv.Close()

	b, err := New(WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	read := func() string {
		t.Helper()
		r, err := b.Read(ctx, Request{URL: srv.URL}, "full", 0, "")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return r.Markdown
	}
	// backdate expires the cached entry so the next read must revalidate.
	backdate := func() {
		t.Helper()
		key := cacheKey(srv.URL, false)
		b.cache.mu.Lock()
		e, ok := b.cache.entries[key]
		if !ok {
			b.cache.mu.Unlock()
			t.Fatal("page not cached")
		}
		e.at = time.Now().Add(-2 * DefaultCacheTTL)
		b.cache.entries[key] = e
		b.cache.mu.Unlock()
	}
	counts := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return fullServes, condHits
	}

	if md := read(); !strings.Contains(md, "version-one") {
		t.Fatalf("initial read missing body:\n%s", md)
	}

	// Unchanged content: the conditional 304 reuses the parsed page…
	backdate()
	if md := read(); !strings.Contains(md, "version-one") {
		t.Errorf("revalidated read lost the cached body:\n%s", md)
	}
	if f, c := counts(); f != 1 || c != 1 {
		t.Errorf("after 304: fullServes=%d condHits=%d, want 1/1", f, c)
	}
	// …and the entry is fresh again — an immediate re-read makes no request.
	if md := read(); !strings.Contains(md, "version-one") {
		t.Errorf("fresh re-read lost the body:\n%s", md)
	}
	if f, c := counts(); f != 1 || c != 1 {
		t.Errorf("after touch: fullServes=%d condHits=%d, want 1/1 (no new request)", f, c)
	}

	// Changed content: the conditional GET returns the new body and replaces the entry.
	mu.Lock()
	etag, version = `"v2"`, "version-two"
	mu.Unlock()
	backdate()
	if md := read(); !strings.Contains(md, "version-two") {
		t.Errorf("changed content not picked up on revalidation:\n%s", md)
	}
	if f, _ := counts(); f != 2 {
		t.Errorf("fullServes = %d, want 2", f)
	}
}

// A stale entry without validators falls back to a plain refetch (no If-* headers).
func TestStatelessRevalidateWithoutValidators(t *testing.T) {
	serves := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt", "/llms.txt", "/llms-full.txt":
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			t.Error("plain refetch must not carry conditional headers")
		}
		serves++
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><body><p>no-validator body %d with plenty of words to keep.</p></body></html>", serves)
	}))
	defer srv.Close()

	b, err := New(WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	if _, err := b.Read(ctx, Request{URL: srv.URL}, "full", 0, ""); err != nil {
		t.Fatal(err)
	}
	key := cacheKey(srv.URL, false)
	b.cache.mu.Lock()
	e := b.cache.entries[key]
	e.at = time.Now().Add(-2 * DefaultCacheTTL)
	b.cache.entries[key] = e
	b.cache.mu.Unlock()

	r, err := b.Read(ctx, Request{URL: srv.URL}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if serves != 2 || !strings.Contains(r.Markdown, "body 2") {
		t.Errorf("serves=%d markdown=%q, want a full refetch", serves, r.Markdown)
	}
}
