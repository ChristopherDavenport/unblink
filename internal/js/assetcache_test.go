package js_test

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// urlCountTransport serves canned bodies by URL substring and counts requests
// per matched route, so tests can assert exactly which URLs hit the network.
type urlCountTransport struct {
	mu     sync.Mutex
	routes map[string]string
	hits   map[string]int
}

func (s *urlCountTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.routes {
		if strings.Contains(url, k) {
			if s.hits == nil {
				s.hits = map[string]int{}
			}
			s.hits[k]++
			return &js.Response{Status: 200, Headers: map[string]string{"content-type": "application/javascript"}, Body: []byte(v), FinalURL: url}, nil
		}
	}
	return &js.Response{Status: 404, Body: []byte("not found"), FinalURL: url}, nil
}

func (s *urlCountTransport) count(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[route]
}

func renderTwice(t *testing.T, eng *js.Engine, pageHTML string, tr js.Transport) {
	t.Helper()
	base, _ := url.Parse("https://example.com/app")
	for i := 0; i < 2; i++ {
		doc, err := html.Parse(strings.NewReader(pageHTML))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		var buf bytes.Buffer
		if err := html.Render(&buf, doc); err != nil {
			t.Fatalf("serialize: %v", err)
		}
		if !strings.Contains(buf.String(), "cache-marker") {
			t.Fatalf("render %d did not produce content:\n%s", i, buf.String())
		}
	}
}

// A second render of the same page must serve the external script from the
// asset cache — one network fetch total — and still render identically.
func TestAssetCacheReusesScriptBytes(t *testing.T) {
	tr := &urlCountTransport{routes: map[string]string{
		"/app.js": "document.getElementById('out').textContent = 'cache-marker';",
	}}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithAssetCache(time.Minute))
	defer eng.Close()
	renderTwice(t, eng, `<html><body><div id="out"></div>
		<script src="/app.js"></script></body></html>`, tr)
	if got := tr.count("/app.js"); got != 1 {
		t.Errorf("script fetched %d times, want 1 (cached on repeat render)", got)
	}
}

// Page data requests (fetch/XHR) must NEVER be served from the asset cache:
// every render re-fetches them live.
func TestAssetCacheNeverCachesFetch(t *testing.T) {
	tr := &urlCountTransport{routes: map[string]string{
		"/api/data": "live-data",
		"/app.js": `fetch('/api/data').then(function(r){return r.text();}).then(function(v){
			document.getElementById('out').textContent = 'cache-marker:' + v; });`,
	}}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithAssetCache(time.Minute))
	defer eng.Close()
	renderTwice(t, eng, `<html><body><div id="out"></div>
		<script src="/app.js"></script></body></html>`, tr)
	if got := tr.count("/api/data"); got != 2 {
		t.Errorf("data fetch hit the network %d times, want 2 (never cached)", got)
	}
	if got := tr.count("/app.js"); got != 1 {
		t.Errorf("script fetched %d times, want 1", got)
	}
}

// An expired entry is refetched.
func TestAssetCacheTTLExpiry(t *testing.T) {
	tr := &urlCountTransport{routes: map[string]string{
		"/app.js": "document.getElementById('out').textContent = 'cache-marker';",
	}}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithAssetCache(time.Nanosecond))
	defer eng.Close()
	renderTwice(t, eng, `<html><body><div id="out"></div>
		<script src="/app.js"></script></body></html>`, tr)
	if got := tr.count("/app.js"); got != 2 {
		t.Errorf("script fetched %d times, want 2 (TTL expired between renders)", got)
	}
}

// Module scripts: the source fetch AND the esbuild bundle are cached, and the
// second render still mounts.
func TestAssetCacheReusesModuleBundle(t *testing.T) {
	tr := &urlCountTransport{routes: map[string]string{
		"/mod.js": "export default function(){ document.getElementById('out').textContent = 'cache-marker'; }",
	}}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithAssetCache(time.Minute))
	defer eng.Close()
	renderTwice(t, eng, `<html><body><div id="out"></div>
		<script type="module">import run from '/mod.js'; run();</script></body></html>`, tr)
	if got := tr.count("/mod.js"); got != 1 {
		t.Errorf("module fetched %d times, want 1 (bundle + source cached)", got)
	}
}

// With the cache disabled (the --js-asset-cache=false posture), every render
// re-fetches assets.
func TestAssetCacheDisabledRefetches(t *testing.T) {
	tr := &urlCountTransport{routes: map[string]string{
		"/app.js": "document.getElementById('out').textContent = 'cache-marker';",
	}}
	eng := js.New(js.WithTimeout(3 * time.Second)) // no WithAssetCache
	defer eng.Close()
	renderTwice(t, eng, `<html><body><div id="out"></div>
		<script src="/app.js"></script></body></html>`, tr)
	if got := tr.count("/app.js"); got != 2 {
		t.Errorf("script fetched %d times, want 2 (cache disabled)", got)
	}
}
