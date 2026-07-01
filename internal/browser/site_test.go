package browser_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

const sitePage = `<!doctype html><html><head><title>Home</title></head><body>` +
	`<h1>Home</h1><p>A real page with enough prose to look like content.</p>` +
	`<a href="/page">More</a></body></html>`

type siteHits struct{ robots, llms, llmsFull int64 }

// siteServer is a well-behaved host: an HTML page on every non-metadata path,
// plus a text robots.txt, a Markdown llms.txt, and a HEAD-able llms-full.txt.
func siteServer(t *testing.T) (*httptest.Server, *siteHits) {
	t.Helper()
	h := &siteHits{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, sitePage)
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&h.robots, 1)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "User-agent: *\nDisallow: /private\nCrawl-delay: 7\n"+
			"Sitemap: https://example.com/sitemap.xml\n")
	})
	mux.HandleFunc("/llms.txt", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&h.llms, 1)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = io.WriteString(w, "# Example Docs\n\n> A short summary.\n\n## Guides\n- [Intro](/intro): start here\n")
	})
	mux.HandleFunc("/llms-full.txt", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&h.llmsFull, 1)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.WriteString(w, "# Full Docs\n\nlots of content\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, h
}

func TestSite(t *testing.T) {
	srv, _ := siteServer(t)
	b := newBrowser(t)
	ctx := context.Background()

	r, err := b.Site(ctx, req(srv.URL+"/page"))
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if r.Robots == nil || !r.Robots.Present {
		t.Fatalf("robots = %+v, want present", r.Robots)
	}
	if r.Robots.CrawlDelay != 7 {
		t.Errorf("crawl-delay = %v, want 7", r.Robots.CrawlDelay)
	}
	if len(r.Robots.Sitemaps) != 1 || !strings.Contains(r.Robots.Sitemaps[0], "sitemap.xml") {
		t.Errorf("sitemaps = %v", r.Robots.Sitemaps)
	}
	if !r.Robots.AllowedForUs {
		t.Errorf("/page should be allowed, got disallowed")
	}
	if r.LLMsTxt == nil || r.LLMsTxt.Title != "Example Docs" {
		t.Fatalf("llms_txt = %+v, want title 'Example Docs'", r.LLMsTxt)
	}
	if !strings.Contains(r.LLMsTxt.Content, "## Guides") {
		t.Errorf("llms_txt content missing section:\n%s", r.LLMsTxt.Content)
	}
	if !r.LLMsFullAvailable {
		t.Error("llms-full.txt should be reported available")
	}

	// A disallowed path is reported (but never blocks).
	rp, err := b.Site(ctx, req(srv.URL+"/private/secret"))
	if err != nil {
		t.Fatalf("site private: %v", err)
	}
	if rp.Robots.AllowedForUs {
		t.Error("/private/secret should be disallowed for us")
	}
}

// bareServer serves a page but 404s every origin-root metadata file.
func bareServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt", "/llms.txt", "/llms-full.txt":
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, sitePage)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSiteRobotsAbsent(t *testing.T) {
	srv := bareServer(t)
	b := newBrowser(t)
	r, err := b.Site(context.Background(), req(srv.URL+"/page"))
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if r.Robots.Present {
		t.Error("robots should be absent")
	}
	if !r.Robots.AllowedForUs {
		t.Error("absent robots.txt means allow-all")
	}
	if r.Robots.Note == "" {
		t.Error("expected an explanatory note for absent robots.txt")
	}
	if r.LLMsTxt != nil {
		t.Errorf("llms_txt should be absent, got %+v", r.LLMsTxt)
	}
	if r.LLMsFullAvailable {
		t.Error("llms-full.txt should be unavailable")
	}
}

// softServer 200s its HTML shell for every path, including the metadata files.
func softServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, sitePage)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSiteSoftFourOhFour(t *testing.T) {
	srv := softServer(t)
	b := newBrowser(t)
	r, err := b.Site(context.Background(), req(srv.URL+"/"))
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if r.Robots.Present {
		t.Error("an HTML shell must not be treated as robots.txt")
	}
	if r.LLMsTxt != nil {
		t.Error("an HTML shell must not be treated as llms.txt")
	}
	if r.LLMsFullAvailable {
		t.Error("an HTML shell must not be treated as llms-full.txt")
	}
}

func TestSiteLLMsTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/llms.txt" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, sitePage)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = io.WriteString(w, "# Big Guide\n")
		line := strings.Repeat("word ", 20) + "\n"
		for i := 0; i < 3000; i++ {
			_, _ = io.WriteString(w, line)
		}
	}))
	t.Cleanup(srv.Close)

	b := newBrowser(t)
	r, err := b.Site(context.Background(), req(srv.URL+"/"))
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if r.LLMsTxt == nil || !r.LLMsTxt.Truncated {
		t.Fatalf("expected truncated llms_txt, got %+v", r.LLMsTxt)
	}
	if r.LLMsTxt.Tokens > browser.DefaultLLMsMaxTokens+50 {
		t.Errorf("truncated tokens = %d, want <= %d", r.LLMsTxt.Tokens, browser.DefaultLLMsMaxTokens+50)
	}
	if r.LLMsTxt.Title != "Big Guide" {
		t.Errorf("title = %q, want 'Big Guide'", r.LLMsTxt.Title)
	}
}

func TestBrowseSiteHints(t *testing.T) {
	srv, _ := siteServer(t)
	b := newBrowser(t)
	r, err := b.Browse(context.Background(), req(srv.URL+"/page"))
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if !r.LLMsTxt {
		t.Error("expected llms_txt hint = true")
	}
	if r.Robots == nil {
		t.Fatal("expected a robots hint")
	}
	if r.Robots.CrawlDelay != 7 || !r.Robots.AllowedForUs {
		t.Errorf("robots hint = %+v", r.Robots)
	}
}

func TestSiteHostCache(t *testing.T) {
	srv, h := siteServer(t)
	b := newBrowser(t)
	ctx := context.Background()

	if _, err := b.Site(ctx, req(srv.URL+"/page")); err != nil {
		t.Fatalf("site: %v", err)
	}
	robots1, llms1 := atomic.LoadInt64(&h.robots), atomic.LoadInt64(&h.llms)
	if robots1 != 1 || llms1 != 1 {
		t.Fatalf("first probe counts: robots=%d llms=%d, want 1/1", robots1, llms1)
	}

	// A second lookup on the same origin (different path) and a Browse must reuse
	// the host cache — no new origin-root requests.
	if _, err := b.Site(ctx, req(srv.URL+"/other")); err != nil {
		t.Fatalf("site other: %v", err)
	}
	if _, err := b.Browse(ctx, req(srv.URL+"/page")); err != nil {
		t.Fatalf("browse: %v", err)
	}
	if got := atomic.LoadInt64(&h.robots); got != robots1 {
		t.Errorf("robots.txt re-fetched %d times; cache miss", got-robots1)
	}
	if got := atomic.LoadInt64(&h.llms); got != llms1 {
		t.Errorf("llms.txt re-fetched %d times; cache miss", got-llms1)
	}
}

func TestBrowseSiteHintsDisabled(t *testing.T) {
	srv, h := siteServer(t)
	b, err := browser.New(browser.WithSiteHints(false), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatalf("new browser: %v", err)
	}
	r, err := b.Browse(context.Background(), req(srv.URL+"/page"))
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if r.LLMsTxt || r.Robots != nil {
		t.Errorf("hints should be omitted when disabled: %+v", r)
	}
	if got := atomic.LoadInt64(&h.robots); got != 0 {
		t.Errorf("disabled hints still probed robots.txt %d times", got)
	}
}

func TestSiteNoSessionHistory(t *testing.T) {
	srv, _ := siteServer(t)
	b := newBrowser(t)
	ctx := context.Background()
	const sid = "s"

	if _, err := b.Browse(ctx, browser.Request{SessionID: sid, URL: srv.URL + "/page"}); err != nil {
		t.Fatalf("browse: %v", err)
	}
	before, err := b.SessionHistoryOf(sid)
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	// site within the session (with a URL) must not record navigation history.
	if _, err := b.Site(ctx, browser.Request{SessionID: sid, URL: srv.URL + "/other"}); err != nil {
		t.Fatalf("site: %v", err)
	}
	after, err := b.SessionHistoryOf(sid)
	if err != nil {
		t.Fatalf("history after: %v", err)
	}
	if len(after.URLs) != len(before.URLs) {
		t.Errorf("site changed session history: before %v, after %v", before.URLs, after.URLs)
	}
}
