package js_test

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// funcTransport is a js.Transport backed by a function, for responses a real
// HTTP server cannot shape (a transcoded Body next to its Raw octets, exact
// header-name casing, a transport error).
type funcTransport func(rawURL string) (*js.Response, error)

func (f funcTransport) Do(_ context.Context, _, rawURL string, _ map[string]string, _ []byte) (*js.Response, error) {
	return f(rawURL)
}

// renderXHRDiag renders a page whose script is body over tr and returns the
// serialized document plus the render diagnostics.
func renderXHRDiag(t *testing.T, tr js.Transport, body string) (string, js.RenderResult) {
	t.Helper()
	page := `<html><body><div id="out">unset</div><script>` + xhrLogJS + body + `</script></body></html>`
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://site.test/page")
	var diag js.RenderResult
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr, Diag: &diag}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String(), diag
}

// wantReported asserts each want appears in the error-level console capture,
// which (unlike RenderResult.Errors, snapshotted before the settle) also covers
// exceptions reported from an async delivery.
func wantReported(t *testing.T, diag js.RenderResult, wants ...string) {
	t.Helper()
	var msgs []string
	for _, m := range diag.Console {
		if m.Level == "error" {
			msgs = append(msgs, m.Text)
		}
	}
	all := strings.Join(msgs, "\n")
	for _, want := range wants {
		if !strings.Contains(all, want) {
			t.Errorf("want %q reported on the console, got:\n%s", want, all)
		}
	}
}

// TestXHRThrowingHandlersDoNotStrandRequest proves an exception from an event
// handler or listener is reported and does not stop the other listeners or the
// rest of the delivery: the request still reaches DONE and fires load/loadend.
// The onreadystatechange below is the common idiom that throws at
// HEADERS_RECEIVED (status 200, responseText still empty).
func TestXHRThrowingHandlersDoNotStrandRequest(t *testing.T) {
	tr := funcTransport(func(string) (*js.Response, error) {
		return &js.Response{Status: 200, Headers: map[string]string{"content-type": "application/json"}, Body: []byte(`[7]`)}, nil
	})
	out, diag := renderXHRDiag(t, tr, `
	  var x = new XMLHttpRequest(), parsed = [], sendThrew = 'no';
	  x.addEventListener('load', function () { throw new Error('boom-listener'); });
	  watch(x);
	  x.onreadystatechange = function () { if (x.status === 200) parsed.push(JSON.parse(x.responseText)[0]); };
	  x.onloadstart = function () { throw new Error('boom-loadstart'); };
	  x.onprogress = function () { throw new Error('boom-progress'); };
	  x.addEventListener('loadend', function () {
	    show(log.join(' ') + ' | ' + x.readyState + ' ' + x.responseText + ' | parsed=' + parsed.join(',') + ' | sendThrew=' + sendThrew);
	  });
	  x.open('GET', '/data');
	  try { x.send(); } catch (e) { sendThrew = String(e); }
	`)
	wantOut(t, out, `rsc1 loadstart1 rsc2 rsc3 progress3 rsc4 load4 loadend4 | 4 [7] | parsed=7,7 | sendThrew=no`)
	wantReported(t, diag,
		"XMLHttpRequest readystatechange handler", "SyntaxError",
		"XMLHttpRequest loadstart handler", "boom-loadstart",
		"XMLHttpRequest progress handler", "boom-progress",
		"XMLHttpRequest load handler", "boom-listener")
}

// TestXHRThrowingHandlersOnFailure covers the request error steps: a throwing
// readystatechange/error handler still lets error and loadend fire.
func TestXHRThrowingHandlersOnFailure(t *testing.T) {
	tr := funcTransport(func(string) (*js.Response, error) { return nil, errors.New("connection refused") })
	out, diag := renderXHRDiag(t, tr, `
	  var x = new XMLHttpRequest();
	  x.onreadystatechange = function () { if (x.readyState === 4) throw new Error('boom-rsc'); };
	  x.onerror = function () { throw new Error('boom-error'); };
	  watch(x);
	  x.addEventListener('loadend', function () { show(log.join(' ') + ' | ' + x.readyState + ':' + x.status); });
	  x.open('GET', '/data'); x.send();
	`)
	wantOut(t, out, "rsc1 loadstart1 rsc4 error4 loadend4 | 4:0")
	wantReported(t, diag, "boom-rsc", "XMLHttpRequest error handler", "boom-error")
}

// scanJS deep-searches an object's own properties (enumerable or not) for a
// string containing the secret, returning the paths that hold it.
const scanJS = `
  function scan(root, secret) {
    var hits = [], seen = [];
    (function walk(v, path, depth) {
      if (typeof v === 'string') { if (v.indexOf(secret) >= 0) hits.push(path); return; }
      if (v === null || (typeof v !== 'object' && typeof v !== 'function') || depth > 6 || seen.indexOf(v) >= 0) return;
      seen.push(v);
      var names = Object.getOwnPropertyNames(v);
      for (var i = 0; i < names.length; i++) {
        var child; try { child = v[names[i]]; } catch (e) { continue; }
        walk(child, path + '.' + names[i], depth + 1);
      }
    })(root, 'o', 0);
    return hits;
  }
`

// TestSetCookieHiddenFromPageJS proves Set-Cookie / Set-Cookie2 never reach page
// script, whatever the header-name casing: not through the XHR getters, the XHR
// object's own properties, a fetch() Response's headers, or the raw object the
// internal fetch primitive resolves with.
func TestSetCookieHiddenFromPageJS(t *testing.T) {
	const secret = "s3cr3t-httponly"
	tr := funcTransport(func(string) (*js.Response, error) {
		return &js.Response{Status: 200, Headers: map[string]string{
			"Set-Cookie":   "sid=" + secret + "; HttpOnly",
			"SET-COOKIE2":  "sid2=" + secret,
			"content-type": "text/plain",
			"x-visible":    "yes",
		}, Body: []byte("ok")}, nil
	})
	out, _ := renderXHRDiag(t, tr, scanJS+`
	  var secret = '`+secret+`', r = [];
	  var x = new XMLHttpRequest();
	  x.onloadend = function (ev) {
	    r.push('get=' + x.getResponseHeader('Set-Cookie') + ',' + x.getResponseHeader('set-cookie2'));
	    r.push('all=' + x.getAllResponseHeaders().replace(/\r\n/g, ';'));
	    r.push('own=' + scan(x, secret).concat(scan(ev, secret)).join(','));
	    r.push('visible=' + x.getResponseHeader('X-Visible'));
	    fetch('/data').then(function (resp) {
	      var h = resp.headers, names = [], it = [];
	      h.forEach(function (v, k) { names.push(k); it.push(v); });
	      Array.from(h.entries()).forEach(function (p) { it.push(p.join('=')); });
	      Array.from(h.values()).forEach(function (v) { it.push(v); });
	      r.push('fetch=' + h.get('set-cookie') + ',' + h.get('Set-Cookie2') + ',' + h.has('set-cookie') + ',' + h.has('set-cookie2') +
	        ',' + (typeof h.getSetCookie === 'function' ? h.getSetCookie().length : 0) + ',' + h.get('x-visible'));
	      r.push('names=' + names.sort().join(','));
	      r.push('iter=' + (it.join('|').indexOf(secret) >= 0) + ' own=' + scan(resp, secret).join(','));
	      return __unblinkFetch('GET', '/data', {}, null, 'cors', 'same-origin');
	    }).then(function (raw) {
	      r.push('raw=' + scan(raw, secret).join(','));
	      show(r.join(' # '));
	    });
	  };
	  x.open('GET', '/data'); x.send();
	`)
	// The one expected occurrence is the page's own script source.
	if strings.Count(out, secret) != 1 {
		t.Errorf("secret leaked into the page:\n%s", out)
	}
	wantOut(t, out, "get=null,null # all=content-type: text/plain;x-visible: yes; # own= # visible=yes"+
		" # fetch=null,null,false,false,0,yes # names=content-type,x-visible # iter=false own= # raw=")
}

// TestXHRUserDefinedCharsetReadsRawOctets proves the x-user-defined
// binary-as-text idiom decodes the octets the server sent (Response.Raw), not
// the charset-transcoded body, and falls back to the body when no transcode
// happened (Raw absent, or Raw sharing Body's backing array).
func TestXHRUserDefinedCharsetReadsRawOctets(t *testing.T) {
	raw := []byte{0x00, 0x7f, 0x80, 0xff, 0xc3, 0x28, 0x89, 0x50}
	// What a windows-1252 → UTF-8 transcode makes of those octets.
	transcoded := []byte("\x00\x7f€ÿÃ(‰P")
	tr := funcTransport(func(u string) (*js.Response, error) {
		res := &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/plain"}}
		switch {
		case strings.HasSuffix(u, "/transcoded"):
			res.Body, res.Raw = transcoded, raw
		case strings.HasSuffix(u, "/shared"):
			res.Body, res.Raw = raw, raw
		default:
			res.Body = raw
		}
		return res, nil
	})
	out, _ := renderXHRDiag(t, tr, `
	  var paths = ['/transcoded', '/shared', '/noraw'], r = [];
	  function hex(s) {
	    var o = '';
	    for (var i = 0; i < s.length; i++) o += ('0' + (s.charCodeAt(i) & 0xff).toString(16)).slice(-2);
	    return o;
	  }
	  (function next() {
	    if (!paths.length) { show(r.join(' ')); return; }
	    var x = new XMLHttpRequest();
	    x.open('GET', paths.shift());
	    x.overrideMimeType('text/plain; charset=x-user-defined');
	    x.onload = function () { r.push(hex(x.responseText)); next(); };
	    x.send();
	  })();
	`)
	wantOut(t, out, "007f80ffc3288950 007f80ffc3288950 007f80ffc3288950")
}
