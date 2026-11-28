package js_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// dataTransport records every URL it is asked for and every byte charge, and
// can refuse charges (a spent download budget).
type dataTransport struct {
	mu      sync.Mutex
	routes  map[string]string
	urls    []string
	charged int64
	refuse  bool
}

func (d *dataTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	d.mu.Lock()
	d.urls = append(d.urls, url)
	d.mu.Unlock()
	for k, v := range d.routes {
		if strings.HasSuffix(url, k) {
			return &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/javascript"}, Body: []byte(v), FinalURL: url}, nil
		}
	}
	return &js.Response{Status: 404, FinalURL: url}, nil
}

func (d *dataTransport) ChargeBytes(n int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.refuse {
		return errors.New("download budget exceeded")
	}
	d.charged += n
	return nil
}

func (d *dataTransport) sawDataURL() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, u := range d.urls {
		if strings.HasPrefix(strings.ToLower(u), "data:") {
			return true
		}
	}
	return false
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func dataJS(src string) string { return "data:text/javascript;base64," + b64(src) }

func docHTML(t *testing.T, doc *html.Node) string {
	t.Helper()
	var sb strings.Builder
	if err := html.Render(&sb, doc); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// Every script-loading path decodes a data: URL locally: it runs, and the
// transport never sees it.
func TestDataURLScriptsOnEveryPath(t *testing.T) {
	mark := func(tag string) string {
		return `document.getElementById('out').appendChild(document.createTextNode('[` + tag + `]'));`
	}
	cases := []struct{ name, page, want string }{
		{"parser classic script", `<div id="out"></div><script src="` + dataJS(mark("classic")) + `"></script>`, "[classic]"},
		{"percent-encoded form", `<div id="out"></div><script src="data:text/javascript,` + strings.NewReplacer(" ", "%20", "'", "%27", "[", "%5B", "]", "%5D").Replace(mark("pct")) + `"></script>`, "[pct]"},
		{"script-inserted, with its load event", `<div id="out"></div><script>
			var s = document.createElement('script'); s.setAttribute('src', "` + dataJS(mark("inserted")) + `");
			s.addEventListener('load', function () { ` + mark("load") + ` });
			document.head.appendChild(s);</script>`, "[inserted][load]"},
		{"module entry", `<div id="out"></div><script type="module" src="` + dataJS(mark("module")) + `"></script>`, "[module]"},
		{"dynamic import from a classic script", `<div id="out"></div><script>
			import("` + dataJS(`export default 'dyn';`) + `").then(function (m) { document.getElementById('out').textContent += '[' + m.default + ']'; });</script>`, "[dyn]"},
		{"static import inside a module", `<div id="out"></div><script type="module">
			import v from "` + dataJS(`export default 'dep';`) + `"; document.getElementById('out').textContent += '[' + v + ']';</script>`, "[dep]"},
		{"preload and modulepreload hints are not fetched", `<link rel="preload" as="script" href="` + dataJS(mark("x")) + `"><link rel="modulepreload" href="` + dataJS(`export default 1;`) + `">
			<link rel="modulepreload" href="/a.mjs"><link rel="modulepreload" href="/b.mjs"><div id="out"></div><script>` + mark("page") + `</script>`, "[page]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &dataTransport{routes: map[string]string{"/a.mjs": "export default 1;", "/b.mjs": "export default 2;"}}
			var diag js.RenderResult
			out := renderWithEnv(t, `<html><head></head><body>`+c.page+`</body></html>`, js.Env{Transport: tr, Diag: &diag})
			if !strings.Contains(out, c.want) {
				t.Errorf("script did not run: want %q in\n%s\nerrors: %v", c.want, out, diag.Errors)
			}
			if tr.sawDataURL() {
				t.Errorf("a data: URL reached the transport: %v", tr.urls)
			}
		})
	}
}

// The decoded bytes are charged to the download budget, and a spent budget
// stops a data: script exactly as it stops a network one.
func TestDataURLScriptsChargeTheByteBudget(t *testing.T) {
	src := `document.getElementById('out').textContent = 'ran';`
	page := `<html><body><div id="out"></div><script src="` + dataJS(src) + `"></script></body></html>`
	tr := &dataTransport{}
	out := renderWithEnv(t, page, js.Env{Transport: tr})
	if !strings.Contains(out, ">ran<") || tr.charged != int64(len(src)) {
		t.Errorf("charged %d bytes, want %d; out=%s", tr.charged, len(src), out)
	}
	spent := &dataTransport{refuse: true}
	out = renderWithEnv(t, page, js.Env{Transport: spent})
	if strings.Contains(out, ">ran<") {
		t.Errorf("a data: script ran past a spent budget:\n%s", out)
	}
}

// script-src gates data: scripts: only an explicit `data:` source (or
// 'strict-dynamic' trust) admits one — not a host-source, not `*`, not 'self'.
func TestDataURLScriptsObeyCSP(t *testing.T) {
	ran := `document.getElementById('out').textContent += '[ran]';`
	parser := `<html><body><div id="out"></div><script src="` + dataJS(ran) + `"></script></body></html>`
	inserted := `<html><body><div id="out"></div><script nonce="n1">
		var s = document.createElement('script'); s.setAttribute('src', "` + dataJS(ran) + `"); document.head.appendChild(s);</script></body></html>`
	dynImport := `<html><body><div id="out"></div><script nonce="n1">
		import("` + dataJS(`export default 1;`) + `").then(function () { ` + ran + ` }, function () { document.getElementById('out').textContent += '[rejected]'; });</script></body></html>`
	modDep := `<html><body><div id="out"></div><script type="module" nonce="n1">
		import v from "` + dataJS(`export default 1;`) + `"; ` + ran + `</script></body></html>`
	cases := []struct {
		name, page, policy string
		wantRun            bool
	}{
		{"no policy", parser, "", true},
		{"'self' does not cover data:", parser, "script-src 'self'", false},
		{"* does not cover data:", parser, "script-src *", false},
		{"a host-source does not cover data:", parser, "script-src https://example.com", false},
		{"default-src applies", parser, "default-src 'self'", false},
		{"explicit data: allows", parser, "script-src 'self' data:", true},
		{"inserted: blocked without data:", inserted, "script-src 'nonce-n1'", false},
		{"inserted: strict-dynamic propagates trust", inserted, "script-src 'nonce-n1' 'strict-dynamic'", true},
		{"inserted: explicit data: allows", inserted, "script-src 'nonce-n1' data:", true},
		{"import(): blocked without data:", dynImport, "script-src 'nonce-n1'", false},
		{"import(): explicit data: allows", dynImport, "script-src 'nonce-n1' data:", true},
		{"import(): strict-dynamic allows", dynImport, "script-src 'nonce-n1' 'strict-dynamic'", true},
		{"module dependency: blocked without data:", modDep, "script-src 'nonce-n1'", false},
		{"module dependency: explicit data: allows", modDep, "script-src 'nonce-n1' data:", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.policy != "" {
				h = cspHeader(c.policy)
			}
			tr := &dataTransport{}
			doc, diag := renderCSP(t, c.page, "https://example.com/", h, tr)
			out := docHTML(t, doc)
			if got := regexp.MustCompile(`<div id="out">[^<]*\[ran\]`).MatchString(out); got != c.wantRun {
				t.Errorf("ran=%v, want %v\nout: %s\nerrors: %v", got, c.wantRun, out, diag.Errors)
			}
			if tr.sawDataURL() {
				t.Errorf("a data: URL reached the transport")
			}
		})
	}
}

// integrity on a data: script is checked against the decoded bytes.
func TestDataURLScriptsObeySRI(t *testing.T) {
	src := `document.getElementById('out').textContent = 'ran';`
	sum := sha256.Sum256([]byte(src))
	good := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	bad := "sha256-" + base64.StdEncoding.EncodeToString(make([]byte, 32))
	for name, tc := range map[string]struct {
		integrity string
		want      bool
	}{"matching hash runs": {good, true}, "mismatched hash is blocked": {bad, false}} {
		t.Run(name, func(t *testing.T) {
			out := renderWithEnv(t, `<html><body><div id="out"></div><script integrity="`+tc.integrity+`" src="`+dataJS(src)+`"></script></body></html>`,
				js.Env{Transport: &dataTransport{}})
			if got := strings.Contains(out, ">ran<"); got != tc.want {
				t.Errorf("ran=%v, want %v\n%s", got, tc.want, out)
			}
		})
	}
}
