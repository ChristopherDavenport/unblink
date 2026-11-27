package browser_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// TestRenderStatusTextIsServerReasonPhrase drives the real transport end to
// end: XHR and fetch() must see the reason phrase the server put on its
// HTTP/1.1 status line, not a constant.
func TestRenderStatusTextIsServerReasonPhrase(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><div id="out">loading</div>
<script>
var x = new XMLHttpRequest();
x.open('GET', '/missing');
x.onloadend = function(){
  fetch('/missing').then(function(r){
    document.getElementById('out').innerHTML = '<article><h1>status text</h1><p>xhr=' + x.status + ' ' +
      x.statusText + '; fetch=' + r.status + ' ' + r.statusText +
      '; rendered with enough prose for the reducer to keep this content around.</p></article>';
  });
};
x.send();
</script></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b, err := browser.New(browser.WithJS(3*time.Second), browser.WithJSAllowPrivate(true), browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatalf("new browser: %v", err)
	}
	r, err := b.Read(context.Background(), browser.Request{URL: srv.URL + "/page", Render: true}, "full", 6000, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"xhr=404 Not Found;", "fetch=404 Not Found;"} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("want %q in output; got:\n%s", want, r.Markdown)
		}
	}
}
