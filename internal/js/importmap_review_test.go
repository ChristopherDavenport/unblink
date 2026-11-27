package js_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// Regression tests for the import map review: when the map is acquired, its CSP
// gate, lookup cost, URL Standard normalization, and two parsing rules.

// outText returns the text of #out in a rendered document.
func outText(t *testing.T, doc *html.Node) string {
	t.Helper()
	var buf strings.Builder
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	out := buf.String()
	const open = `<div id="out">`
	i := strings.Index(out, open)
	if i < 0 {
		t.Fatalf("no #out in output:\n%s", out)
	}
	rest := out[i+len(open):]
	return rest[:strings.Index(rest, "</div>")]
}

// renderMap renders page at https://example.com/page with the given response
// headers and returns #out, the diagnostics, and the transport (for what was fetched).
func renderMap(t *testing.T, page string, routes map[string]string, env js.Env) (string, *js.RenderResult, *exactTransport) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse(origin + "/page")
	tr := &exactTransport{routes: routes}
	diag := &js.RenderResult{}
	env.Transport, env.Diag = tr, diag
	if err := js.New(js.WithTimeout(3*time.Second)).Render(context.Background(), doc, base, env); err != nil {
		t.Fatalf("render: %v", err)
	}
	return outText(t, doc), diag, tr
}

func (e *exactTransport) wasFetched(u string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range e.fetched {
		if f == u {
			return true
		}
	}
	return false
}

func errorsContaining(d *js.RenderResult, sub string) int {
	n := 0
	for _, e := range d.Errors {
		if strings.Contains(e, sub) {
			n++
		}
	}
	return n
}

// ---- 1. acquisition: when module loading begins, not before classic scripts ----

const injectMap = `
	var s = document.createElement('script');
	s.type = 'importmap';
	s.textContent = JSON.stringify({imports: {dep: '/dep.js'}});
	document.head.appendChild(s);`

// A classic script that builds the import map and appends it is honoured by a later
// module script: the map is read when modules start resolving.
func TestImportMapInjectedByClassicScript(t *testing.T) {
	got := renderScoped(t, `<html><head></head><body><div id="out">?</div>
		<script>`+injectMap+`</script>
		<script type="module">
		  import { v } from 'dep';
		  document.getElementById('out').textContent = v;
		</script></body></html>`, map[string]string{
		origin + "/dep.js": `export const v = 'INJECTED';`,
	})
	if got != "INJECTED" {
		t.Errorf("script-injected import map not honoured: got %q", got)
	}
}

// The same holds for a classic script's own import(), which is why the map is
// acquired at the first import() too rather than only by the module pass.
func TestImportMapInjectedBeforeDynamicImport(t *testing.T) {
	got := renderScoped(t, `<html><head></head><body><div id="out">?</div>
		<script>`+injectMap+`
		  import('dep').then(function (m) { document.getElementById('out').textContent = m.v; })
		    .catch(function (e) { document.getElementById('out').textContent = 'ERR ' + e; });
		</script></body></html>`, map[string]string{
		origin + "/dep.js": `export const v = 'INJECTED';`,
	})
	if got != "INJECTED" {
		t.Errorf("script-injected import map not honoured by import(): got %q", got)
	}
}

// Once a module has started loading, import maps are no longer allowed: a map added
// afterwards is ignored, and it must not disturb what was already resolved.
func TestImportMapAddedAfterModuleLoadingIsIgnored(t *testing.T) {
	routes := map[string]string{
		origin + "/first.js": `export const first = 1;`,
		origin + "/dep.js":   `export const v = 'LATE';`,
	}
	t.Run("added by a module script", func(t *testing.T) {
		got, _, tr := renderMap(t, `<html><head></head><body><div id="out">?</div>
			<script type="module">`+injectMap+`</script>
			<script type="module">
			  import { v } from 'dep';
			  document.getElementById('out').textContent = v;
			</script></body></html>`, routes, js.Env{})
		if got != "?" || tr.wasFetched(origin+"/dep.js") {
			t.Errorf("import map added after module loading began was honoured: got %q", got)
		}
	})
	t.Run("added after an import()", func(t *testing.T) {
		got, _, tr := renderMap(t, `<html><head></head><body><div id="out">?</div>
			<script>
			  import('/first.js').then(function () {`+injectMap+`
			    return import('dep');
			  }).then(function (m) { document.getElementById('out').textContent = m.v; })
			    .catch(function (e) { document.getElementById('out').textContent = 'REJECTED'; });
			</script></body></html>`, routes, js.Env{})
		if got != "REJECTED" || tr.wasFetched(origin+"/dep.js") {
			t.Errorf("import map added after the first import() was honoured: got %q", got)
		}
	})
}

// ---- 2. CSP gate ----

const hijackMap = `{"imports":{"/good.js":"/evil.js"}}`

func hijackPage(mapAttrs string) string {
	return `<html><head></head><body><div id="out">?</div>
		<script type="importmap"` + mapAttrs + `>` + hijackMap + `</script>
		<script type="module" nonce="abc">
		  import { v } from '/good.js';
		  document.getElementById('out').textContent = v;
		</script></body></html>`
}

var hijackRoutes = map[string]string{
	origin + "/good.js": `export const v = 'GOOD';`,
	origin + "/evil.js": `export const v = 'EVIL';`,
}

func TestImportMapCSPGate(t *testing.T) {
	sum := sha256.Sum256([]byte(hijackMap))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	enforce := func(v string) http.Header { return http.Header{"Content-Security-Policy": {v}} }
	const blockedMsg = `blocked by Content-Security-Policy: inline <script type="importmap">`

	tests := []struct {
		name       string
		mapAttrs   string
		env        js.Env
		want       string
		wantErrSub string // "" means: no CSP diagnostic at all
	}{
		{"non-nonced map is refused", ``, js.Env{ResponseHeaders: enforce("script-src 'nonce-abc'")}, "GOOD", blockedMsg},
		{"wrong nonce is refused", ` nonce="nope"`, js.Env{ResponseHeaders: enforce("script-src 'nonce-abc'")}, "GOOD", blockedMsg},
		{"nonced map is honoured", ` nonce="abc"`, js.Env{ResponseHeaders: enforce("script-src 'nonce-abc'")}, "EVIL", ""},
		{"hash-allowed map is honoured", ``, js.Env{ResponseHeaders: enforce("script-src 'nonce-abc' " + hash)}, "EVIL", ""},
		{"strict-dynamic does not trust a parser-inserted map", ``, js.Env{ResponseHeaders: enforce("script-src 'nonce-abc' 'strict-dynamic'")}, "GOOD", blockedMsg},
		{"report-only reports without blocking", ``, js.Env{ResponseHeaders: http.Header{"Content-Security-Policy-Report-Only": {"script-src 'nonce-abc'"}}}, "EVIL",
			`Content-Security-Policy report-only: would block inline <script type="importmap">`},
		{"DisableCSP turns the gate off", ``, js.Env{ResponseHeaders: enforce("script-src 'nonce-abc'"), DisableCSP: true}, "EVIL", ""},
		{"no policy, no gate", ``, js.Env{}, "EVIL", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, diag, tr := renderMap(t, hijackPage(tc.mapAttrs), hijackRoutes, tc.env)
			if got != tc.want {
				t.Errorf("got %q, want %q (errors: %v)", got, tc.want, diag.Errors)
			}
			if tc.want == "GOOD" && tr.wasFetched(origin+"/evil.js") {
				t.Error("blocked import map still caused /evil.js to be fetched")
			}
			if tc.wantErrSub == "" {
				if n := errorsContaining(diag, "Content-Security-Policy"); n != 0 {
					t.Errorf("unexpected CSP diagnostics: %v", diag.Errors)
				}
			} else if n := errorsContaining(diag, tc.wantErrSub); n != 1 {
				t.Errorf("want exactly one %q diagnostic, got %d: %v", tc.wantErrSub, n, diag.Errors)
			}
		})
	}
}

// A script-inserted import map goes through the same gate: page JS that is allowed
// to run cannot register a map the policy would refuse (it has no nonce to give it,
// since nonces are hidden from the DOM), while one it can vouch for by hash works.
func TestImportMapCSPGateScriptInserted(t *testing.T) {
	page := func(policyNonceScript string) string {
		return `<html><head></head><body><div id="out">?</div>
		<script nonce="abc">
		  var s = document.createElement('script');
		  s.setAttribute('type', 'importmap');
		  ` + policyNonceScript + `
		  s.textContent = '` + hijackMap + `';
		  document.head.appendChild(s);
		</script>
		<script type="module" nonce="abc">
		  import { v } from '/good.js';
		  document.getElementById('out').textContent = v;
		</script></body></html>`
	}
	env := js.Env{ResponseHeaders: http.Header{"Content-Security-Policy": {"script-src 'nonce-abc'"}}}

	got, diag, _ := renderMap(t, page(`s.setAttribute('nonce', document.currentScript.getAttribute('nonce'));`), hijackRoutes, env)
	if got != "GOOD" || errorsContaining(diag, `inline <script type="importmap">`) != 1 {
		t.Errorf("map carrying a scraped (hidden, so empty) nonce: got %q, errors %v", got, diag.Errors)
	}
	got, diag, _ = renderMap(t, page(`s.setAttribute('nonce', 'abc');`), hijackRoutes, env)
	if got != "EVIL" {
		t.Errorf("script-inserted map with the real nonce should register: got %q, errors %v", got, diag.Errors)
	}
}

// A map the policy refuses never registers, so a later allowed one still can.
func TestImportMapCSPBlockedMapDoesNotConsumeTheSlot(t *testing.T) {
	got, _, _ := renderMap(t, `<html><head></head><body><div id="out">?</div>
		<script type="importmap">{"imports":{"dep":"/evil.js"}}</script>
		<script type="importmap" nonce="abc">{"imports":{"dep":"/good.js"}}</script>
		<script type="module" nonce="abc">
		  import { v } from 'dep';
		  document.getElementById('out').textContent = v;
		</script></body></html>`, hijackRoutes,
		js.Env{ResponseHeaders: http.Header{"Content-Security-Policy": {"script-src 'nonce-abc'"}}})
	if got != "GOOD" {
		t.Errorf("got %q, want the nonced map's GOOD", got)
	}
}

// ---- 3. lookup cost ----

// anyModuleTransport serves a tiny module for every URL.
type anyModuleTransport struct{}

func (anyModuleTransport) Do(_ context.Context, _ string, u string, _ map[string]string, _ []byte) (*js.Response, error) {
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/javascript"},
		Body: []byte(`globalThis.__n = (globalThis.__n || 0) + 1;`), FinalURL: u}, nil
}

// bigMapPage has keys exact keys plus as many trailing-slash keys, and a module
// script with imports static imports: half hit an exact key, half fall to a prefix
// key (which used to scan every key).
func bigMapPage(keys, imports int, viaImportCall bool) string {
	var m, src strings.Builder
	m.WriteString(`{"imports":{`)
	for i := 0; i < keys; i++ {
		if i > 0 {
			m.WriteByte(',')
		}
		fmt.Fprintf(&m, `"k%d":"/m/%d.js","p%d/":"/p/%d/"`, i, i, i, i)
	}
	m.WriteString(`}}`)
	for i := 0; i < imports; i++ {
		spec := fmt.Sprintf("k%d", i)
		if i%2 == 1 {
			spec = fmt.Sprintf("p%d/a/b/c.js", i)
		}
		fmt.Fprintf(&src, "import '%s';\n", spec)
	}
	body := `<script type="module">` + src.String() + `document.getElementById('out').textContent = 'n=' + globalThis.__n;</script>`
	if viaImportCall {
		// The registry path: /entry.js carries the static imports and is import()ed.
		body = `<script>import('/entry.js').then(function () { document.getElementById('out').textContent = 'n=' + globalThis.__n; })
			.catch(function (e) { document.getElementById('out').textContent = 'ERR ' + e; });</script>`
	}
	return `<html><head></head><body><div id="out">?</div><script type="importmap">` + m.String() + `</script>` + body + `</body></html>`
}

// 20,000 exact keys + 20,000 prefix keys and 2,000 imports must finish, with every
// module loaded, inside the engine timeout. The assertion is on the outcome, not on
// a duration: a lookup that is linear in the key count does not get there.
func TestImportMapLargeMapResolvesAllImports(t *testing.T) {
	const keys, imports = 20_000, 2_000
	t.Run("static module", func(t *testing.T) {
		got := renderWith(t, bigMapPage(keys, imports, false), anyModuleTransport{})
		if want := fmt.Sprintf(`<div id="out">n=%d</div>`, imports); !strings.Contains(got, want) {
			t.Errorf("want %s in output; #out region: %.200s", want, got[strings.Index(got, `<div id="out">`):])
		}
	})
	t.Run("import() registry", func(t *testing.T) {
		var src strings.Builder
		for i := 0; i < imports; i++ {
			spec := fmt.Sprintf("k%d", i)
			if i%2 == 1 {
				spec = fmt.Sprintf("p%d/a/b/c.js", i)
			}
			fmt.Fprintf(&src, "import '%s';\n", spec)
		}
		tr := entryTransport{entry: src.String()}
		got := renderWith(t, bigMapPage(keys, imports, true), tr)
		// imports modules plus none for /entry.js itself (it only imports).
		if want := fmt.Sprintf(`<div id="out">n=%d</div>`, imports); !strings.Contains(got, want) {
			t.Errorf("want %s in output; #out region: %.200s", want, got[strings.Index(got, `<div id="out">`):])
		}
	})
}

// entryTransport serves entry for /entry.js and a counting module for anything else.
type entryTransport struct{ entry string }

func (e entryTransport) Do(ctx context.Context, method, u string, h map[string]string, b []byte) (*js.Response, error) {
	if u == origin+"/entry.js" {
		return &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/javascript"}, Body: []byte(e.entry), FinalURL: u}, nil
	}
	return anyModuleTransport{}.Do(ctx, method, u, h, b)
}

// A map over the size bounds is rejected whole and reported, not truncated.
func TestImportMapOverLimitRejected(t *testing.T) {
	var m strings.Builder
	m.WriteString(`{"imports":{"dep":"/dep.js"`)
	for i := 0; i < 50_001; i++ {
		fmt.Fprintf(&m, `,"k%d":"/k.js"`, i)
	}
	m.WriteString(`}}`)
	got, diag, _ := renderMap(t, `<html><body><div id="out">?</div>
		<script type="importmap">`+m.String()+`</script>
		<script type="module">
		  import { v } from 'dep';
		  document.getElementById('out').textContent = v;
		</script></body></html>`, map[string]string{origin + "/dep.js": `export const v = 'DEP';`}, js.Env{})
	if got != "?" || errorsContaining(diag, "import map ignored") != 1 {
		t.Errorf("oversized import map: got %q, errors %v", got, diag.Errors)
	}
}

// ---- 4. URL Standard normalization ----

func TestImportMapURLStandardNormalization(t *testing.T) {
	tests := []struct {
		name, importMap, spec, serve string
	}{
		{"address with dot segments", `{"imports":{"lib/":"https://example.com/x/../lib/"}}`, "lib/a.js", origin + "/lib/a.js"},
		{"remainder containing a colon", `{"imports":{"lib/":"/lib/"}}`, "lib/1:2.js", origin + "/lib/1:2.js"},
		{"URL-like key differing in host case", `{"imports":{"https://EXAMPLE.com/a.js":"/mapped.js"}}`, "https://example.com/a.js", origin + "/mapped.js"},
		{"URL-like key with dot segments and default port", `{"imports":{"https://example.com:443/x/../a.js":"/mapped.js"}}`, "/a.js", origin + "/mapped.js"},
		{"specifier with dot segments meets a normalized key", `{"imports":{"/a.js":"/mapped.js"}}`, "/x/../a.js", origin + "/mapped.js"},
		{"scope prefix differing in host case", `{"scopes":{"https://EXAMPLE.com/":{"dep":"/mapped.js"}}}`, "dep", origin + "/mapped.js"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderScoped(t, `<html><body><div id="out">?</div>
				<script type="importmap">`+tc.importMap+`</script>
				<script type="module">
				  import { v } from '`+tc.spec+`';
				  document.getElementById('out').textContent = v;
				</script></body></html>`, map[string]string{tc.serve: `export const v = 'OK';`})
			if got != "OK" {
				t.Errorf("import %q with %s: got %q, want it served from %s", tc.spec, tc.importMap, got, tc.serve)
			}
		})
	}
}

// The rules that were already right stay right: a remainder that climbs out of the
// mapped address is refused, and a prefix key does not apply to a non-special URL.
func TestImportMapBacktrackingStillRefused(t *testing.T) {
	got, _, tr := renderMap(t, `<html><body><div id="out">?</div>
		<script type="importmap">{"imports":{"lib/":"/lib/"}}</script>
		<script type="module">
		  import { v } from 'lib/../secret.js';
		  document.getElementById('out').textContent = v;
		</script></body></html>`, map[string]string{origin + "/secret.js": `export const v = 'ESCAPED';`}, js.Env{})
	if got != "?" || tr.wasFetched(origin+"/secret.js") {
		t.Errorf("backtracking above the mapped address was allowed: got %q", got)
	}
}

// ---- 5. parsing rules ----

// Two keys that normalize to the same URL: the later one in source order wins.
// Repeated because the old Go-map decode made the winner vary from run to run.
func TestImportMapDuplicateNormalizedKeysLastWins(t *testing.T) {
	routes := map[string]string{
		origin + "/first.js": `export const v = 'FIRST';`,
		origin + "/last.js":  `export const v = 'LAST';`,
	}
	maps := []string{
		`{"imports":{"/a.js":"/first.js","https://example.com/a.js":"/last.js"}}`,
		`{"imports":{"https://EXAMPLE.com/a.js":"/first.js","./a.js":"/first.js","/x/../a.js":"/last.js"}}`,
		`{"imports":{"/a.js":"/last.js"},"scopes":{"/":{"/a.js":"/first.js"},"https://example.com/":{"/a.js":"/first.js"},"https://EXAMPLE.com:443/":{}}}`,
	}
	for _, im := range maps {
		for i := 0; i < 20; i++ {
			got := renderScoped(t, `<html><body><div id="out">?</div>
				<script type="importmap">`+im+`</script>
				<script type="module">
				  import { v } from '/a.js';
				  document.getElementById('out').textContent = v;
				</script></body></html>`, routes)
			if got != "LAST" {
				t.Fatalf("run %d of %s: got %q, want LAST", i, im, got)
			}
		}
	}
}

// A null or non-object "imports" / "scopes" / scope value rejects the whole map
// (the spec's TypeError), so nothing in it applies, and the rejection is reported.
func TestImportMapNonObjectMembersRejectWholeMap(t *testing.T) {
	maps := []string{
		`{"imports":null,"scopes":{"/":{"dep":"/dep.js"}}}`,
		`{"imports":["dep"],"scopes":{"/":{"dep":"/dep.js"}}}`,
		`{"imports":{"dep":"/dep.js"},"scopes":null}`,
		`{"imports":{"dep":"/dep.js"},"scopes":"x"}`,
		`{"imports":{"dep":"/dep.js"},"scopes":{"/other/":null}}`,
		`[{"imports":{"dep":"/dep.js"}}]`,
		`{"imports":{"dep":"/dep.js"}} trailing`,
	}
	for _, im := range maps {
		got, diag, tr := renderMap(t, `<html><body><div id="out">?</div>
			<script type="importmap">`+im+`</script>
			<script type="module">
			  import { v } from 'dep';
			  document.getElementById('out').textContent = v;
			</script></body></html>`, map[string]string{origin + "/dep.js": `export const v = 'DEP';`}, js.Env{})
		if got != "?" || tr.wasFetched(origin+"/dep.js") {
			t.Errorf("%s: map was not rejected whole, got %q", im, got)
		}
		if errorsContaining(diag, "import map ignored") != 1 {
			t.Errorf("%s: want one 'import map ignored' diagnostic, got %v", im, diag.Errors)
		}
	}
}
