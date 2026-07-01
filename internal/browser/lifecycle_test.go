package browser_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/session"
)

func serveDynamicHTML(t *testing.T, body func(r *http.Request) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" || r.URL.Path == "/llms.txt" || r.URL.Path == "/llms-full.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body(r)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An evicted credentialed session must surface a session_expired error telling
// the agent to re-create and re-authenticate — never be silently rebuilt as an
// anonymous session that then fetches gated pages unauthenticated.
func TestExpiredSessionSurfacesTypedError(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return "<html><body><h1>Hi</h1><p>content here</p></body></html>"
	})
	b, err := browser.New(browser.WithAllowPrivate(true), browser.WithSessionLimits(10*time.Millisecond, 0))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	if _, err := b.NewSession("auth", session.Config{Bearer: "tok", Origin: srv.URL}); err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, err := b.Read(ctx, browser.Request{SessionID: "auth", URL: srv.URL}, "", 0, ""); err != nil {
		t.Fatalf("first read: %v", err)
	}
	time.Sleep(30 * time.Millisecond) // exceed the idle TTL

	_, err = b.Read(ctx, browser.Request{SessionID: "auth", URL: srv.URL}, "", 0, "")
	be := browser.Classify(err)
	if err == nil || be.Code != browser.ErrSessionExpired {
		t.Fatalf("read after eviction: err=%v code=%v, want session_expired", err, be.Code)
	}
	if !strings.Contains(be.Message, "re-attach its credentials") {
		t.Errorf("expired-credentialed message should tell the agent to re-authenticate: %q", be.Message)
	}

	// The session tool paths report the same code for the same id...
	if _, err := b.SessionState("auth"); browser.Classify(err).Code != browser.ErrSessionExpired {
		t.Errorf("SessionState after eviction = %v, want session_expired", err)
	}
	// ...and a never-seen id is distinct.
	if _, err := b.SessionState("never-seen"); browser.Classify(err).Code != browser.ErrUnknownSession {
		t.Errorf("SessionState(unknown) = %v, want unknown_session", err)
	}

	// Deliberate re-creation works and clears the tombstone.
	if _, err := b.NewSession("auth", session.Config{Bearer: "tok", Origin: srv.URL}); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if _, err := b.Read(ctx, browser.Request{SessionID: "auth", URL: srv.URL}, "", 0, ""); err != nil {
		t.Errorf("read after re-create: %v", err)
	}
}

// Re-creating a live session with new credentials must error, not silently keep
// the old config.
func TestRecreateLiveSessionWithAuthErrors(t *testing.T) {
	b, err := browser.New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.NewSession("dup", session.Config{}); err != nil {
		t.Fatal(err)
	}
	_, err = b.NewSession("dup", session.Config{Bearer: "tok", Origin: "https://example.com"})
	if browser.Classify(err).Code != browser.ErrBadInput {
		t.Errorf("credentialed re-create = %v, want bad_input", err)
	}
}

// A page with no distinct article body — e.g. an unhydrated SPA shell — must
// report the article→full fallback, not claim mode=article ran.
func TestArticleFallbackReported(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return `<html><head><title>app</title></head><body><div id="app"></div><script src="/x.js"></script></body></html>`
	})
	b, err := browser.New(browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	r, err := b.Read(context.Background(), browser.Request{URL: srv.URL}, "article", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !r.ArticleFallback || r.Mode != "full" {
		t.Errorf("mode=%q fallback=%v, want full/true for an article-less page", r.Mode, r.ArticleFallback)
	}
}

// A cursor issued for one version of a page must error with cursor_expired when
// the content changes, never silently serve page 1 (or a wrong page).
func TestStaleCursorReported(t *testing.T) {
	var version atomic.Int32
	srv := serveDynamicHTML(t, func(*http.Request) string {
		var sb strings.Builder
		sb.WriteString("<html><body>")
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&sb, "<p>version %d paragraph %d with some padding text to fill the token budget.</p>", version.Load(), i)
		}
		sb.WriteString("</body></html>")
		return sb.String()
	})
	b, err := browser.New(browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	// Session reads bypass the shared URL cache, so the second read re-fetches.
	req := browser.Request{SessionID: "cur", URL: srv.URL}
	r1, err := b.Read(ctx, req, "full", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if r1.NextCursor == "" {
		t.Fatal("fixture did not paginate; enlarge it")
	}

	version.Store(1) // the page changes under the agent
	_, err = b.Read(ctx, req, "full", 100, r1.NextCursor)
	if browser.Classify(err).Code != browser.ErrCursorExpired {
		t.Errorf("read with stale cursor = %v, want cursor_expired", err)
	}
}

// An uncaught script error during a JS render must be visible in the result.
func TestReadSurfacesJSErrors(t *testing.T) {
	srv := serveDynamicHTML(t, func(*http.Request) string {
		return `<html><body><p>static content</p><script>throw new Error("boom: hydration failed")</script></body></html>`
	})
	b, err := browser.New(browser.WithJS(2*time.Second), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	r, err := b.Read(context.Background(), browser.Request{URL: srv.URL, Render: true}, "full", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.JSErrors) == 0 {
		t.Fatal("expected js_errors for a throwing script")
	}
	if !strings.Contains(strings.Join(r.JSErrors, " "), "boom") {
		t.Errorf("js_errors = %q, want the thrown message", r.JSErrors)
	}
}
