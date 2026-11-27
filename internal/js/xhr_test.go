package js_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// httpTransport is a js.Transport over a real HTTP client, so XHR tests run
// against an httptest server: the request honours its context (cancellation and
// deadlines reach the handler) and the reason phrase comes off the status line.
type httpTransport struct{ client *http.Client }

func (h httpTransport) Do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (*js.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	hdr := make(map[string]string, len(res.Header))
	for k := range res.Header {
		hdr[strings.ToLower(k)] = res.Header.Get(k)
	}
	return &js.Response{
		Status:     res.StatusCode,
		StatusText: strings.TrimSpace(strings.TrimPrefix(res.Status, res.Status[:3])),
		Headers:    hdr,
		Body:       b,
		FinalURL:   rawURL,
	}, nil
}

// xhrLogJS records every XHR event as "<type><readyState>" into log[], the
// order the assertions below read back.
const xhrLogJS = `
  var log = [];
  function watch(x) {
    ['readystatechange','loadstart','progress','load','error','abort','timeout','loadend'].forEach(function (t) {
      x.addEventListener(t, function (e) { log.push((t === 'readystatechange' ? 'rsc' : t) + x.readyState); });
    });
  }
  function show(s) { document.getElementById('out').textContent = s; }
`

// renderXHR renders a page whose script is body against srv (same-origin) and
// returns the serialized document plus how long the render took.
func renderXHR(t *testing.T, srv *httptest.Server, body string) (string, time.Duration) {
	t.Helper()
	page := `<html><body><div id="out">unset</div><script>` + xhrLogJS + body + `</script></body></html>`
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse(srv.URL + "/page")
	eng := js.New(js.WithTimeout(3 * time.Second))
	start := time.Now()
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: httpTransport{client: srv.Client()}}); err != nil {
		t.Fatalf("render: %v", err)
	}
	took := time.Since(start)
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String(), took
}

func wantOut(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, `<div id="out">`+want+`</div>`) {
		t.Errorf("want #out = %q, got:\n%s", want, out)
	}
}

// TestXHRReadyStateSequence proves a successful request walks
// OPENED -> HEADERS_RECEIVED -> LOADING -> DONE with readystatechange at each
// step, in the standard's order relative to loadstart/progress/load/loadend, and
// that headers are readable at 2 and the body at 3.
func TestXHRReadyStateSequence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "BODY")
	}))
	defer srv.Close()
	out, _ := renderXHR(t, srv, `
	  var x = new XMLHttpRequest(), seen = [];
	  watch(x);
	  x.onreadystatechange = function () {
	    seen.push(x.readyState + ':' + x.status + ':' + (x.getResponseHeader('content-type') || '-') + ':' + x.responseText);
	  };
	  x.addEventListener('loadend', function (e) {
	    show(log.join(' ') + ' | ' + seen.join(' ') + ' | ' + e.loaded + '/' + e.total +
	      ' | ' + [XMLHttpRequest.UNSENT, XMLHttpRequest.OPENED, XMLHttpRequest.HEADERS_RECEIVED, XMLHttpRequest.LOADING, x.DONE].join(''));
	  });
	  x.open('GET', '/data'); x.send();
	`)
	wantOut(t, out, "rsc1 loadstart1 rsc2 rsc3 progress3 rsc4 load4 loadend4"+
		" | 1:0:-: 2:200:text/plain: 3:200:text/plain:BODY 4:200:text/plain:BODY"+
		" | 4/4 | 01234")
}

// TestXHRStatusText proves statusText is the response's reason phrase, not a
// hard-coded 'OK', for both XHR and fetch — and that a 404 is a load, not an error.
func TestXHRStatusText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "fine")
	}))
	defer srv.Close()
	out, _ := renderXHR(t, srv, `
	  var x = new XMLHttpRequest();
	  watch(x);
	  x.addEventListener('loadend', function () {
	    var first = x.status + ' [' + x.statusText + ']';
	    fetch('/missing').then(function (r) {
	      var y = new XMLHttpRequest();
	      y.onload = function () {
	        show(first + ' ' + log.slice(-2).join(' ') + ' | fetch ' + r.status + ' [' + r.statusText + '] | ' + y.status + ' [' + y.statusText + ']');
	      };
	      y.open('GET', '/ok'); y.send();
	    });
	  });
	  x.open('GET', '/missing'); x.send();
	`)
	wantOut(t, out, "404 [Not Found] load4 loadend4 | fetch 404 [Not Found] | 200 [OK]")
}

// TestXHRStatusTextEmptyWithoutReasonPhrase proves nothing is synthesized when
// the transport supplies no reason phrase (the HTTP/2 case).
func TestXHRStatusTextEmptyWithoutReasonPhrase(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/api": "x"}}
	out := renderWith(t, `<html><body><div id="out">unset</div><script>
	  var x = new XMLHttpRequest();
	  x.onload = function () { document.getElementById('out').textContent = x.status + ' [' + x.statusText + ']'; };
	  x.open('GET', '/api'); x.send();
	</script></body></html>`, tr)
	wantOut(t, out, "200 []")
}

// TestXHRAbortBeforeResponse proves abort() on an in-flight request fires
// readystatechange(DONE, empty) -> abort -> loadend, never load, resets the
// object to UNSENT, and cancels the underlying request rather than letting it
// run on and deliver later.
func TestXHRAbortBeforeResponse(t *testing.T) {
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(2 * time.Second):
			_, _ = io.WriteString(w, "TOO-LATE")
		}
	}))
	defer srv.Close()
	out, took := renderXHR(t, srv, `
	  var x = new XMLHttpRequest(), atDone = '';
	  watch(x);
	  x.onreadystatechange = function () { if (x.readyState === 4) atDone = x.status + '[' + x.responseText + ']'; };
	  x.open('GET', '/slow'); x.send();
	  setTimeout(function () {
	    x.abort();
	    var after = x.readyState + ':' + x.status;
	    // Leave time for a (wrongly) surviving response to land before reporting.
	    setTimeout(function () { show(log.join(' ') + ' | ' + atDone + ' | ' + after + ' | ' + x.responseText.length); }, 40);
	  }, 10);
	`)
	wantOut(t, out, "rsc1 loadstart1 rsc4 abort4 loadend4 | 0[] | 0:0 | 0")
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Error("abort() did not cancel the underlying request")
	}
	if took > time.Second {
		t.Errorf("render took %v: the aborted request held the settle open", took)
	}
}

// TestXHRAbortInsideHandler proves an abort() issued from a readystatechange
// handler mid-delivery stops the remaining steps: no LOADING, no load.
func TestXHRAbortInsideHandler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "BODY")
	}))
	defer srv.Close()
	out, _ := renderXHR(t, srv, `
	  var x = new XMLHttpRequest();
	  watch(x);
	  x.onreadystatechange = function () { if (x.readyState === 2) x.abort(); };
	  x.onloadend = function () { setTimeout(function () { show(log.join(' ') + ' | ' + x.readyState + ':' + x.status + '[' + x.responseText + ']'); }, 20); };
	  x.open('GET', '/data'); x.send();
	`)
	// The handler runs before the listener, so the listener logs the first
	// readystatechange only after the abort has reset the object to UNSENT.
	wantOut(t, out, "rsc1 loadstart1 rsc4 abort4 loadend4 rsc0 | 0:0[]")
}

// TestXHRTimeout proves timeout/ontimeout fire for a response slower than
// xhr.timeout, followed by loadend and never load, that the request is cut at
// the deadline (the render does not wait out the slow handler), and that a
// request finishing inside its timeout is unaffected.
func TestXHRTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(1500 * time.Millisecond):
			}
		}
		_, _ = io.WriteString(w, "DATA")
	}))
	defer srv.Close()
	out, took := renderXHR(t, srv, `
	  var x = new XMLHttpRequest(), viaProp = 0, t0 = Date.now();
	  watch(x);
	  x.timeout = 40;
	  x.ontimeout = function () { viaProp++; };
	  x.addEventListener('loadend', function () {
	    var slow = log.join(' ') + ' | ' + viaProp + ' | ' + x.status + '[' + x.responseText + '] | ' + (Date.now() - t0 >= 35);
	    var y = new XMLHttpRequest();
	    y.timeout = 2000;
	    y.ontimeout = function () { show('fast request timed out'); };
	    y.onload = function () { show(slow + ' | ' + y.status + '[' + y.responseText + ']'); };
	    y.open('GET', '/fast'); y.send();
	  });
	  x.open('GET', '/slow'); x.send();
	`)
	wantOut(t, out, "rsc1 loadstart1 rsc4 timeout4 loadend4 | 1 | 0[] | true | 200[DATA]")
	if took > time.Second {
		t.Errorf("render took %v: the timed-out request was not cut at its deadline", took)
	}
}

// TestXHROverrideMimeType proves overrideMimeType changes how the body is
// interpreted: an XML override makes responseXML a document for a text/plain
// response, and the x-user-defined charset exposes raw bytes through
// responseText (the "& 0xff" binary idiom). It is rejected once LOADING.
func TestXHROverrideMimeType(t *testing.T) {
	raw := make([]byte, 256)
	for i := range raw {
		raw[i] = byte(i)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bin" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(raw)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "<root><item>hi</item></root>")
	}))
	defer srv.Close()
	out, _ := renderXHR(t, srv, `
	  var res = [];
	  function get(path, mime, type, done) {
	    var x = new XMLHttpRequest();
	    x.open('GET', path);
	    if (mime) x.overrideMimeType(mime);
	    if (type) x.responseType = type;
	    x.onload = function () { done(x); };
	    x.send();
	  }
	  get('/xml', null, '', function (a) {
	    res.push('plain=' + (a.responseXML === null));
	    get('/xml', 'text/xml', '', function (b) {
	      var item = b.responseXML && b.responseXML.querySelector('item');
	      res.push('xml=' + (item ? item.textContent : 'none'));
	      var threw = 'no';
	      try { b.overrideMimeType('text/plain'); } catch (e) { threw = e.name; }
	      res.push('late=' + threw);
	      get('/bin', 'text/plain; charset=x-user-defined', '', function (c) {
	        var t = c.responseText, ok = t.length === 256;
	        for (var i = 0; i < t.length; i++) if ((t.charCodeAt(i) & 0xff) !== i) ok = false;
	        res.push('bytes=' + ok);
	        get('/bin', 'image/png', 'blob', function (d) {
	          res.push('blob=' + d.response.type);
	          show(res.join(' '));
	        });
	      });
	    });
	  });
	`)
	wantOut(t, out, "plain=true xml=hi late=InvalidStateError bytes=true blob=image/png")
}

// TestXHRResponseHeaders proves the header getters hide Set-Cookie (a forbidden
// response-header name: it would expose HttpOnly cookies), return null before a
// response, and list names sorted.
func TestXHRResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "sid=secret; HttpOnly")
		w.Header().Set("X-B", "2")
		w.Header().Set("X-A", "1")
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	out, _ := renderXHR(t, srv, `
	  var x = new XMLHttpRequest();
	  x.open('GET', '/h');
	  var before = x.getResponseHeader('x-a') + '/' + JSON.stringify(x.getAllResponseHeaders());
	  x.onload = function () {
	    var all = x.getAllResponseHeaders();
	    show(before + ' | ' + x.getResponseHeader('Set-Cookie') + ' | ' + (all.indexOf('secret') < 0) +
	      ' | ' + (all.indexOf('x-a: 1\r\n') >= 0 && all.indexOf('x-a') < all.indexOf('x-b')) + ' | ' + x.getResponseHeader('X-B'));
	  };
	  x.send();
	`)
	wantOut(t, out, `null/&#34;&#34; | null | true | true | 2`)
}

// TestXHRSendRequiresOpened proves send() throws InvalidStateError before
// open() and while a request is already in flight, instead of issuing a request.
func TestXHRSendRequiresOpened(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	out, _ := renderXHR(t, srv, `
	  var x = new XMLHttpRequest(), a = 'no', b = 'no';
	  try { x.send(); } catch (e) { a = e.name; }
	  x.open('GET', '/ok'); x.send();
	  try { x.send(); } catch (e) { b = e.name; }
	  x.onload = function () { show(a + ' ' + b + ' ' + x.responseText); };
	`)
	wantOut(t, out, "InvalidStateError InvalidStateError ok")
}
