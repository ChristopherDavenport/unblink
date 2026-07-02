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
