package js_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// sriRoute is a canned subresource: body is what the engine executes, raw is what
// SRI must hash (the server's pre-transcode bytes). raw defaults to body.
type sriRoute struct {
	body []byte
	raw  []byte
}

type sriTransport struct {
	routes map[string]sriRoute // url substring → route
}

func (s *sriTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	for k, r := range s.routes {
		if strings.Contains(url, k) {
			raw := r.raw
			if raw == nil {
				raw = r.body
			}
			return &js.Response{
				Status:   200,
				Headers:  map[string]string{"content-type": "application/javascript"},
				Body:     r.body,
				Raw:      raw,
				FinalURL: url,
			}, nil
		}
	}
	return &js.Response{Status: 404, FinalURL: url}, nil
}

func integrityFor(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

func renderSRI(t *testing.T, pageHTML string, env js.Env, tr js.Transport) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://page.com/here")
	env.Transport = tr
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, env); err != nil {
		t.Fatalf("render: %v", err)
	}
	return doc
}

func TestSRIExternalScriptMatchRuns(t *testing.T) {
	body := []byte(`document.getElementById('out').textContent='RAN';`)
	tr := &sriTransport{routes: map[string]sriRoute{"app.js": {body: body}}}
	page := `<html><body><div id="out">pending</div>` +
		`<script src="https://cdn.example.com/app.js" integrity="` + integrityFor(body) + `"></script></body></html>`
	doc := renderSRI(t, page, js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "RAN" {
		t.Errorf("#out=%q want RAN (matching integrity should execute)", got)
	}
}

func TestSRIExternalScriptMismatchBlocked(t *testing.T) {
	body := []byte(`document.getElementById('out').textContent='RAN';`)
	tr := &sriTransport{routes: map[string]sriRoute{"app.js": {body: body}}}
	bad := integrityFor([]byte("something else entirely"))
	page := `<html><body><div id="out">pending</div>` +
		`<script src="https://cdn.example.com/app.js" integrity="` + bad + `"></script></body></html>`
	diag := &js.RenderResult{}
	doc := renderSRI(t, page, js.Env{Diag: diag}, tr)
	if got := divText(t, doc, "out"); got != "pending" {
		t.Errorf("#out=%q want pending (mismatched integrity must NOT execute)", got)
	}
	if !hasIntegrityError(diag) {
		t.Errorf("expected an integrity-mismatch diagnostic, got %v", diag.Errors)
	}
}

func TestSRIDisabledRunsAnyway(t *testing.T) {
	body := []byte(`document.getElementById('out').textContent='RAN';`)
	tr := &sriTransport{routes: map[string]sriRoute{"app.js": {body: body}}}
	page := `<html><body><div id="out">pending</div>` +
		`<script src="https://cdn.example.com/app.js" integrity="` + integrityFor([]byte("wrong")) + `"></script></body></html>`
	doc := renderSRI(t, page, js.Env{DisableSRI: true}, tr)
	if got := divText(t, doc, "out"); got != "RAN" {
		t.Errorf("#out=%q want RAN (--no-sri bypasses verification)", got)
	}
}

// TestSRIHashesRawNotTranscoded locks the transcoding fix: the integrity is the
// hash of the raw (server) bytes, which differ from the UTF-8 Body the engine
// executes. Hashing Body would false-mismatch and wrongly block a legitimate
// non-UTF-8 script.
func TestSRIHashesRawNotTranscoded(t *testing.T) {
	raw := []byte("/* \xe9 */ document.getElementById('out').textContent='RAN';") // latin-1 é (0xe9)
	body := []byte("/* é */ document.getElementById('out').textContent='RAN';")
	tr := &sriTransport{routes: map[string]sriRoute{"app.js": {body: body, raw: raw}}}
	page := `<html><body><div id="out">pending</div>` +
		`<script src="https://cdn.example.com/app.js" integrity="` + integrityFor(raw) + `"></script></body></html>`
	doc := renderSRI(t, page, js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "RAN" {
		t.Errorf("#out=%q want RAN (SRI must hash raw bytes, not the transcoded body)", got)
	}
}

func TestSRIInsertedScriptMismatchFiresError(t *testing.T) {
	chunk := []byte(`document.getElementById('out').textContent='loaded';`)
	tr := &sriTransport{routes: map[string]sriRoute{"chunk.js": {body: chunk}}}
	bad := integrityFor([]byte("nope"))
	page := `<html><body><div id="out">pending</div><script>
		var s = document.createElement('script');
		s.src = 'https://cdn.example.com/chunk.js';
		s.setAttribute('integrity', '` + bad + `');
		s.onload = function(){ document.getElementById('out').textContent = 'loaded'; };
		s.onerror = function(){ document.getElementById('out').textContent = 'error'; };
		document.body.appendChild(s);
	</script></body></html>`
	doc := renderSRI(t, page, js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "error" {
		t.Errorf("#out=%q want error (mismatched inserted chunk must fire the error event, not run)", got)
	}
}

func TestSRIModuleEntryMismatchBlocked(t *testing.T) {
	mod := []byte(`document.getElementById('out').textContent='MOD';`)
	tr := &sriTransport{routes: map[string]sriRoute{"entry.mjs": {body: mod}}}
	bad := integrityFor([]byte("tampered"))
	page := `<html><body><div id="out">pending</div>` +
		`<script type="module" src="https://cdn.example.com/entry.mjs" integrity="` + bad + `"></script></body></html>`
	doc := renderSRI(t, page, js.Env{}, tr)
	if got := divText(t, doc, "out"); got != "pending" {
		t.Errorf("#out=%q want pending (mismatched module entry must not execute)", got)
	}
}

func hasIntegrityError(d *js.RenderResult) bool {
	for _, e := range d.Errors {
		if strings.Contains(e, "integrity") {
			return true
		}
	}
	return false
}
