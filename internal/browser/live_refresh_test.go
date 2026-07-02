package browser_test

// Tests for the live-session refresh skip: a session read after an interact
// must show the mutated content (staleness), an unchanged live DOM must not
// break repeat reads (the skip path), and Interact.Changed must track whether
// the dispatch actually mutated the DOM (version bracketing).

import (
	"context"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

const liveCounterPage = `<!doctype html><html><body>
	<article><h1>Counter</h1>
	<p>A counter page with enough prose around the value that reduction keeps the
	body text intact across repeated reads of the same session page.</p>
	<p id="count">count:0</p></article>
	<button id="inc">increment</button>
	<button id="noop">noop</button>
	<script>
	  var n = 0;
	  document.getElementById('inc').addEventListener('click', function () {
	    n++;
	    document.getElementById('count').textContent = 'count:' + n;
	  });
	  document.getElementById('noop').addEventListener('click', function () {});
	</script></body></html>`

func readSession(t *testing.T, b *browser.Browser, sid string) string {
	t.Helper()
	r, err := b.Read(context.Background(), browser.Request{SessionID: sid, UseCurrent: true}, "full", 6000, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return r.Markdown
}

// Interact -> read -> interact -> read: every read reflects the latest live
// DOM, and the intermediate no-change read (served via the version-skip path)
// returns the same content instead of a stale or broken page.
func TestSessionReadTracksLiveMutations(t *testing.T) {
	srv := liveServer(t, liveCounterPage)
	b := newJSBrowser(t)
	ctx := context.Background()
	seedSession(t, b, "s", srv.URL)

	if r, err := b.Interact(ctx, "s", "#inc", "click", ""); err != nil || !r.Matched {
		t.Fatalf("interact 1: matched=%v err=%v", r != nil && r.Matched, err)
	}
	first := readSession(t, b, "s")
	if !strings.Contains(first, "count:1") {
		t.Fatalf("read after interact missing mutated content:\n%s", first)
	}

	// No mutation between these reads: the second is served via the skip path
	// and must be identical.
	second := readSession(t, b, "s")
	if second != first {
		t.Errorf("repeat read of unchanged live page differs\nfirst:  %s\nsecond: %s", first, second)
	}

	if r, err := b.Interact(ctx, "s", "#inc", "click", ""); err != nil || !r.Matched {
		t.Fatalf("interact 2: matched=%v err=%v", r != nil && r.Matched, err)
	}
	third := readSession(t, b, "s")
	if !strings.Contains(third, "count:2") {
		t.Errorf("read after second interact is stale (skip failed to invalidate):\n%s", third)
	}
}

// Changed reports whether the dispatch mutated the DOM: true for a real
// mutation, false for a handler that does nothing.
func TestInteractChangedTracksMutation(t *testing.T) {
	srv := liveServer(t, liveCounterPage)
	b := newJSBrowser(t)
	ctx := context.Background()
	seedSession(t, b, "s", srv.URL)

	r, err := b.Interact(ctx, "s", "#inc", "click", "")
	if err != nil {
		t.Fatalf("interact inc: %v", err)
	}
	if !r.Changed {
		t.Error("mutating click reported Changed=false")
	}

	r, err = b.Interact(ctx, "s", "#noop", "click", "")
	if err != nil {
		t.Fatalf("interact noop: %v", err)
	}
	if r.Changed {
		t.Error("no-op click reported Changed=true")
	}
}
