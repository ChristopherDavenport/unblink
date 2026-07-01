package js_test

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// renderWaitDiag renders pageHTML with env under the given engine timeout and returns
// the serialized DOM, the render diagnostics, and the wall-clock the render took.
func renderWaitDiag(t *testing.T, pageHTML string, env js.Env, timeout time.Duration) (string, js.RenderResult, time.Duration) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	eng := js.New(js.WithTimeout(timeout))
	var diag js.RenderResult
	env.Diag = &diag
	start := time.Now()
	if err := eng.Render(context.Background(), doc, base, env); err != nil {
		t.Fatalf("render: %v", err)
	}
	elapsed := time.Since(start)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String(), diag, elapsed
}

// delayedTransport answers every request with body after a fixed delay, so a test can
// exercise content that lands well after the page's scripts first run.
type delayedTransport struct {
	delay time.Duration
	body  string
}

func (d *delayedTransport) Do(ctx context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte(d.body), FinalURL: url}, nil
}

// A setInterval that neither mutates the DOM nor makes requests keeps the event loop
// busy forever. The old drain-based render burned the whole budget; the quiet-period
// settle detects that the page is idle and returns fast.
func TestRenderSettlesEarlyOnQuietInterval(t *testing.T) {
	out, _, elapsed := renderWaitDiag(t, `<html><body><div id="out">ready</div>
		<script>setInterval(function () { var x = 1 + 1; }, 5);</script>
		</body></html>`, js.Env{}, 2*time.Second)
	if !strings.Contains(out, "ready") {
		t.Fatalf("content missing:\n%s", out)
	}
	if elapsed > time.Second {
		t.Errorf("a quiet setInterval page should settle well under the 2s budget, took %v", elapsed)
	}
}

// A setInterval that mutates the DOM every tick never goes DOM-quiet, so the render
// runs to the budget — but it must still return the content produced so far.
func TestRenderPersistentMutationRunsToBudgetAndKeepsContent(t *testing.T) {
	out, _, elapsed := renderWaitDiag(t, `<html><body><ul id="list"></ul>
		<script>setInterval(function () {
			var li = document.createElement('li'); li.textContent = 'x';
			document.getElementById('list').appendChild(li);
		}, 5);</script></body></html>`, js.Env{}, 300*time.Millisecond)
	if !strings.Contains(out, "<li>") {
		t.Fatalf("mutated content missing:\n%s", out)
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("a persistently-mutating page should run to the ~300ms budget, took %v", elapsed)
	}
}

// wait_for holds the render open until the selector appears via a delayed fetch, and
// reports that the condition was met.
func TestRenderWaitForMet(t *testing.T) {
	env := js.Env{
		Transport: &delayedTransport{delay: 100 * time.Millisecond, body: "LOADED"},
		Wait:      &js.WaitCondition{Selector: "#result"},
	}
	out, diag, _ := renderWaitDiag(t, `<html><body><div id="app"></div>
		<script>
		  fetch('/api').then(function (r) { return r.text(); }).then(function (t) {
		    var d = document.createElement('div'); d.id = 'result'; d.textContent = t;
		    document.getElementById('app').appendChild(d);
		  });
		</script></body></html>`, env, 2*time.Second)
	if !diag.WaitRequested || !diag.WaitMet {
		t.Errorf("want WaitRequested && WaitMet, got %+v", diag)
	}
	if !strings.Contains(out, "LOADED") {
		t.Errorf("awaited content missing:\n%s", out)
	}
}

// wait_for for content that never appears runs to the budget (not instantly, not
// forever) and reports the condition unmet.
func TestRenderWaitForNotMet(t *testing.T) {
	env := js.Env{Wait: &js.WaitCondition{Selector: "#never"}}
	_, diag, elapsed := renderWaitDiag(t, `<html><body><div id="app">static</div>
		<script>window.__x = 1;</script></body></html>`, env, 200*time.Millisecond)
	if !diag.WaitRequested || diag.WaitMet {
		t.Errorf("want WaitRequested && !WaitMet, got %+v", diag)
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("an unmet wait should hold the render to the ~200ms budget, took %v", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("an unmet wait must not hang, took %v", elapsed)
	}
}

// Env.Timeout extends both the render budget and the per-fetch timeout, so content
// that lands after the engine's default budget is still awaited.
func TestRenderWaitTimeoutExtendsBudget(t *testing.T) {
	env := js.Env{
		Transport: &delayedTransport{delay: 400 * time.Millisecond, body: "LATE"},
		Wait:      &js.WaitCondition{Text: "LATE"},
		Timeout:   3 * time.Second,
	}
	out, diag, elapsed := renderWaitDiag(t, `<html><body><div id="app"></div>
		<script>fetch('/api').then(function (r) { return r.text(); }).then(function (t) {
			document.getElementById('app').textContent = t;
		});</script></body></html>`, env, 150*time.Millisecond) // short engine default, overridden by Env.Timeout
	if !diag.WaitMet {
		t.Errorf("want WaitMet once the extended timeout allows the late content, got %+v", diag)
	}
	if !strings.Contains(out, "LATE") {
		t.Errorf("late content missing:\n%s", out)
	}
	if elapsed < 350*time.Millisecond {
		t.Errorf("should have waited past the default budget for late content, took %v", elapsed)
	}
}
