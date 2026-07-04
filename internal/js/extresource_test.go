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

// TestExtensionResourceServing verifies a content script's
// fetch(chrome.runtime.getURL("data.json")) resolves to the packaged extension file
// rather than hitting the network.
func TestExtensionResourceServing(t *testing.T) {
	bundle, err := webext.Load("../webext/testdata/resource-mini")
	if err != nil {
		t.Fatal(err)
	}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	defer eng.Close()

	doc, err := html.Parse(strings.NewReader(`<html><body><div id="res">pending</div></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://www.example.com/page")
	// A transport that would fail any *network* fetch — the extension resource must be
	// served without touching it.
	tr := &stubTransport{routes: map[string]string{}}
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := divText(t, doc, "res"); got != "from-extension" {
		t.Errorf("#res = %q, want from-extension (chrome-extension:// resource must be served from the bundle)", got)
	}
	if tr.count != 0 {
		t.Errorf("inner transport saw %d requests, want 0 (extension resource must not hit the network)", tr.count)
	}
}
