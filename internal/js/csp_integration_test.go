package js_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// cspRecordTransport serves per-URL-substring JS bodies (ACAO:* so cross-origin
// script loads aren't CORS-noise) and records every requested URL, so tests can
// assert a CSP-blocked external script was never fetched.
type cspRecordTransport struct {
	mu     sync.Mutex
	seen   []string
	bodies map[string]string
}

func (c *cspRecordTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	c.mu.Lock()
	c.seen = append(c.seen, url)
	c.mu.Unlock()
	body := ""
	for k, v := range c.bodies {
		if strings.Contains(url, k) {
			body = v
		}
	}
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "application/javascript", "access-control-allow-origin": "*"}, Body: []byte(body), FinalURL: url}, nil
}

func (c *cspRecordTransport) requested(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.seen {
		if strings.Contains(u, sub) {
			return true
		}
	}
	return false
}

func renderCSP(t *testing.T, pageHTML, baseURL string, headers http.Header, tr js.Transport) (*html.Node, *js.RenderResult) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse(baseURL)
	diag := &js.RenderResult{}
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, js.Env{ResponseHeaders: headers, Transport: tr, Diag: diag}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return doc, diag
}

func cspHeader(values ...string) http.Header {
	h := http.Header{}
	for _, v := range values {
		h.Add("Content-Security-Policy", v)
	}
	return h
}

func cspReportOnly(v string) http.Header {
	h := http.Header{}
	h.Set("Content-Security-Policy-Report-Only", v)
	return h
}

func hasCSPError(d *js.RenderResult) bool {
	for _, e := range d.Errors {
		if strings.Contains(e, "Content-Security-Policy") {
			return true
		}
	}
	return false
}

func TestCSPEvalGate(t *testing.T) {
	page := `<html><body><div id="out">pending</div><script>
		var r = [];
		try { eval('1+1'); r.push('eval-ran'); } catch (e) { r.push('eval:' + e.name); }
		try { (new Function('return 1'))(); r.push('fn-ran'); } catch (e) { r.push('fn:' + e.name); }
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'unsafe-inline'"), nil)
	if got := divText(t, doc, "out"); got != "eval:EvalError,fn:EvalError" {
		t.Errorf("#out = %q, want eval:EvalError,fn:EvalError", got)
	}
}

func TestCSPEvalAllowedWithUnsafeEval(t *testing.T) {
	page := `<html><body><div id="out">pending</div><script>
		try { document.getElementById('out').textContent = 'eval=' + eval('40+2'); }
		catch (e) { document.getElementById('out').textContent = 'blocked'; }
	</script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'unsafe-inline' 'unsafe-eval'"), nil)
	if got := divText(t, doc, "out"); got != "eval=42" {
		t.Errorf("#out = %q, want eval=42 (unsafe-eval present)", got)
	}
}

func TestCSPInlineBlockedWithoutNonce(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script>document.getElementById('out').textContent='ran';</script></body></html>`
	doc, diag := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'self'"), nil)
	if got := divText(t, doc, "out"); got != "pending" {
		t.Errorf("#out = %q, want pending (inline blocked by script-src 'self')", got)
	}
	if !hasCSPError(diag) {
		t.Errorf("expected a CSP diagnostic, got %v", diag.Errors)
	}
}

func TestCSPInlineAllowedWithNonce(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script nonce="r4nd0m">document.getElementById('out').textContent='ran';</script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-r4nd0m'"), nil)
	if got := divText(t, doc, "out"); got != "ran" {
		t.Errorf("#out = %q, want ran (matching nonce)", got)
	}
}

func TestCSPInlineAllowedWithHash(t *testing.T) {
	body := `document.getElementById('out').textContent='hash-ran';`
	sum := sha256.Sum256([]byte(body))
	h := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	page := `<html><body><div id="out">pending</div><script>` + body + `</script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src '"+h+"'"), nil)
	if got := divText(t, doc, "out"); got != "hash-ran" {
		t.Errorf("#out = %q, want hash-ran (matching hash)", got)
	}
}

func TestCSPExternalScriptBlockedByHost(t *testing.T) {
	tr := &cspRecordTransport{bodies: map[string]string{"evil.com": `document.getElementById('out').textContent='EVIL';`}}
	page := `<html><body><div id="out">pending</div>` +
		`<script src="https://cdn.evil.com/x.js"></script></body></html>`
	doc, diag := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'self'"), tr)
	if got := divText(t, doc, "out"); got != "pending" {
		t.Errorf("#out = %q, want pending (cross-origin script not allowed by 'self')", got)
	}
	if tr.requested("evil.com") {
		t.Error("CSP-blocked external script was fetched (a browser blocks before the load)")
	}
	if !hasCSPError(diag) {
		t.Errorf("expected a CSP diagnostic, got %v", diag.Errors)
	}
}

func TestCSPExternalScriptAllowedByHost(t *testing.T) {
	tr := &cspRecordTransport{bodies: map[string]string{"cdn.example.com": `document.getElementById('out').textContent='EXT';`}}
	page := `<html><body><div id="out">pending</div>` +
		`<script src="https://cdn.example.com/x.js"></script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src https://cdn.example.com"), tr)
	if got := divText(t, doc, "out"); got != "EXT" {
		t.Errorf("#out = %q, want EXT (host allowlisted)", got)
	}
}

func TestCSPStrictDynamicAllowsInsertedChunk(t *testing.T) {
	tr := &cspRecordTransport{bodies: map[string]string{"chunk.js": `document.getElementById('out').textContent='CHUNK';`}}
	// A nonce'd inline script inserts a script from a non-allowlisted host; under
	// strict-dynamic the script-inserted chunk is trusted and runs.
	page := `<html><body><div id="out">pending</div>
		<script nonce="n1">
		  var s = document.createElement('script');
		  s.src = 'https://cdn.other.com/chunk.js';
		  document.body.appendChild(s);
		</script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-n1' 'strict-dynamic'"), tr)
	if got := divText(t, doc, "out"); got != "CHUNK" {
		t.Errorf("#out = %q, want CHUNK (strict-dynamic trusts the inserted chunk)", got)
	}
}

func TestCSPConnectSrcBlocksFetch(t *testing.T) {
	tr := &cspRecordTransport{}
	page := `<html><body><div id="out">pending</div><script>
		fetch('https://api.other.com/x')
		  .then(function(){ document.getElementById('out').textContent = 'fetched'; })
		  .catch(function(){ document.getElementById('out').textContent = 'blocked'; });
	</script></body></html>`
	// Only connect-src is set → script-src unrestricted, so the inline script runs.
	doc, diag := renderCSP(t, page, "https://example.com/p", cspHeader("connect-src 'self'"), tr)
	if got := divText(t, doc, "out"); got != "blocked" {
		t.Errorf("#out = %q, want blocked (connect-src 'self' forbids cross-origin fetch)", got)
	}
	if tr.requested("api.other.com") {
		t.Error("CSP-blocked fetch was still dispatched")
	}
	if !hasCSPError(diag) {
		t.Errorf("expected a CSP diagnostic, got %v", diag.Errors)
	}
}

func TestCSPReportOnlyNeverBlocks(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script>document.getElementById('out').textContent='ran';</script></body></html>`
	doc, diag := renderCSP(t, page, "https://example.com/p", cspReportOnly("script-src 'none'"), nil)
	if got := divText(t, doc, "out"); got != "ran" {
		t.Errorf("#out = %q, want ran (report-only must not block)", got)
	}
	if !hasCSPError(diag) {
		t.Errorf("expected a report-only CSP diagnostic, got %v", diag.Errors)
	}
}

func TestCSPMultiplePoliciesAllEnforced(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script nonce="n1">document.getElementById('out').textContent='ran';</script></body></html>`
	// Policy 1 allows the nonce; policy 2 ('self', no nonce) blocks it → blocked.
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-n1'", "script-src 'self'"), nil)
	if got := divText(t, doc, "out"); got != "pending" {
		t.Errorf("#out = %q, want pending (a second policy blocks it)", got)
	}
}

func TestCSPFromMetaTag(t *testing.T) {
	page := `<html><head><meta http-equiv="Content-Security-Policy" content="script-src 'none'"></head>` +
		`<body><div id="out">pending</div>` +
		`<script>document.getElementById('out').textContent='ran';</script></body></html>`
	doc, diag := renderCSP(t, page, "https://example.com/p", nil, nil)
	if got := divText(t, doc, "out"); got != "pending" {
		t.Errorf("#out = %q, want pending (meta CSP script-src 'none' blocks inline)", got)
	}
	if !hasCSPError(diag) {
		t.Errorf("expected a CSP diagnostic from meta CSP, got %v", diag.Errors)
	}
}

// TestCSPNonceHiddenFromPageJS proves nonce hiding: a nonce'd inline script still
// runs (enforcement reads the captured value) while the nonce is blanked in the DOM
// the page's own JS — and the agent — sees, exactly as a browser hides it.
func TestCSPNonceHiddenFromPageJS(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script id="s" nonce="secret123">document.getElementById('out').textContent = 'nonce=[' + document.getElementById('s').getAttribute('nonce') + ']';</script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-secret123'"), nil)
	if got := divText(t, doc, "out"); got != "nonce=[]" {
		t.Errorf("#out = %q, want nonce=[] (script ran via the captured nonce; DOM nonce blanked)", got)
	}
}

// TestCSPNonceNotExfiltratable proves untrusted page JS cannot recover ANY nonce
// from the DOM — closing the DOM-scrape / CSS-attribute-selector reuse channel that
// would otherwise let a page steal a nonce and slip a script past script-src.
func TestCSPNonceNotExfiltratable(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script nonce="secret">var els=document.querySelectorAll('[nonce]');var leaked=0;` +
		`for(var i=0;i<els.length;i++){if(els[i].getAttribute('nonce'))leaked++;}` +
		`document.getElementById('out').textContent='leaked='+leaked+',bySel='+document.querySelectorAll('[nonce="secret"]').length;</script></body></html>`
	doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-secret'"), nil)
	if got := divText(t, doc, "out"); got != "leaked=0,bySel=0" {
		t.Errorf("#out = %q, want leaked=0,bySel=0 (no nonce readable via getAttribute or a [nonce=…] selector)", got)
	}
}

// TestCSPInlineBlockedWithWrongNonce locks the direct nonce-mismatch block (it was
// previously exercised only indirectly through the multi-policy test).
func TestCSPInlineBlockedWithWrongNonce(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script nonce="wrong">document.getElementById('out').textContent='ran';</script></body></html>`
	doc, diag := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-right'"), nil)
	if got := divText(t, doc, "out"); got != "pending" {
		t.Errorf("#out = %q, want pending (nonce mismatch blocks the inline script)", got)
	}
	if !hasCSPError(diag) {
		t.Errorf("expected a CSP diagnostic, got %v", diag.Errors)
	}
}

// TestCSPExternalScriptByNonce covers a plain (non-strict-dynamic) external script
// allowed by a matching nonce, and blocked-and-never-fetched by a wrong one.
func TestCSPExternalScriptByNonce(t *testing.T) {
	t.Run("matching nonce allows + loads", func(t *testing.T) {
		tr := &cspRecordTransport{bodies: map[string]string{"x.js": `document.getElementById('out').textContent='EXT';`}}
		page := `<html><body><div id="out">pending</div>` +
			`<script src="https://cdn.other.com/x.js" nonce="extnonce"></script></body></html>`
		doc, _ := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-extnonce'"), tr)
		if got := divText(t, doc, "out"); got != "EXT" {
			t.Errorf("#out = %q, want EXT (matching nonce allows the external script)", got)
		}
	})
	t.Run("wrong nonce blocks + never fetched", func(t *testing.T) {
		tr := &cspRecordTransport{bodies: map[string]string{"x.js": `document.getElementById('out').textContent='EXT';`}}
		page := `<html><body><div id="out">pending</div>` +
			`<script src="https://cdn.other.com/x.js" nonce="wrong"></script></body></html>`
		doc, diag := renderCSP(t, page, "https://example.com/p", cspHeader("script-src 'nonce-extnonce'"), tr)
		if got := divText(t, doc, "out"); got != "pending" {
			t.Errorf("#out = %q, want pending (nonce mismatch blocks the external script)", got)
		}
		if tr.requested("x.js") {
			t.Error("CSP-blocked external script was fetched (a browser blocks before the load)")
		}
		if !hasCSPError(diag) {
			t.Errorf("expected a CSP diagnostic, got %v", diag.Errors)
		}
	})
}

func TestCSPDisabledBypass(t *testing.T) {
	page := `<html><body><div id="out">pending</div>` +
		`<script>document.getElementById('out').textContent='ran';</script></body></html>`
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/p")
	eng := js.New(js.WithTimeout(3 * time.Second))
	env := js.Env{ResponseHeaders: cspHeader("script-src 'none'"), DisableCSP: true}
	if err := eng.Render(context.Background(), doc, base, env); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := divText(t, doc, "out"); got != "ran" {
		t.Errorf("#out = %q, want ran (DisableCSP bypasses enforcement)", got)
	}
}
