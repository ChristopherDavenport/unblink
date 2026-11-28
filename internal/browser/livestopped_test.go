package browser_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// runawayAfterClick arms, on click, a callback that starts spinning only after
// the interact has settled — i.e. while the session's live runtime is idle.
const runawayAfterClick = `<html><body><main><h1>Runaway</h1><p id="out">before</p>
<button id="go">go</button></main><script>
document.getElementById('go').addEventListener('click', function () {
	document.getElementById('out').textContent = 'clicked';
	setTimeout(function () { setInterval(function () { for (;;) {} }, 10); }, 1300);
});
</script></body></html>`

func newRunawayBrowser(t *testing.T) *browser.Browser {
	t.Helper()
	b, err := browser.New(browser.WithJS(time.Second), browser.WithAllowPrivate(true),
		browser.WithJSLiveTaskBudget(150*time.Millisecond))
	if err != nil {
		t.Fatalf("new browser: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

// A session whose page runs away between tool calls has its live runtime shut
// down; the next interact says so (and that state is lost) instead of hanging or
// failing opaquely, and the one after starts a fresh runtime.
func TestInteractReportsStoppedLiveRuntime(t *testing.T) {
	srv := serveHTML(t, runawayAfterClick)
	b := newRunawayBrowser(t)
	ctx := context.Background()
	seedSession(t, b, "s", srv.URL)
	if _, err := b.Interact(ctx, "s", "#go", "click", "", ""); err != nil {
		t.Fatalf("first interact: %v", err)
	}
	var stopped *browser.Error
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		_, err := b.Interact(ctx, "s", "#out", "hover", "", "")
		if errors.As(err, &stopped) && stopped.Code == browser.ErrLiveStopped {
			break
		}
		stopped = nil
		if err != nil {
			t.Fatalf("interact while the page was running away: %v", err)
		}
	}
	if stopped == nil {
		t.Fatal("the runaway runtime was never reported as stopped")
	}
	if !stopped.Retryable || !strings.Contains(stopped.Message, "state") {
		t.Errorf("stop error should be retryable and say in-page state is lost: %+v", stopped)
	}
	// The retry opens a fresh runtime from the page's original bytes.
	r, err := b.Interact(ctx, "s", "#out", "hover", "", "")
	if err != nil || !r.Matched {
		t.Fatalf("interact after the stop: matched=%v err=%v", r != nil && r.Matched, err)
	}
}

// A read of the session after the stop returns the last captured state and says
// the page's JavaScript is no longer running.
func TestReadReportsStoppedLiveRuntime(t *testing.T) {
	srv := serveHTML(t, runawayAfterClick)
	b := newRunawayBrowser(t)
	ctx := context.Background()
	seedSession(t, b, "s", srv.URL)
	if _, err := b.Interact(ctx, "s", "#go", "click", "", ""); err != nil {
		t.Fatalf("interact: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		r, err := b.Read(ctx, browser.Request{SessionID: "s", UseCurrent: true}, "full", 0, "")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(r.JSErrors) > 0 && strings.Contains(r.JSErrors[0], "live JavaScript runtime stopped") {
			if !strings.Contains(r.Markdown, "clicked") {
				t.Errorf("read after the stop lost the last captured state: %q", r.Markdown)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read never reported the stopped runtime; js_errors=%v", r.JSErrors)
		}
		time.Sleep(300 * time.Millisecond)
	}
	st, err := b.SessionState("s")
	if err == nil && st.LiveJS {
		t.Error("session still reports a live runtime after it was stopped")
	}
}
