package browser

// White-box, like revalidate_test.go: these cases need a site-cache entry to
// age, and backdating it is the only way to do that without sleeping.
// Everything else goes through the public API.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// robotsFlipServer serves an HTML page on every path and a robots.txt whose
// status and body the test can change; it counts requests per path.
type robotsFlipServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	body   string
	hits   map[string]int
}

func newRobotsFlipServer(t *testing.T, status int, body string) *robotsFlipServer {
	t.Helper()
	s := &robotsFlipServer{status: status, body: body, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		status, body := s.status, s.body
		s.mu.Unlock()
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><head><title>T</title></head><body><p>a page body with enough prose to survive reduction.</p></body></html>")
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *robotsFlipServer) flip(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

func (s *robotsFlipServer) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// siteEntry returns the site-cache entry for origin, failing if there is none.
func siteEntry(t *testing.T, b *Browser, origin string) siteCacheEntry {
	t.Helper()
	b.siteCache.mu.Lock()
	defer b.siteCache.mu.Unlock()
	e, ok := b.siteCache.entries[origin]
	if !ok {
		t.Fatalf("no site-cache entry for %s", origin)
	}
	return e
}

// ageSiteEntry makes origin's site-cache entry look d old.
func ageSiteEntry(t *testing.T, b *Browser, origin string, d time.Duration) {
	t.Helper()
	e := siteEntry(t, b, origin)
	b.siteCache.mu.Lock()
	e.at = time.Now().Add(-d)
	b.siteCache.entries[origin] = e
	b.siteCache.mu.Unlock()
}

func newEnforcingBrowser(t *testing.T) *Browser {
	t.Helper()
	// HTML cache entry under test: pin negotiation off, whatever the default.
	b, err := New(WithAllowPrivate(true), WithRespectRobots(true), WithRetries(0), WithPreferMarkdown(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

// A page already in the URL cache is refused once robots.txt disallows it: the
// cache hit goes through the same gate as a fresh fetch.
func TestRobotsCachedPageRefused(t *testing.T) {
	srv := newRobotsFlipServer(t, http.StatusOK, "User-agent: *\nDisallow:\n")
	b := newEnforcingBrowser(t)
	ctx := context.Background()
	url := srv.URL + "/private/page"

	if _, err := b.Read(ctx, Request{URL: url}, "full", 0, ""); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, ok := b.cache.get(cacheKey(url, false)); !ok {
		t.Fatal("page not cached")
	}

	// The site starts disallowing the path; the robots verdict is re-fetched once
	// its site-cache entry expires, while the page itself is still cached.
	srv.flip(http.StatusOK, "User-agent: *\nDisallow: /private\n")
	ageSiteEntry(t, b, srv.URL, 2*DefaultSiteCacheTTL)

	_, err := b.Read(ctx, Request{URL: url}, "full", 0, "")
	if !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("cached read after the rule change: err = %v, want ErrRobotsDisallowed", err)
	}
	if n := srv.count("/private/page"); n != 1 {
		t.Errorf("page requested %d times, want 1 (the refusal must not refetch)", n)
	}
}

// An unreachable robots.txt blocks its host only for robotsUnreachableTTL, not
// for the 10-minute site-cache TTL: once the 500 clears and the short TTL has
// passed, the host is reachable again.
func TestRobotsUnreachableExpiresQuickly(t *testing.T) {
	srv := newRobotsFlipServer(t, http.StatusInternalServerError, "")
	b := newEnforcingBrowser(t)
	ctx := context.Background()
	url := srv.URL + "/page"

	read := func() error {
		_, err := b.Read(ctx, Request{URL: url}, "full", 0, "")
		return err
	}
	if err := read(); !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("read while robots.txt is 500: err = %v, want ErrRobotsDisallowed", err)
	}
	if e := siteEntry(t, b, srv.URL); e.ttl != robotsUnreachableTTL || e.ttl >= DefaultSiteCacheTTL {
		t.Fatalf("unreachable entry ttl = %v, want %v (well under %v)", e.ttl, robotsUnreachableTTL, DefaultSiteCacheTTL)
	}

	// The outage ends. Within the short TTL the cached verdict still holds and
	// robots.txt is not re-probed on every call...
	srv.flip(http.StatusOK, "User-agent: *\nDisallow: /private\n")
	if err := read(); !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("read inside the unreachable TTL: err = %v, want still refused", err)
	}
	if n := srv.count("/robots.txt"); n != 1 {
		t.Errorf("robots.txt probed %d times inside the TTL, want 1", n)
	}
	// ...and just past it the host is reachable again.
	ageSiteEntry(t, b, srv.URL, robotsUnreachableTTL+time.Second)
	if err := read(); err != nil {
		t.Fatalf("read after the unreachable TTL: %v", err)
	}
	if n := srv.count("/page"); n != 1 {
		t.Errorf("page requested %d times, want 1", n)
	}
	// A reachable robots.txt goes back to the default TTL.
	if e := siteEntry(t, b, srv.URL); e.ttl != 0 {
		t.Errorf("reachable entry ttl = %v, want the cache default", e.ttl)
	}
}

// With enforcement off an unreachable robots.txt keeps the default TTL and the
// allow-all reporting it always had.
func TestRobotsUnreachableDefaultModeUnchanged(t *testing.T) {
	srv := newRobotsFlipServer(t, http.StatusInternalServerError, "")
	b, err := New(WithAllowPrivate(true), WithRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)

	s, err := b.Site(context.Background(), Request{URL: srv.URL + "/page"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Robots.AllowedForUs || s.Robots.Enforced || s.Robots.Note != "robots.txt returned 500; treating as allow-all" {
		t.Errorf("robots = %+v", s.Robots)
	}
	if e := siteEntry(t, b, srv.URL); e.ttl != 0 {
		t.Errorf("entry ttl = %v, want the cache default", e.ttl)
	}
}
