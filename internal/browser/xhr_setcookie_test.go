package browser_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// TestRenderHttpOnlyCookieHiddenFromPageJS drives the real transport end to
// end: a same-origin response sets an HttpOnly cookie, and page JS must not be
// able to obtain its value through XHR (getters or any own property of the
// object) or fetch() (headers.get / iteration) — while the cookie jar still
// stores it, so a later same-origin request sends it and the non-HttpOnly
// sibling shows up in document.cookie.
func TestRenderHttpOnlyCookieHiddenFromPageJS(t *testing.T) {
	// The page never holds the whole secret: it searches for a fragment, so any
	// occurrence of the full value in the output is a leak.
	const secretHead, secretTail = "s3cr3t-session", "-value-9f2c"
	const secret = secretHead + secretTail
	var sawCookie atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "sid="+secret+"; Path=/; HttpOnly")
		w.Header().Add("Set-Cookie", "vis=shown; Path=/")
		w.Header().Set("Set-Cookie2", "legacy="+secret)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sid"); err == nil && c.Value == secret {
			sawCookie.Store(true)
		}
		_, _ = io.WriteString(w, "checked")
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><div id="out">loading</div>
<script>
var needle = '`+secretHead+`';
function scan(root) {
  var hits = [], seen = [];
  (function walk(v, path, depth) {
    if (typeof v === 'string') { if (v.indexOf(needle) >= 0) hits.push(path + ':' + v); return; }
    if (v === null || (typeof v !== 'object' && typeof v !== 'function') || depth > 6 || seen.indexOf(v) >= 0) return;
    seen.push(v);
    var names = Object.getOwnPropertyNames(v);
    for (var i = 0; i < names.length; i++) {
      var child; try { child = v[names[i]]; } catch (e) { continue; }
      walk(child, path + '.' + names[i], depth + 1);
    }
  })(root, 'o', 0);
  return hits.join(',');
}
var x = new XMLHttpRequest(), r = [];
x.open('GET', '/login');
x.onloadend = function (ev) {
  r.push('xhrget=' + x.getResponseHeader('Set-Cookie') + '/' + x.getResponseHeader('set-cookie2'));
  r.push('xhrall=' + x.getAllResponseHeaders().toLowerCase().indexOf('cookie'));
  r.push('xhrown=' + (scan(x) + scan(ev) || 'none'));
  fetch('/login').then(function (resp) {
    var it = [];
    resp.headers.forEach(function (v, k) { it.push(k + '=' + v); });
    Array.from(resp.headers.entries()).forEach(function (p) { it.push(p.join('=')); });
    r.push('fetchget=' + resp.headers.get('set-cookie') + '/' + resp.headers.get('Set-Cookie2') + '/' + resp.headers.has('set-cookie'));
    r.push('fetchiter=' + it.join('|').toLowerCase().indexOf('cookie'));
    r.push('fetchown=' + (scan(resp) || 'none'));
    return fetch('/whoami');
  }).then(function () {
    r.push('doccookie=' + document.cookie);
    document.getElementById('out').innerHTML = '<article><h1>cookie probe</h1><p>' + r.join('; ') +
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
	if strings.Contains(r.Markdown, secret) {
		t.Errorf("HttpOnly cookie value leaked to page JS:\n%s", r.Markdown)
	}
	for _, want := range []string{
		"xhrget=null/null;", "xhrall=-1;", "xhrown=none;",
		"fetchget=null/null/false;", "fetchiter=-1;", "fetchown=none;",
		"doccookie=vis=shown;",
	} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("want %q in output; got:\n%s", want, r.Markdown)
		}
	}
	if !sawCookie.Load() {
		t.Error("the HttpOnly cookie was not stored: a later same-origin request did not send it")
	}
}

// TestRenderXHRUserDefinedCharsetIsBinarySafe serves a binary body as text/*
// (which the fetch layer charset-transcodes) and reads it back with the
// x-user-defined idiom: the page must get the server's original octets.
func TestRenderXHRUserDefinedCharsetIsBinarySafe(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte{0x00, 0x7f, 0x80, 0xff, 0xc3, 0x28, 0x89, 0x50})
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><div id="out">loading</div>
<script>
var x = new XMLHttpRequest();
x.open('GET', '/bin');
x.overrideMimeType('text/plain; charset=x-user-defined');
x.onload = function () {
  var s = x.responseText, o = '';
  for (var i = 0; i < s.length; i++) o += ('0' + (s.charCodeAt(i) & 0xff).toString(16)).slice(-2);
  document.getElementById('out').innerHTML = '<article><h1>binary probe</h1><p>hex=' + o +
    '; rendered with enough prose for the reducer to keep this content around.</p></article>';
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
	if want := "hex=007f80ffc3288950;"; !strings.Contains(r.Markdown, want) {
		t.Errorf("want %q in output; got:\n%s", want, r.Markdown)
	}
}
