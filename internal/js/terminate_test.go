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

// renderReturns renders page with a 1s budget and reports whether Render came
// back within limit. A render that does not come back has leaked a goroutine
// spinning a core; the test fails rather than hanging the suite.
func renderReturns(t *testing.T, page string, limit time.Duration) (time.Duration, bool) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	eng := js.New(js.WithTimeout(time.Second))
	start := time.Now()
	returned := make(chan struct{})
	go func() {
		_ = eng.Render(context.Background(), doc, base, js.Env{})
		close(returned)
	}()
	select {
	case <-returned:
		return time.Since(start), true
	case <-time.After(limit):
		return time.Since(start), false
	}
}

// The render watchdog interrupts a script that never yields. An interrupt is
// one-shot: it unwinds that one call and is cleared. The event loop then starts
// the next queued callback, and if that one never returns either, nothing was
// left to stop it — the settle's closing tick could not run, and Render waited
// on it forever while a core spun. One line of page JavaScript did it:
// setInterval(function () { for (;;) {} }, 10).
func TestRenderReturnsWhenASecondRunawayTaskIsQueued(t *testing.T) {
	for name, page := range map[string]string{
		"timers": `<html><body><p>content</p><script>
			setTimeout(function spin1() { setTimeout(function spin2() { for (;;) {} }, 20); for (;;) {} }, 0);
		</script></body></html>`,
		"interval": `<html><body><p>content</p><script>
			setInterval(function () { for (;;) {} }, 10);
		</script></body></html>`,
		"listener": `<html><body><p>content</p><script>
			window.addEventListener('load', function () { setTimeout(function () { for (;;) {} }, 30); for (;;) {} });
		</script></body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			elapsed, ok := renderReturns(t, page, 10*time.Second)
			if !ok {
				t.Fatalf("Render did not return %v after a 1s budget: the event loop is stuck in page JS", elapsed)
			}
			if elapsed > 3*time.Second {
				t.Errorf("Render took %v for a 1s budget", elapsed)
			}
		})
	}
}

// A live context survives a runaway page too: the operation returns, and so
// does Close, with a callback that never returns still queued.
func TestLiveContextSurvivesRunawayCallbacks(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body><button id="b">go</button><script>
		document.getElementById('b').addEventListener('click', function () {
			setInterval(function () { for (;;) {} }, 10);
		});
	</script></body></html>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	eng := js.New(js.WithTimeout(time.Second))
	lc, err := eng.Open(context.Background(), doc, base, js.Env{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	finished := make(chan string, 1)
	go func() {
		if _, err := lc.Dispatch(context.Background(), js.Action{Selector: "#b", Type: "click"}); err != nil {
			finished <- "dispatch: " + err.Error()
			return
		}
		// The interval is spinning now; a second operation must still come back.
		if _, err := lc.Dispatch(context.Background(), js.Action{Selector: "#b", Type: "hover"}); err != nil {
			finished <- "second dispatch: " + err.Error()
			return
		}
		lc.Close()
		finished <- ""
	}()
	select {
	case msg := <-finished:
		if msg != "" {
			t.Fatal(msg)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a live context with a runaway interval never returned from Dispatch/Close")
	}
}
