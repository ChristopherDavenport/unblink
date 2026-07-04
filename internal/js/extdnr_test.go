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

// TestDynamicDNRRules verifies chrome.declarativeNetRequest.updateDynamicRules takes
// effect: a rule added at runtime blocks a subsequent page-JS request.
func TestDynamicDNRRules(t *testing.T) {
	bundle, err := webext.Load("../webext/testdata/adblock-mini")
	if err != nil {
		t.Fatal(err)
	}
	eng := js.New(js.WithTimeout(3*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	defer eng.Close()

	doc, err := html.Parse(strings.NewReader(`<html><body><div id="out">pending</div>
		<script>
		  chrome.declarativeNetRequest.updateDynamicRules({
		    addRules: [{ id: 100, action: { type: 'block' }, condition: { urlFilter: '||dyn-ad.example^' } }]
		  }).then(function () {
		    fetch('https://dyn-ad.example/beacon')
		      .then(function () { document.getElementById('out').textContent = 'reached'; })
		      .catch(function () { document.getElementById('out').textContent = 'blocked'; });
		  });
		</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.com/page")
	tr := &stubTransport{routes: map[string]string{}}
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := divText(t, doc, "out"); got != "blocked" {
		t.Errorf("#out = %q, want blocked (dynamic rule should cancel the request)", got)
	}
	if tr.count != 0 {
		t.Errorf("inner transport saw %d requests, want 0 (dynamically-blocked request must not reach the network)", tr.count)
	}
}
