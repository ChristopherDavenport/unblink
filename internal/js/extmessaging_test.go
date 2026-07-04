package js_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/webext"
)

// TestBackgroundMessaging drives a full render where a content script queries the
// background worker (uBlock's dynamic-cosmetic model) and awaits an async reply. It is
// also the ADR-0004 regression: the async ping round-trip must land before the render
// settles (else #ping-result would still read "pending").
func TestBackgroundMessaging(t *testing.T) {
	bundle, err := webext.Load("../webext/testdata/messaging-mini")
	if err != nil {
		t.Fatal(err)
	}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	defer eng.Close()

	doc, err := html.Parse(strings.NewReader(`<html><body>
		<div class="bg-ad">BG-AD-TEXT</div>
		<div id="bg-sponsored">BG-SPON-TEXT</div>
		<div id="ping-result">pending</div>
		<article id="content">REAL-CONTENT</article>
	</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://www.example.com/page")
	if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	// Async reply landed before settle (the pending bracket held the render open).
	if got := divText(t, doc, "ping-result"); got != "pong" {
		t.Errorf("#ping-result = %q, want pong (async message round-trip must land before settle)", got)
	}
	// The content script removed the elements the background told it to hide.
	out := serialize(t, doc)
	for _, gone := range []string{"BG-AD-TEXT", "BG-SPON-TEXT"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q should have been removed via background-provided selectors:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "REAL-CONTENT") {
		t.Errorf("real content should survive:\n%s", out)
	}
}

// TestExtensionStorageRoundTrip exercises the chrome.storage.local set→get path (an
// extension is loaded, so the chrome API is present in the page world).
func TestExtensionStorageRoundTrip(t *testing.T) {
	bundle, err := webext.Load("../webext/testdata/adblock-mini")
	if err != nil {
		t.Fatal(err)
	}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	defer eng.Close()

	doc, err := html.Parse(strings.NewReader(`<html><body><div id="out">pending</div>
		<script>
		  chrome.storage.local.set({ hello: 'world' }, function () {
		    chrome.storage.local.get('hello', function (res) {
		      document.getElementById('out').textContent = res.hello;
		    });
		  });
		</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.com/page")
	if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := divText(t, doc, "out"); got != "world" {
		t.Errorf("#out = %q, want world (chrome.storage.local set→get round-trip)", got)
	}
}
