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

// TestMV2WebRequestBlocking loads an MV2 extension whose background cancels tracker
// requests via a blocking webRequest.onBeforeRequest listener, and verifies a matching
// page-JS fetch is cancelled while an unrelated one still reaches the network.
func TestMV2WebRequestBlocking(t *testing.T) {
	bundle, err := webext.Load("../webext/testdata/webrequest-mini")
	if err != nil {
		t.Fatal(err)
	}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	defer eng.Close()

	doc, err := html.Parse(strings.NewReader(`<html><body><div id="out">pending</div><div id="out2">pending</div>
		<script>
		  fetch('https://tracker.example/beacon')
		    .then(function () { document.getElementById('out').textContent = 'reached'; })
		    .catch(function () { document.getElementById('out').textContent = 'blocked'; });
		  fetch('https://safe.example/data')
		    .then(function () { document.getElementById('out2').textContent = 'ok'; })
		    .catch(function () { document.getElementById('out2').textContent = 'failed'; });
		</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.com/page")
	tr := &stubTransport{routes: map[string]string{"safe.example": "SAFE"}}
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
		t.Fatalf("render: %v", err)
	}

	if got := divText(t, doc, "out"); got != "blocked" {
		t.Errorf("#out = %q, want blocked (webRequest listener should cancel the tracker)", got)
	}
	if got := divText(t, doc, "out2"); got != "ok" {
		t.Errorf("#out2 = %q, want ok (unrelated request should pass)", got)
	}
	if tr.count != 1 {
		t.Errorf("inner transport saw %d requests, want 1 (only the allowed request reaches the network)", tr.count)
	}
}
