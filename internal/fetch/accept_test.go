package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const testMarkdownAccept = "text/markdown, text/html;q=0.9, */*;q=0.8"

// acceptRecorder serves /start as a redirect to /target and records the Accept
// header each path was requested with.
type acceptRecorder struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (a *acceptRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if a.seen == nil {
		a.seen = map[string][]string{}
	}
	a.seen[r.URL.Path] = append(a.seen[r.URL.Path], r.Header.Get("Accept"))
	a.mu.Unlock()
	if r.URL.Path == "/start" {
		http.Redirect(w, r, "/target", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

func (a *acceptRecorder) last(path string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.seen[path]
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

// WithAccept replaces the default Accept for that one request — including each
// redirect hop — and leaves the same client's other requests on the default.
func TestWithAcceptOverridesDefault(t *testing.T) {
	rec := &acceptRecorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	const def = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	if _, err := c.Get(context.Background(), srv.URL+"/plain"); err != nil {
		t.Fatal(err)
	}
	if got := rec.last("/plain"); got != def {
		t.Errorf("default Accept = %q, want %q", got, def)
	}

	ctx := WithAccept(context.Background(), testMarkdownAccept)
	if _, err := c.Get(ctx, srv.URL+"/start"); err != nil {
		t.Fatal(err)
	}
	if got := rec.last("/start"); got != testMarkdownAccept {
		t.Errorf("negotiated Accept on first hop = %q, want %q", got, testMarkdownAccept)
	}
	if got := rec.last("/target"); got != testMarkdownAccept {
		t.Errorf("negotiated Accept after redirect = %q, want %q", got, testMarkdownAccept)
	}

	// The override was scoped to that context: the shared client is unchanged.
	if _, err := c.Get(context.Background(), srv.URL+"/plain"); err != nil {
		t.Fatal(err)
	}
	if got := rec.last("/plain"); got != def {
		t.Errorf("Accept after a negotiated request = %q, want the default %q", got, def)
	}

	// An empty accept is a no-op, not an empty header.
	if _, err := c.Get(WithAccept(context.Background(), ""), srv.URL+"/empty"); err != nil {
		t.Fatal(err)
	}
	if got := rec.last("/empty"); got != def {
		t.Errorf("Accept with empty override = %q, want the default %q", got, def)
	}
}

// The override also wins on the --tls-mimic path, whose Chrome persona otherwise
// fills Accept; the rest of that persona is still sent.
func TestWithAcceptOverridesTLSMimic(t *testing.T) {
	var mu sync.Mutex
	var accept, secCHUA string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accept, secCHUA = r.Header.Get("Accept"), r.Header.Get("sec-ch-ua")
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c, err := New(WithTLSMimic(true), withInsecureTLS())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	mimicDefault := accept
	mu.Unlock()
	if mimicDefault == "" || mimicDefault == testMarkdownAccept {
		t.Fatalf("tls-mimic default Accept = %q, want the Chrome persona", mimicDefault)
	}

	if _, err := c.Get(WithAccept(context.Background(), testMarkdownAccept), srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if accept != testMarkdownAccept {
		t.Errorf("tls-mimic negotiated Accept = %q, want %q", accept, testMarkdownAccept)
	}
	if secCHUA == "" {
		t.Error("tls-mimic negotiated request dropped the sec-ch-ua persona header")
	}
}
