package js_test

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// corsReq is one request the CORS layer issued through the transport.
type corsReq struct {
	method, url, origin, acrm, acrh string
	omit                            bool // js.WithOmitCredentials was set (cookies to be stripped)
}

// corsRecorder records every request (so tests can assert preflights,
// origins, and cookie-omit signaling — the "history preservation" invariant) and
// serves responses from a per-test responder.
type corsRecorder struct {
	mu   sync.Mutex
	reqs []corsReq
	resp func(method, url string) *js.Response
}

func (r *corsRecorder) Do(ctx context.Context, method, url string, headers map[string]string, _ []byte) (*js.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, corsReq{
		method: method, url: url,
		origin: headers["Origin"],
		acrm:   headers["Access-Control-Request-Method"],
		acrh:   headers["Access-Control-Request-Headers"],
		omit:   js.OmitCredentials(ctx),
	})
	r.mu.Unlock()
	if res := r.resp(method, url); res != nil {
		return res, nil
	}
	return &js.Response{Status: 200, Body: []byte("BODY"), FinalURL: url}, nil
}

func (r *corsRecorder) methodCount(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, q := range r.reqs {
		if q.method == method {
			n++
		}
	}
	return n
}

func (r *corsRecorder) last() corsReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}

func jsResp(status int, headers map[string]string) *js.Response {
	return &js.Response{Status: status, Headers: headers, Body: []byte("BODY"), FinalURL: ""}
}

// fetchScript records a single fetch's observed outcome ("ok:<type>:<status>" or
// "err") into #out, reflecting what the page's own code can see.
const fetchScript = `.then(function(r){document.getElementById('out').textContent='ok:'+r.type+':'+r.status;})` +
	`.catch(function(){document.getElementById('out').textContent='err';});`

func renderCORS(t *testing.T, fetchExpr, baseURL string, env js.Env, tr js.Transport) *html.Node {
	t.Helper()
	page := `<html><body><div id="out">pending</div><script>` + fetchExpr + fetchScript + `</script></body></html>`
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse(baseURL)
	env.Transport = tr
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, env); err != nil {
		t.Fatalf("render: %v", err)
	}
	return doc
}

func TestCORSSameOriginBasic(t *testing.T) {
	tr := &corsRecorder{resp: func(_, _ string) *js.Response { return jsResp(200, nil) }}
	doc := renderCORS(t, `fetch('/api')`, "https://page.com/here", js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "ok:basic:200" {
		t.Errorf("#out=%q want ok:basic:200 (same-origin skips CORS)", got)
	}
	if o := tr.last().origin; o != "" {
		t.Errorf("same-origin request carried Origin=%q, want none", o)
	}
}

func TestCORSCrossOriginAllowed(t *testing.T) {
	tr := &corsRecorder{resp: func(_, _ string) *js.Response {
		return jsResp(200, map[string]string{"access-control-allow-origin": "*"})
	}}
	doc := renderCORS(t, `fetch('https://api.other.com/x')`, "https://page.com/here", js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "ok:cors:200" {
		t.Errorf("#out=%q want ok:cors:200", got)
	}
	if o := tr.last().origin; o != "https://page.com" {
		t.Errorf("cross-origin request Origin=%q want https://page.com", o)
	}
}

func TestCORSCrossOriginBlockedButLogged(t *testing.T) {
	tr := &corsRecorder{resp: func(_, _ string) *js.Response { return jsResp(200, nil) }} // no ACAO
	diag := &js.RenderResult{}
	doc := renderCORS(t, `fetch('https://api.other.com/x')`, "https://page.com/here", js.Env{Diag: diag}, tr)
	if got := divText(t, doc, "out"); got != "err" {
		t.Errorf("#out=%q want err (missing ACAO blocks the read)", got)
	}
	// History preservation: the request was still SENT (the agent can see it).
	if n := tr.methodCount("GET"); n != 1 {
		t.Errorf("blocked cross-origin request reached transport %d times, want 1 (still logged)", n)
	}
	// ...and it appears in the requests-tool diagnostics, though the page's read was denied.
	found := false
	for _, r := range diag.Requests {
		if strings.Contains(r.URL, "api.other.com/x") {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked cross-origin request missing from the requests log: %+v", diag.Requests)
	}
}

func TestCORSPreflight(t *testing.T) {
	tr := &corsRecorder{resp: func(method, _ string) *js.Response {
		if method == "OPTIONS" {
			return jsResp(204, map[string]string{
				"access-control-allow-origin":  "https://page.com",
				"access-control-allow-methods": "GET",
				"access-control-allow-headers": "x-custom",
			})
		}
		return jsResp(200, map[string]string{"access-control-allow-origin": "https://page.com"})
	}}
	doc := renderCORS(t, `fetch('https://api.other.com/x',{headers:{'X-Custom':'1'}})`, "https://page.com/here", js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "ok:cors:200" {
		t.Errorf("#out=%q want ok:cors:200", got)
	}
	if n := tr.methodCount("OPTIONS"); n != 1 {
		t.Errorf("preflight OPTIONS count=%d want 1", n)
	}
	if n := tr.methodCount("GET"); n != 1 {
		t.Errorf("actual GET count=%d want 1", n)
	}
}

func TestCORSPreflightDeniedSkipsRealRequest(t *testing.T) {
	tr := &corsRecorder{resp: func(method, _ string) *js.Response {
		if method == "OPTIONS" {
			return jsResp(204, map[string]string{"access-control-allow-origin": "https://page.com"}) // no Allow-Headers
		}
		return jsResp(200, map[string]string{"access-control-allow-origin": "https://page.com"})
	}}
	doc := renderCORS(t, `fetch('https://api.other.com/x',{headers:{'X-Custom':'1'}})`, "https://page.com/here", js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "err" {
		t.Errorf("#out=%q want err (preflight denied)", got)
	}
	if n := tr.methodCount("GET"); n != 0 {
		t.Errorf("real GET sent %d times after a denied preflight, want 0", n)
	}
}

func TestCORSNoCorsOpaque(t *testing.T) {
	tr := &corsRecorder{resp: func(_, _ string) *js.Response { return jsResp(200, nil) }}
	doc := renderCORS(t, `fetch('https://api.other.com/beacon',{mode:'no-cors'})`, "https://page.com/here", js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "ok:opaque:0" {
		t.Errorf("#out=%q want ok:opaque:0 (no-cors is opaque, unreadable)", got)
	}
	if n := tr.methodCount("GET"); n != 1 {
		t.Errorf("no-cors request reached transport %d times, want 1 (beacon must still fire + log)", n)
	}
}

func TestCORSSameOriginModeBlocksWithoutSending(t *testing.T) {
	tr := &corsRecorder{resp: func(_, _ string) *js.Response { return jsResp(200, nil) }}
	doc := renderCORS(t, `fetch('https://api.other.com/x',{mode:'same-origin'})`, "https://page.com/here", js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "err" {
		t.Errorf("#out=%q want err (same-origin mode forbids cross-origin)", got)
	}
	if n := len(tr.reqs); n != 0 {
		t.Errorf("same-origin-mode cross-origin request was sent %d times, want 0", n)
	}
}

func TestCORSCredentials(t *testing.T) {
	// Default credentials ('same-origin') → cookies omitted cross-origin.
	t.Run("default omits cookies", func(t *testing.T) {
		tr := &corsRecorder{resp: func(_, _ string) *js.Response {
			return jsResp(200, map[string]string{"access-control-allow-origin": "*"})
		}}
		renderCORS(t, `fetch('https://api.other.com/x')`, "https://page.com/here", js.Env{}, tr)
		if !tr.last().omit {
			t.Error("non-credentialed cross-origin request did not signal cookie omission")
		}
	})
	// credentials:'include' with an echoed ACAO + ACA-Credentials → cookies sent, read allowed.
	t.Run("include with echo+flag allowed", func(t *testing.T) {
		tr := &corsRecorder{resp: func(_, _ string) *js.Response {
			return jsResp(200, map[string]string{
				"access-control-allow-origin":      "https://page.com",
				"access-control-allow-credentials": "true",
			})
		}}
		doc := renderCORS(t, `fetch('https://api.other.com/x',{credentials:'include'})`, "https://page.com/here", js.Env{}, tr)
		if got := divText(t, doc, "out"); got != "ok:cors:200" {
			t.Errorf("#out=%q want ok:cors:200", got)
		}
		if tr.last().omit {
			t.Error("credentialed request incorrectly signaled cookie omission")
		}
	})
	// credentials:'include' with wildcard ACAO → rejected (wildcard forbidden when credentialed).
	t.Run("include with wildcard rejected", func(t *testing.T) {
		tr := &corsRecorder{resp: func(_, _ string) *js.Response {
			return jsResp(200, map[string]string{
				"access-control-allow-origin":      "*",
				"access-control-allow-credentials": "true",
			})
		}}
		doc := renderCORS(t, `fetch('https://api.other.com/x',{credentials:'include'})`, "https://page.com/here", js.Env{}, tr)
		if got := divText(t, doc, "out"); got != "err" {
			t.Errorf("#out=%q want err (wildcard ACAO forbidden for credentialed)", got)
		}
	})
}

func TestCORSAllowCrossOriginBypass(t *testing.T) {
	tr := &corsRecorder{resp: func(_, _ string) *js.Response { return jsResp(200, nil) }} // no ACAO
	doc := renderCORS(t, `fetch('https://api.other.com/x')`, "https://page.com/here", js.Env{AllowCrossOrigin: true}, tr)
	if got := divText(t, doc, "out"); got != "ok:basic:200" {
		t.Errorf("#out=%q want ok:basic:200 (enforcement disabled)", got)
	}
}
