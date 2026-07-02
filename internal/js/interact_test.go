package js_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// openContext loads pageHTML into a persistent live runtime with the given per-op
// timeout and returns the handle plus a cleanup func.
func openContext(t *testing.T, pageHTML string, timeout time.Duration) (js.LiveContext, func()) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	eng := js.New(js.WithTimeout(timeout))
	lc, err := eng.Open(context.Background(), doc, base, js.Env{})
	if err != nil {
		eng.Close()
		t.Fatalf("open: %v", err)
	}
	return lc, func() { lc.Close(); eng.Close() }
}

// openContextWith is openContext with a Transport wired in (for chunk/module loading).
func openContextWith(t *testing.T, pageHTML string, timeout time.Duration, tr js.Transport) (js.LiveContext, func()) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	eng := js.New(js.WithTimeout(timeout))
	lc, err := eng.Open(context.Background(), doc, base, js.Env{Transport: tr})
	if err != nil {
		eng.Close()
		t.Fatalf("open: %v", err)
	}
	return lc, func() { lc.Close(); eng.Close() }
}

func snapshot(t *testing.T, lc js.LiveContext) string {
	t.Helper()
	b, _, err := lc.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return string(b)
}

func dispatch(t *testing.T, lc js.LiveContext, selector, event string) bool {
	t.Helper()
	res, err := lc.Dispatch(context.Background(), js.Action{Selector: selector, Type: event})
	if err != nil {
		t.Fatalf("dispatch %s: %v", selector, err)
	}
	return res.Matched
}

// The load script runs exactly once at Open; if a later Dispatch re-ran it (as the
// old replay model did) __loads would climb. A true session keeps it at 1.
func TestContextLoadRunsOnce(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="b">go</button><div id="out">?</div>
		<script>
		  window.__loads = (window.__loads || 0) + 1;
		  document.getElementById('b').addEventListener('click', function () {
		    document.getElementById('out').textContent = 'loads:' + window.__loads;
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#b", "click")
	dispatch(t, lc, "#b", "click")
	if out := snapshot(t, lc); !strings.Contains(out, "loads:1") {
		t.Errorf("load script should run once for the session, got:\n%s", out)
	}
}

// State (a closure variable) accumulates across dispatches — it is not reset.
func TestContextStatePersists(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="b">+</button><div id="out">0</div>
		<script>
		  var n = 0;
		  document.getElementById('b').addEventListener('click', function () {
		    n++; document.getElementById('out').textContent = String(n);
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#b", "click")
	dispatch(t, lc, "#b", "click")
	dispatch(t, lc, "#b", "click")
	if out := snapshot(t, lc); !strings.Contains(out, ">3<") {
		t.Errorf("counter should persist to 3 across dispatches, got:\n%s", out)
	}
}

func TestContextDispatchAsyncSettles(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="b">go</button><div id="out">x</div>
		<script>
		  document.getElementById('b').addEventListener('click', function () {
		    setTimeout(function () { document.getElementById('out').textContent = 'async-done'; }, 0);
		  });
		</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#b", "click")
	if out := snapshot(t, lc); !strings.Contains(out, "async-done") {
		t.Errorf("async mutation should settle before snapshot, got:\n%s", out)
	}
}

func TestContextDispatchUnmatched(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body><div id="out">x</div>
		<script>/* no handlers */</script></body></html>`, 2*time.Second)
	defer cleanup()

	if matched := dispatch(t, lc, "#missing", "click"); matched {
		t.Error("expected matched=false for a selector that resolves to nothing")
	}
}

// After a runaway handler is interrupted, the runtime must remain usable (the
// interrupt flag is cleared before the next op runs).
func TestContextInterruptThenReuse(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="spin">spin</button><button id="ok">ok</button><div id="out">x</div>
		<script>
		  document.getElementById('spin').addEventListener('click', function () { while (true) {} });
		  document.getElementById('ok').addEventListener('click', function () {
		    document.getElementById('out').textContent = 'ok-fired';
		  });
		</script></body></html>`, 150*time.Millisecond)
	defer cleanup()

	// Interrupted by the watchdog; must not error or wedge the loop.
	dispatch(t, lc, "#spin", "click")
	// The runtime is still usable.
	dispatch(t, lc, "#ok", "click")
	if out := snapshot(t, lc); !strings.Contains(out, "ok-fired") {
		t.Errorf("runtime should be reusable after an interrupt, got:\n%s", out)
	}
}
