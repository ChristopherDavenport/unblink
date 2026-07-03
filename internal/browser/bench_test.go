package browser_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// benchFixtureServer serves the longread fixture as HTML on every path, so
// cold-read benchmarks can mint unique URLs freely.
func benchFixtureServer(b *testing.B) *httptest.Server {
	b.Helper()
	body, err := os.ReadFile("../../eval/corpus/longread.input.html")
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	}))
}

// benchBrowser builds a browser tuned for benchmarking: loopback fetches
// allowed (httptest) and the per-host rate limiter off, so iteration speed
// measures the pipeline rather than the limiter.
func benchBrowser(b *testing.B) *browser.Browser {
	b.Helper()
	br, err := browser.New(browser.WithAllowPrivate(true), browser.WithRateLimit(0, 0))
	if err != nil {
		b.Fatalf("browser.New: %v", err)
	}
	return br
}

// BenchmarkReadArticleCached reads one warm URL repeatedly: the fetch is a
// cache hit, so each iteration is exactly the per-read compute (reduce + emit
// + defang + paginate) that runs on every read even when the page is cached.
func BenchmarkReadArticleCached(b *testing.B) {
	srv := benchFixtureServer(b)
	defer srv.Close()
	br := benchBrowser(b)
	defer br.Close()
	ctx := context.Background()
	url := srv.URL + "/post"
	if _, err := br.Read(ctx, browser.Request{URL: url}, "article", 6000, ""); err != nil {
		b.Fatalf("warm read: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := br.Read(ctx, browser.Request{URL: url}, "article", 6000, ""); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
}

// BenchmarkReadArticleCold reads a unique URL each iteration: the full
// fetch -> parse -> extract -> reduce -> emit pipeline over loopback.
func BenchmarkReadArticleCold(b *testing.B) {
	srv := benchFixtureServer(b)
	defer srv.Close()
	br := benchBrowser(b)
	defer br.Close()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		url := fmt.Sprintf("%s/post/%d", srv.URL, i)
		if _, err := br.Read(ctx, browser.Request{URL: url}, "article", 6000, ""); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
}

// BenchmarkReadConcurrent drives parallel reads over a small warm URL set —
// the throughput dimension (cache mutex, connection pool, shared state).
func BenchmarkReadConcurrent(b *testing.B) {
	srv := benchFixtureServer(b)
	defer srv.Close()
	br := benchBrowser(b)
	defer br.Close()
	ctx := context.Background()
	urls := make([]string, 8)
	for i := range urls {
		urls[i] = fmt.Sprintf("%s/warm/%d", srv.URL, i)
		if _, err := br.Read(ctx, browser.Request{URL: urls[i]}, "article", 6000, ""); err != nil {
			b.Fatalf("warm read: %v", err)
		}
	}
	var next atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			url := urls[next.Add(1)%int64(len(urls))]
			if _, err := br.Read(ctx, browser.Request{URL: url}, "article", 6000, ""); err != nil {
				b.Fatalf("read: %v", err)
			}
		}
	})
}

// BenchmarkReadPaginatedCursor reads page 2 of a warm multi-page document —
// the pagination hot path. Before the per-entry markdown memo every cursor
// page recomputed reduce+emit over the whole document and discarded the other
// chunks; now it reslices the memoized markdown.
func BenchmarkReadPaginatedCursor(b *testing.B) {
	srv := benchFixtureServer(b)
	defer srv.Close()
	br := benchBrowser(b)
	defer br.Close()
	ctx := context.Background()
	url := srv.URL + "/post"
	first, err := br.Read(ctx, browser.Request{URL: url}, "full", 300, "")
	if err != nil {
		b.Fatalf("warm read: %v", err)
	}
	if first.NextCursor == "" {
		b.Fatal("fixture did not paginate at 300 tokens")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := br.Read(ctx, browser.Request{URL: url}, "full", 300, first.NextCursor); err != nil {
			b.Fatalf("cursor read: %v", err)
		}
	}
}
