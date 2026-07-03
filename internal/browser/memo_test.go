package browser_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// countingFixtureServer serves the longread fixture and counts requests, so
// tests can prove reads were served from the cache (and therefore that the
// memo, which rides the cache entry, was in play).
func countingFixtureServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	body, err := os.ReadFile("../../eval/corpus/longread.input.html")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	}))
	return srv, &hits
}

func memoTestBrowser(t *testing.T) *browser.Browser {
	t.Helper()
	br, err := browser.New(browser.WithAllowPrivate(true), browser.WithRateLimit(0, 0))
	if err != nil {
		t.Fatalf("browser.New: %v", err)
	}
	return br
}

// Repeat reads of a cached page — the memo-hit path — must be byte-identical
// to the first (computed) read, per mode, off a single fetch.
func TestReadMemoRepeatIdentical(t *testing.T) {
	srv, hits := countingFixtureServer(t)
	defer srv.Close()
	br := memoTestBrowser(t)
	defer br.Close()
	ctx := context.Background()
	url := srv.URL + "/post"

	art1, err := br.Read(ctx, browser.Request{URL: url}, "article", 6000, "")
	if err != nil {
		t.Fatalf("article read 1: %v", err)
	}
	art2, err := br.Read(ctx, browser.Request{URL: url}, "article", 6000, "")
	if err != nil {
		t.Fatalf("article read 2: %v", err)
	}
	if art1.Markdown != art2.Markdown || art1.Mode != art2.Mode || art1.ArticleFallback != art2.ArticleFallback {
		t.Fatal("repeat article read differs from the first — memo hit is not byte-identical")
	}
	full, err := br.Read(ctx, browser.Request{URL: url}, "full", 6000, "")
	if err != nil {
		t.Fatalf("full read: %v", err)
	}
	if full.Markdown == art1.Markdown {
		t.Fatal("full and article reads returned identical markdown — memo keys are not mode-scoped")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("server hit %d times, want 1 (all reads within the cache TTL)", n)
	}
}

// Pagination cursor pages must be stable across repeats and distinct from
// page 1 — the memo serves the same markdown the cursor was minted against.
func TestReadMemoCursorPagesStable(t *testing.T) {
	srv, _ := countingFixtureServer(t)
	defer srv.Close()
	br := memoTestBrowser(t)
	defer br.Close()
	ctx := context.Background()
	url := srv.URL + "/post"

	p1, err := br.Read(ctx, browser.Request{URL: url}, "full", 300, "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if p1.NextCursor == "" {
		t.Fatal("fixture did not paginate at 300 tokens; cursor test needs >1 page")
	}
	p2a, err := br.Read(ctx, browser.Request{URL: url}, "full", 300, p1.NextCursor)
	if err != nil {
		t.Fatalf("page 2 (first): %v", err)
	}
	p2b, err := br.Read(ctx, browser.Request{URL: url}, "full", 300, p1.NextCursor)
	if err != nil {
		t.Fatalf("page 2 (repeat): %v", err)
	}
	if p2a.Markdown != p2b.Markdown {
		t.Fatal("repeat cursor read of page 2 differs")
	}
	if p2a.Markdown == p1.Markdown {
		t.Fatal("page 2 identical to page 1")
	}
	if p2a.Page != 2 {
		t.Fatalf("cursor read reports page %d, want 2", p2a.Page)
	}
}
