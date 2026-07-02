package js_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

// recordingTransport captures the last request so tests can assert what the
// prelude's fetch/XHR actually put on the wire (headers + serialized body).
type recordingTransport struct {
	mu      sync.Mutex
	method  string
	url     string
	headers map[string]string
	body    []byte
	reply   js.Response
}

func (r *recordingTransport) Do(_ context.Context, method, url string, headers map[string]string, body []byte) (*js.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.method, r.url, r.headers = method, url, headers
	r.body = append([]byte(nil), body...)
	resp := r.reply
	if resp.Status == 0 {
		resp = js.Response{Status: 200, Body: []byte("OK"), FinalURL: url}
	}
	return &resp, nil
}

func (r *recordingTransport) last() (string, map[string]string, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.method, r.headers, r.body
}

// TestHeadersBlobFormDataClasses proves the fetch-ecosystem classes are
// constructible and behave: case-insensitive Headers, Blob slicing/typing,
// File inheritance, and FormData's ordered multi-value pair list.
func TestHeadersBlobFormDataClasses(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var h = new Headers({ 'Content-Type': 'text/plain' });
		  h.append('X-A', '1'); h.append('x-a', '2');
		  var hOK = h.get('content-type') === 'text/plain' && h.get('X-A') === '1, 2' && h.has('x-a');
		  h['delete']('x-a');
		  hOK = hOK && !h.has('x-a');

		  var b = new Blob(['hello ', 'world'], { type: 'Text/Plain' });
		  var bOK = b.size === 11 && b.type === 'text/plain' && b.slice(0, 5).size === 5;

		  var f = new File(['data'], 'a.txt', { type: 'text/plain' });
		  var fOK = f instanceof Blob && f.name === 'a.txt' && f.size === 4;

		  var fd = new FormData();
		  fd.append('k', 'v1'); fd.append('k', 'v2'); fd.set('solo', 'x');
		  var fdOK = fd.get('k') === 'v1' && fd.getAll('k').length === 2 && fd.has('solo') && !fd.has('gone');
		  var order = [];
		  fd.forEach(function (v, k) { order.push(k); });
		  fdOK = fdOK && order.join(',') === 'k,k,solo';

		  b.text().then(function (txt) {
		    document.getElementById('out').textContent =
		      'h=' + hOK + ' b=' + bOK + ' f=' + fOK + ' fd=' + fdOK + ' text=' + (txt === 'hello world');
		  });
		</script></body></html>`)
	for _, want := range []string{"h=true", "b=true", "f=true", "fd=true", "text=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("fetch classes missing %q:\n%s", want, out)
		}
	}
}

// TestFetchReturnsRealResponse proves fetch resolves to a constructible-class
// Response carrying instanceof, real Headers, and the binary body readers.
func TestFetchReturnsRealResponse(t *testing.T) {
	tr := &recordingTransport{reply: js.Response{
		Status:   200,
		Headers:  map[string]string{"Content-Type": "application/json"},
		Body:     []byte(`{"n": 7}`),
		FinalURL: "https://example.com/api",
	}}
	out := renderWith(t, `<html><body><div id="out"></div>
		<script>
		  fetch('/api').then(function (r) {
		    var cls = (r instanceof Response) && (r.headers instanceof Headers);
		    var meta = r.ok && r.status === 200 && r.headers.get('content-type') === 'application/json';
		    return r.clone().arrayBuffer().then(function (ab) {
		      return r.json().then(function (j) {
		        document.getElementById('out').textContent =
		          'cls=' + cls + ' meta=' + meta + ' ab=' + ab.byteLength + ' n=' + j.n + ' redir=' + r.redirected;
		      });
		    });
		  });
		</script></body></html>`, tr)
	for _, want := range []string{"cls=true", "meta=true", "ab=8", "n=7", "redir=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("fetch Response missing %q:\n%s", want, out)
		}
	}
}

// TestFetchRequestObjectAndStatics proves fetch(new Request(...)) works and the
// Response statics construct sensible instances.
func TestFetchRequestObjectAndStatics(t *testing.T) {
	tr := &recordingTransport{}
	out := renderWith(t, `<html><body><div id="out"></div>
		<script>
		  var req = new Request('/things', { method: 'POST', headers: { 'X-Req': 'yes' }, body: 'payload' });
		  var clone = req.clone();
		  var reqOK = clone.method === 'POST' && clone.headers.get('x-req') === 'yes' && clone.url === '/things';
		  var rj = Response.json({ a: 1 });
		  var re = Response.error();
		  fetch(req).then(function (r) { return r.text(); }).then(function (txt) {
		    rj.json().then(function (j) {
		      document.getElementById('out').textContent =
		        'req=' + reqOK + ' sent=' + (txt === 'OK') + ' rj=' + (j.a === 1 && rj.headers.get('content-type') === 'application/json') +
		        ' re=' + (re.type === 'error' && !re.ok);
		    });
		  });
		</script></body></html>`, tr)
	for _, want := range []string{"req=true", "sent=true", "rj=true", "re=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("Request/statics missing %q:\n%s", want, out)
		}
	}
	method, headers, body := tr.last()
	if method != "POST" || headers["x-req"] != "yes" || string(body) != "payload" {
		t.Errorf("wire request wrong: method=%q headers=%v body=%q", method, headers, body)
	}
}

// TestFetchFormDataMultipart asserts the JS-side multipart serialization on the
// wire: boundary declared in Content-Type, text field, and file part with
// filename + content type + binary-faithful bytes.
func TestFetchFormDataMultipart(t *testing.T) {
	tr := &recordingTransport{}
	out := renderWith(t, `<html><body><div id="out"></div>
		<script>
		  var fd = new FormData();
		  fd.append('field', 'value-é');
		  fd.append('upload', new File(['file-bytes'], 'notes.txt', { type: 'text/plain' }));
		  fetch('/submit', { method: 'POST', body: fd }).then(function (r) { return r.text(); }).then(function (txt) {
		    document.getElementById('out').textContent = 'posted=' + (txt === 'OK');
		  });
		</script></body></html>`, tr)
	if !strings.Contains(out, "posted=true") {
		t.Fatalf("multipart post did not complete:\n%s", out)
	}
	_, headers, body := tr.last()
	ct := headers["content-type"]
	if !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
		t.Fatalf("content-type = %q, want multipart with boundary", ct)
	}
	boundary := strings.TrimPrefix(ct, "multipart/form-data; boundary=")
	sbody := string(body)
	for _, want := range []string{
		"--" + boundary + "\r\n",
		`Content-Disposition: form-data; name="field"`,
		"value-é",
		`name="upload"; filename="notes.txt"`,
		"Content-Type: text/plain",
		"file-bytes",
		"--" + boundary + "--\r\n",
	} {
		if !strings.Contains(sbody, want) {
			t.Errorf("multipart body missing %q:\n%s", want, sbody)
		}
	}
}

// TestFetchURLSearchParamsBody proves the urlencoded default content type and
// serialization for the login-form-shaped fetch bodies SPAs send.
func TestFetchURLSearchParamsBody(t *testing.T) {
	tr := &recordingTransport{}
	out := renderWith(t, `<html><body><div id="out"></div>
		<script>
		  var p = new URLSearchParams();
		  p.append('user', 'a b'); p.append('pw', 'c&d');
		  fetch('/login', { method: 'POST', body: p }).then(function (r) { return r.text(); }).then(function (txt) {
		    document.getElementById('out').textContent = 'sent=' + (txt === 'OK');
		  });
		</script></body></html>`, tr)
	if !strings.Contains(out, "sent=true") {
		t.Fatalf("urlencoded post did not complete:\n%s", out)
	}
	_, headers, body := tr.last()
	if !strings.HasPrefix(headers["content-type"], "application/x-www-form-urlencoded") {
		t.Errorf("content-type = %q, want urlencoded", headers["content-type"])
	}
	if string(body) != "user=a+b&pw=c%26d" {
		t.Errorf("body = %q, want urlencoded pairs", string(body))
	}
}

// TestXHRBinaryResponseTypes proves responseType='arraybuffer'/'blob' read the
// raw bytes (bodyBytes) rather than the mangled string body.
func TestXHRBinaryResponseTypes(t *testing.T) {
	raw := make([]byte, 256)
	for i := range raw {
		raw[i] = byte(i)
	}
	tr := &recordingTransport{reply: js.Response{Status: 200, Body: raw, FinalURL: "https://example.com/bin"}}
	out := renderWith(t, `<html><body><div id="out"></div>
		<script>
		  var x = new XMLHttpRequest();
		  x.open('GET', '/bin');
		  x.responseType = 'arraybuffer';
		  x.onload = function () {
		    var u8 = new Uint8Array(x.response);
		    var exact = u8.length === 256 && u8[0] === 0 && u8[255] === 255 && u8[128] === 128;
		    var x2 = new XMLHttpRequest();
		    x2.open('GET', '/bin');
		    x2.responseType = 'blob';
		    x2.onload = function () {
		      document.getElementById('out').textContent =
		        'ab=' + exact + ' blob=' + (x2.response instanceof Blob && x2.response.size === 256);
		    };
		    x2.send();
		  };
		  x.send();
		</script></body></html>`, tr)
	for _, want := range []string{"ab=true", "blob=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("XHR binary missing %q:\n%s", want, out)
		}
	}
}
