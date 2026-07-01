package js_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// A one-shot render records a location.href assignment to a different document as a
// pending navigation (it never performs the real fetch).
func TestRenderRecordsPendingNavigationViaHref(t *testing.T) {
	_, diag, _ := renderWaitDiag(t, `<html><body>
		<script>location.href = "https://example.com/next";</script></body></html>`, js.Env{}, time.Second)
	if diag.PendingNavigation != "https://example.com/next" {
		t.Errorf("PendingNavigation = %q, want https://example.com/next", diag.PendingNavigation)
	}
}

// location.assign resolves relative to the current page and is likewise recorded.
func TestRenderRecordsPendingNavigationViaAssign(t *testing.T) {
	_, diag, _ := renderWaitDiag(t, `<html><body>
		<script>location.assign("/other");</script></body></html>`, js.Env{}, time.Second)
	if diag.PendingNavigation != "https://example.com/other" {
		t.Errorf("PendingNavigation = %q, want https://example.com/other", diag.PendingNavigation)
	}
}

// A fragment-only change stays on the same document and must not be reported as a
// navigation.
func TestRenderHashChangeIsNotNavigation(t *testing.T) {
	_, diag, _ := renderWaitDiag(t, `<html><body>
		<script>location.href = "#section";</script></body></html>`, js.Env{}, time.Second)
	if diag.PendingNavigation != "" {
		t.Errorf("a hash change is not a navigation, got PendingNavigation = %q", diag.PendingNavigation)
	}
}

// In a live session, a handler that sets location.href surfaces as a pending
// navigation, and the value is cleared once read.
func TestDispatchRecordsPendingNavigation(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="go">go</button>
		<script>document.getElementById('go').addEventListener('click', function () {
			location.href = 'https://example.com/dest';
		});</script></body></html>`, 2*time.Second)
	defer cleanup()

	if !dispatch(t, lc, "#go", "click") {
		t.Fatal("selector should have matched")
	}
	nav, err := lc.PendingNavigation(context.Background())
	if err != nil {
		t.Fatalf("pending navigation: %v", err)
	}
	if nav != "https://example.com/dest" {
		t.Errorf("PendingNavigation = %q, want https://example.com/dest", nav)
	}
	if nav2, _ := lc.PendingNavigation(context.Background()); nav2 != "" {
		t.Errorf("pending navigation should be cleared after reading, got %q", nav2)
	}
}

// Case A verification: SPA client-side routing (history.pushState + in-place DOM
// update) is captured by the live snapshot and does NOT report a pending navigation.
func TestPushStateRouteVisibleWithoutPendingNavigation(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="nav">next</button>
		<div id="view">home</div>
		<script>document.getElementById('nav').addEventListener('click', function () {
			history.pushState({}, '', '/page2');
			document.getElementById('view').textContent = 'page-two-content';
		});</script></body></html>`, 2*time.Second)
	defer cleanup()

	dispatch(t, lc, "#nav", "click")
	if out := snapshot(t, lc); !strings.Contains(out, "page-two-content") {
		t.Errorf("SPA route content should be in the snapshot:\n%s", out)
	}
	if nav, _ := lc.PendingNavigation(context.Background()); nav != "" {
		t.Errorf("client-side routing must not report a pending navigation, got %q", nav)
	}
}
