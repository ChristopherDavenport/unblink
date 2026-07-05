package js_test

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// headerRecorder captures the outgoing header map per request URL, so tests can
// assert what secFetchTransport attached. It serves a permissive (ACAO:*) JS body
// so cross-origin fetches aren't CORS-blocked before we observe them.
type headerRecorder struct {
	mu   sync.Mutex
	seen map[string]map[string]string
}

func (r *headerRecorder) Do(_ context.Context, _ string, url string, headers map[string]string, _ []byte) (*js.Response, error) {
	r.mu.Lock()
	cp := make(map[string]string, len(headers))
	for k, v := range headers {
		cp[k] = v
	}
	if r.seen == nil {
		r.seen = map[string]map[string]string{}
	}
	r.seen[url] = cp
	r.mu.Unlock()
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "application/javascript", "access-control-allow-origin": "*"}, Body: []byte(""), FinalURL: url}, nil
}

func (r *headerRecorder) get(sub string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for u, h := range r.seen {
		if strings.Contains(u, sub) {
			return h
		}
	}
	return nil
}

func TestSecFetchOnSubrequests(t *testing.T) {
	rec := &headerRecorder{}
	page := `<html><body>
		<script src="/app.js"></script>
		<script>fetch('https://api.other.com/data');</script>
		</body></html>`
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://page.com/here")
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: rec}); err != nil {
		t.Fatalf("render: %v", err)
	}

	// Same-origin external script → dest script, mode no-cors, site same-origin.
	if h := rec.get("/app.js"); h == nil {
		t.Fatal("script request not recorded")
	} else if h["Sec-Fetch-Dest"] != "script" || h["Sec-Fetch-Mode"] != "no-cors" || h["Sec-Fetch-Site"] != "same-origin" {
		t.Errorf("script Sec-Fetch = dest:%q mode:%q site:%q, want script/no-cors/same-origin", h["Sec-Fetch-Dest"], h["Sec-Fetch-Mode"], h["Sec-Fetch-Site"])
	}

	// Cross-origin fetch → dest empty, mode cors, site cross-site.
	if h := rec.get("api.other.com"); h == nil {
		t.Fatal("fetch request not recorded")
	} else if h["Sec-Fetch-Dest"] != "empty" || h["Sec-Fetch-Mode"] != "cors" || h["Sec-Fetch-Site"] != "cross-site" {
		t.Errorf("fetch Sec-Fetch = dest:%q mode:%q site:%q, want empty/cors/cross-site", h["Sec-Fetch-Dest"], h["Sec-Fetch-Mode"], h["Sec-Fetch-Site"])
	}
}
