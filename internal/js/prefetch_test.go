package js_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// routedTransport serves per-URL bodies with per-URL delays and counts every
// request, so tests can pin down both prefetch correctness (document-order
// execution under out-of-order completion) and accounting (each script body
// fetched exactly once).
type routedTransport struct {
	mu     sync.Mutex
	routes map[string]routedScript
	count  map[string]int
}

type routedScript struct {
	body  string
	delay time.Duration
}

func newRoutedTransport(routes map[string]routedScript) *routedTransport {
	return &routedTransport{routes: routes, count: make(map[string]int)}
}

func (rt *routedTransport) Do(ctx context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	rt.mu.Lock()
	r, ok := rt.routes[url]
	rt.count[url]++
	rt.mu.Unlock()
	if !ok {
		return &js.Response{Status: 404, FinalURL: url}, nil
	}
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "application/javascript"}, Body: []byte(r.body), FinalURL: url}, nil
}

func (rt *routedTransport) counts() map[string]int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make(map[string]int, len(rt.count))
	for k, v := range rt.count {
		out[k] = v
	}
	return out
}

// Prefetch overlaps the fetches but execution must stay strictly
// document-ordered: the transport completes the scripts in reverse order
// (first script slowest), and each script depends on its predecessor's global,
// so any out-of-order execution throws and the marker never lands. Each body
// must also be fetched exactly once — the prefetch result is consumed, not
// re-fetched by the synchronous path.
func TestRunScriptsPrefetchPreservesDocumentOrder(t *testing.T) {
	tr := newRoutedTransport(map[string]routedScript{
		"https://example.com/s1.js": {body: "window.order = ['s1'];", delay: 60 * time.Millisecond},
		"https://example.com/s2.js": {body: "window.order.push('s2');", delay: 30 * time.Millisecond},
		"https://example.com/s3.js": {body: "window.order.push('s3'); document.getElementById('out').textContent = window.order.join(',');", delay: 5 * time.Millisecond},
	})
	out, _, _ := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script src="/s1.js"></script>
		<script src="/s2.js"></script>
		<script src="/s3.js"></script>
		</body></html>`, js.Env{Transport: tr}, 5*time.Second)
	if !strings.Contains(out, "s1,s2,s3") {
		t.Fatalf("scripts did not execute in document order:\n%s", out)
	}
	for u, n := range tr.counts() {
		if n != 1 {
			t.Errorf("%s fetched %d times, want exactly 1", u, n)
		}
	}
}

// A failed prefetch skips that script — the same semantics as a failed
// synchronous fetch — without disturbing the scripts around it.
func TestRunScriptsPrefetchFailureSkipsScript(t *testing.T) {
	tr := newRoutedTransport(map[string]routedScript{
		"https://example.com/ok.js": {body: "document.getElementById('out').textContent = 'ran';"},
		// missing.js has no route -> 404 -> skip
	})
	out, _, _ := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script src="/missing.js"></script>
		<script src="/ok.js"></script>
		</body></html>`, js.Env{Transport: tr}, 5*time.Second)
	if !strings.Contains(out, "ran") {
		t.Fatalf("script after a failed fetch did not run:\n%s", out)
	}
}

// BenchmarkRenderMultiExternalScript is the prefetch showcase: three external
// scripts behind a 25ms-latency transport. Serial fetching pays ~75ms of RTT;
// concurrent prefetch pays ~25ms. (Loopback benchmarks can't show this — real
// RTT is the cost being removed.)
func BenchmarkRenderMultiExternalScript(b *testing.B) {
	const rtt = 25 * time.Millisecond
	tr := newRoutedTransport(map[string]routedScript{
		"https://example.com/a.js": {body: "window.a = 1;", delay: rtt},
		"https://example.com/b.js": {body: "window.b = window.a + 1;", delay: rtt},
		"https://example.com/c.js": {body: "document.getElementById('out').textContent = 'n=' + (window.b + 1);", delay: rtt},
	})
	pageHTML := `<html><body><div id="out"></div>
		<script src="/a.js"></script>
		<script src="/b.js"></script>
		<script src="/c.js"></script>
		</body></html>`
	benchRender(b, pageHTML, tr)
}
