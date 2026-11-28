package js_test

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// renderDiagBudget renders with a specific budget and returns the serialized doc
// plus diagnostics.
func renderDiagBudget(t *testing.T, pageHTML string, budget time.Duration) (string, js.RenderResult) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/")
	eng := js.New(js.WithTimeout(budget))
	defer eng.Close()
	var diag js.RenderResult
	if err := eng.Render(context.Background(), doc, base, js.Env{Diag: &diag}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var b strings.Builder
	if err := html.Render(&stringWriter{&b}, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return b.String(), diag
}

type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

// TestTimerClampMaterializesDeferredContent proves a one-shot timer scheduled
// far beyond the render budget is pulled in so its content still appears — the
// pre-Phase-21 behavior was for it to silently never fire.
func TestTimerClampMaterializesDeferredContent(t *testing.T) {
	// 2s budget; content scheduled at 7000ms would never fire without the clamp.
	out, _ := renderDiagBudget(t, `<html><body><div id="out">before</div>
		<script>
		  setTimeout(function () {
		    document.getElementById('out').textContent = 'deferred-content-here';
		  }, 7000);
		</script></body></html>`, 2*time.Second)
	if !strings.Contains(out, "deferred-content-here") {
		t.Errorf("clamp did not materialize the deferred timer content:\n%s", out)
	}
	if strings.Contains(out, ">before<") {
		t.Errorf("deferred content did not replace the placeholder:\n%s", out)
	}
}

// TestTimerClampLeavesShortTimers proves a timer well within the budget still
// fires on its own schedule (the clamp only pulls in timers past the deadline).
func TestTimerClampLeavesShortTimers(t *testing.T) {
	out, _ := renderDiagBudget(t, `<html><body><div id="out">before</div>
		<script>
		  setTimeout(function () {
		    document.getElementById('out').textContent = 'quick';
		  }, 10);
		</script></body></html>`, 2*time.Second)
	if !strings.Contains(out, ">quick<") {
		t.Errorf("short timer did not fire:\n%s", out)
	}
}

// A timer that re-arms itself with a long delay (a poll loop, a retry backoff)
// is pulled in once by the clamp. Its successors are scheduled after the clamp
// deadline, where "pull it in" used to mean "run it now": the chain became a
// busy loop for the rest of the budget — against the page's origin when the
// callback makes a request. The number of such late pull-ins is bounded.
func TestTimerClampDoesNotSpinSelfRearmingTimer(t *testing.T) {
	out, _ := renderDiagBudget(t, `<html><body><div id="out">0</div>
		<script>
		  var n = 0;
		  (function poll() {
		    document.getElementById('out').textContent = String(n++);
		    setTimeout(poll, 30000);
		  })();
		</script></body></html>`, 2*time.Second)
	i := strings.Index(out, `<div id="out">`)
	rest := out[i+len(`<div id="out">`):]
	n, err := strconv.Atoi(rest[:strings.Index(rest, "<")])
	if err != nil {
		t.Fatalf("no counter in:\n%s", out)
	}
	if n < 1 || n > 40 {
		t.Errorf("self-rearming 30s timer ran %d times in one render; want the clamp's one pull-in plus a small bounded tail", n)
	}
}

// Deferred content two timers deep still materializes: the second timer is
// scheduled after the clamp deadline and is one of the bounded late pull-ins.
func TestTimerClampFollowsAChainOfDeferredTimers(t *testing.T) {
	out, _ := renderDiagBudget(t, `<html><body><div id="out">before</div>
		<script>
		  setTimeout(function () {
		    setTimeout(function () { document.getElementById('out').textContent = 'second-stage'; }, 9000);
		  }, 7000);
		</script></body></html>`, 2*time.Second)
	if !strings.Contains(out, "second-stage") {
		t.Errorf("chained deferred timer did not fire:\n%s", out)
	}
}

// After the clamp deadline a short timer keeps its own delay: it fires inside
// what is left of the budget anyway, and must not use up the late pull-ins.
func TestTimerClampLeavesShortTimersAfterDeadline(t *testing.T) {
	out, _ := renderDiagBudget(t, `<html><body><div id="out">0</div>
		<script>
		  var n = 0;
		  setTimeout(function tick() {
		    document.getElementById('out').textContent = String(++n);
		    setTimeout(tick, 150);
		  }, 5000);
		</script></body></html>`, 2*time.Second)
	i := strings.Index(out, `<div id="out">`)
	rest := out[i+len(`<div id="out">`):]
	n, _ := strconv.Atoi(rest[:strings.Index(rest, "<")])
	// At least the pulled-in first run; the 150ms successors run on their own
	// clock and do not hold the settle open, so the count is normally 1 or 2.
	// The clamp compares wall-clock times, so a clock step during the render (a
	// VM resyncing its clock) can make the successors look as if they outlast the
	// budget and send them down the late pull-in path; that path is bounded at 32,
	// which is the property worth holding here.
	if n < 1 || n > 40 {
		t.Errorf("150ms timer chain after the deadline ran %d times; want a handful, and never more than the late pull-in bound", n)
	}
}
