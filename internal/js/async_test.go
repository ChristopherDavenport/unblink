package js_test

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// stubTransport serves canned bodies by URL substring, with no real network.
type stubTransport struct {
	mu     sync.Mutex
	routes map[string]string
	count  int
}

func (s *stubTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	for k, v := range s.routes {
		if strings.Contains(url, k) {
			return &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte(v), FinalURL: url}, nil
		}
	}
	return &js.Response{Status: 404, Body: []byte("not found"), FinalURL: url}, nil
}

func renderWith(t *testing.T, pageHTML string, tr js.Transport) string {
	t.Helper()
	return renderWithEnv(t, pageHTML, js.Env{Transport: tr})
}

func renderWithEnv(t *testing.T, pageHTML string, env js.Env) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, env); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String()
}

func TestWindowFetch(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/api": "FETCHED-OK"}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  fetch('/api').then(function(r){ return r.text(); })
		    .then(function(t){ document.getElementById('out').textContent = t; });
		</script></body></html>`, tr)
	if !strings.Contains(out, "FETCHED-OK") {
		t.Errorf("fetch result not rendered:\n%s", out)
	}
}

func TestFetchFromDOMContentLoaded(t *testing.T) {
	// fetch issued from a later callback — the case the inline-keepalive fix targets.
	tr := &stubTransport{routes: map[string]string{"/api": "DCL-OK"}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  document.addEventListener('DOMContentLoaded', function(){
		    fetch('/api').then(function(r){ return r.text(); })
		      .then(function(t){ document.getElementById('out').textContent = t; });
		  });
		</script></body></html>`, tr)
	if !strings.Contains(out, "DCL-OK") {
		t.Errorf("fetch from DOMContentLoaded did not settle:\n%s", out)
	}
}

func TestChainedFetch(t *testing.T) {
	// Second fetch starts from a .then continuation (deferred-increment race case).
	tr := &stubTransport{routes: map[string]string{"/a": "AA", "/b": "BB"}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  fetch('/a').then(function(r){ return r.text(); }).then(function(t1){
		    return fetch('/b').then(function(r){ return r.text(); }).then(function(t2){
		      document.getElementById('out').textContent = t1 + '-' + t2;
		    });
		  });
		</script></body></html>`, tr)
	if !strings.Contains(out, "AA-BB") {
		t.Errorf("chained fetch did not settle:\n%s", out)
	}
}

func TestXHR(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/api": "XHR-OK"}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  var x = new XMLHttpRequest();
		  x.onload = function(){ document.getElementById('out').textContent = x.responseText; };
		  x.open('GET', '/api'); x.send();
		</script></body></html>`, tr)
	if !strings.Contains(out, "XHR-OK") {
		t.Errorf("XHR result not rendered:\n%s", out)
	}
}

func TestExternalScript(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/app.js": `document.getElementById('out').textContent = 'EXT-RAN';`}}
	out := renderWith(t, `<html><head><script src="/app.js"></script></head>
		<body><div id="out">x</div></body></html>`, tr)
	if !strings.Contains(out, "EXT-RAN") {
		t.Errorf("external script did not run:\n%s", out)
	}
}

func TestDOMContentLoadedMutation(t *testing.T) {
	// No network needed; just lifecycle dispatch.
	out := renderWith(t, `<html><body>
		<script>
		  document.addEventListener('DOMContentLoaded', function(){ document.body.classList.add('ready'); });
		</script></body></html>`, nil)
	if !strings.Contains(out, `class="ready"`) {
		t.Errorf("DOMContentLoaded listener did not run:\n%s", out)
	}
}

func TestNoNetworkRejects(t *testing.T) {
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  fetch('/api').then(
		    function(){ document.getElementById('out').textContent = 'SHOULD-NOT'; },
		    function(){ document.getElementById('out').textContent = 'REJECTED'; });
		</script></body></html>`, nil)
	if !strings.Contains(out, "REJECTED") {
		t.Errorf("fetch should reject without a transport:\n%s", out)
	}
}
