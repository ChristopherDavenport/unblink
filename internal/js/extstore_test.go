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

// renderScript renders a one-off page with a single inline script and returns the doc.
func renderScript(t *testing.T, script, baseURL string, bundle *webext.Bundle) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(`<html><body><div id="out">pending</div><script>` + script + `</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(baseURL)
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	defer eng.Close()
	if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return doc
}

// TestExtensionStoragePersistence writes to chrome.storage.local in one engine and
// reads it back in a fresh engine, proving local storage survives process restarts.
func TestExtensionStoragePersistence(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // isolate the on-disk store

	load := func() *webext.Bundle {
		b, err := webext.Load("../webext/testdata/adblock-mini")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	doc1 := renderScript(t, `chrome.storage.local.set({ saved: 'kept' }, function () {
		document.getElementById('out').textContent = 'written';
	});`, "https://example.com/", load())
	if got := divText(t, doc1, "out"); got != "written" {
		t.Fatalf("write phase #out = %q, want written", got)
	}

	// A brand-new engine (same on-disk root) must see the persisted value.
	doc2 := renderScript(t, `chrome.storage.local.get('saved', function (res) {
		document.getElementById('out').textContent = res.saved || 'MISSING';
	});`, "https://example.com/", load())
	if got := divText(t, doc2, "out"); got != "kept" {
		t.Errorf("read phase #out = %q, want kept (local storage should persist)", got)
	}
}

// TestExtensionStorageOnChanged verifies chrome.storage.onChanged fires when a value is
// set, delivering the change record to the listener.
func TestExtensionStorageOnChanged(t *testing.T) {
	bundle, err := webext.Load("../webext/testdata/adblock-mini")
	if err != nil {
		t.Fatal(err)
	}
	doc := renderScript(t, `
		chrome.storage.onChanged.addListener(function (changes, area) {
			if (changes.k) {
				document.getElementById('out').textContent = changes.k.newValue + ':' + area;
			}
		});
		chrome.storage.local.set({ k: 'v' });
	`, "https://example.com/", bundle)
	if got := divText(t, doc, "out"); got != "v:local" {
		t.Errorf("#out = %q, want v:local (onChanged should deliver the change)", got)
	}
}
