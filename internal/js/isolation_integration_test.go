package js_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// renderIsolation renders at baseURL with the given response headers and returns
// the "<crossOriginIsolated>:<isSecureContext>" the page observed.
func renderIsolation(t *testing.T, baseURL string, h http.Header) string {
	t.Helper()
	page := `<html><body><div id="out"></div><script>
		document.getElementById('out').textContent =
		  String(window.crossOriginIsolated) + ':' + String(window.isSecureContext);
	</script></body></html>`
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse(baseURL)
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, js.Env{ResponseHeaders: h}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return divText(t, doc, "out")
}

func TestCrossOriginIsolatedGlobals(t *testing.T) {
	isolated := http.Header{}
	isolated.Set("Cross-Origin-Opener-Policy", "same-origin")
	isolated.Set("Cross-Origin-Embedder-Policy", "require-corp")

	// Secure + COOP + COEP → crossOriginIsolated true, isSecureContext true.
	if got := renderIsolation(t, "https://page.com/", isolated); got != "true:true" {
		t.Errorf("isolated https render = %q, want true:true", got)
	}
	// Secure but no isolation headers → crossOriginIsolated false, secure true.
	if got := renderIsolation(t, "https://page.com/", http.Header{}); got != "false:true" {
		t.Errorf("secure non-isolated render = %q, want false:true", got)
	}
	// Insecure origin → both false even with the headers.
	if got := renderIsolation(t, "http://page.com/", isolated); got != "false:false" {
		t.Errorf("insecure render = %q, want false:false", got)
	}
}

// TestSharedArrayBufferAbsentUnderIsolation locks ADR 0013's key safety claim:
// crossOriginIsolated is a *truthful fidelity signal, not a capability gate*, so
// SharedArrayBuffer stays undefined even when crossOriginIsolated === true. (No
// test covered this before; it is the one non-obvious security property in 0013.)
func TestSharedArrayBufferAbsentUnderIsolation(t *testing.T) {
	isolated := http.Header{}
	isolated.Set("Cross-Origin-Opener-Policy", "same-origin")
	isolated.Set("Cross-Origin-Embedder-Policy", "require-corp")
	page := `<html><body><div id="out"></div><script>
		document.getElementById('out').textContent =
		  String(window.crossOriginIsolated) + ':' + (typeof SharedArrayBuffer);
	</script></body></html>`
	doc, _ := renderCSP(t, page, "https://page.com/", isolated, nil)
	if got := divText(t, doc, "out"); got != "true:undefined" {
		t.Errorf("#out = %q, want true:undefined (SAB must stay absent even when isolated)", got)
	}
}
