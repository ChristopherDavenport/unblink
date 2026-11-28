package js_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

// Import map "scopes" (HTML Standard, "resolve a module specifier"): a bare
// specifier may map to a different module depending on the URL of the module that
// imports it. These tests pin that on both loaders — the static
// <script type=module> path and dynamic import(), both now the module registry.

// exactTransport serves module bodies by exact URL, so two routes that differ only
// by a path prefix (/dep.js vs /scoped/dep.js) can never be confused, and records
// what was fetched.
type exactTransport struct {
	mu      sync.Mutex
	routes  map[string]string
	fetched []string
}

func (e *exactTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	e.mu.Lock()
	e.fetched = append(e.fetched, url)
	e.mu.Unlock()
	body, ok := e.routes[url]
	if !ok {
		return &js.Response{Status: 404, Body: []byte("not found"), FinalURL: url}, nil
	}
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/javascript"}, Body: []byte(body), FinalURL: url}, nil
}

// renderScoped renders page (at https://example.com/page) and returns the text the
// page wrote into #out.
func renderScoped(t *testing.T, page string, routes map[string]string) string {
	t.Helper()
	tr := &exactTransport{routes: routes}
	out := renderWith(t, page, tr)
	const open = `<div id="out">`
	i := strings.Index(out, open)
	if i < 0 {
		t.Fatalf("no #out in output:\n%s", out)
	}
	rest := out[i+len(open):]
	return rest[:strings.Index(rest, "</div>")]
}

const origin = "https://example.com"

func TestImportMapScopeOverridesTopLevel(t *testing.T) {
	got := renderScoped(t, `<html><body><div id="out">?</div>
		<script type="importmap">{
		  "imports": {"dep": "/dep-top.js"},
		  "scopes":  {"/scoped/": {"dep": "/dep-scoped.js"}}
		}</script>
		<script type="module">
		  import { a } from '/scoped/a.js';
		  import { b } from '/plain/b.js';
		  import { v } from 'dep';
		  document.getElementById('out').textContent = 'a=' + a + ' b=' + b + ' doc=' + v;
		</script></body></html>`, map[string]string{
		origin + "/scoped/a.js":   `import { v } from 'dep'; export const a = v;`,
		origin + "/plain/b.js":    `import { v } from 'dep'; export const b = v;`,
		origin + "/dep-top.js":    `export const v = 'TOP';`,
		origin + "/dep-scoped.js": `export const v = 'SCOPED';`,
	})
	if got != "a=SCOPED b=TOP doc=TOP" {
		t.Errorf("scoped remap not applied per importer: got %q", got)
	}
}

func TestImportMapLongestScopeWins(t *testing.T) {
	got := renderScoped(t, `<html><body><div id="out">?</div>
		<script type="importmap">{
		  "imports": {"dep": "/dep-top.js"},
		  "scopes":  {
		    "/a/":   {"dep": "/dep-shallow.js"},
		    "/a/b/": {"dep": "/dep-deep.js"}
		  }
		}</script>
		<script type="module">
		  import { v as deep } from '/a/b/m.js';
		  import { v as shallow } from '/a/m.js';
		  document.getElementById('out').textContent = 'deep=' + deep + ' shallow=' + shallow;
		</script></body></html>`, map[string]string{
		origin + "/a/b/m.js":       `export { v } from 'dep';`,
		origin + "/a/m.js":         `export { v } from 'dep';`,
		origin + "/dep-top.js":     `export const v = 'TOP';`,
		origin + "/dep-shallow.js": `export const v = 'SHALLOW';`,
		origin + "/dep-deep.js":    `export const v = 'DEEP';`,
	})
	if got != "deep=DEEP shallow=SHALLOW" {
		t.Errorf("most specific scope did not win: got %q", got)
	}
}

// A module inside a scope whose specifier map lacks the specifier falls through
// to a less specific scope, then to top-level "imports".
func TestImportMapScopeFallsBackToImports(t *testing.T) {
	got := renderScoped(t, `<html><body><div id="out">?</div>
		<script type="importmap">{
		  "imports": {"dep": "/dep-top.js", "other": "/other-top.js"},
		  "scopes":  {
		    "/a/":   {"mid": "/mid-shallow.js"},
		    "/a/b/": {"dep": "/dep-deep.js"}
		  }
		}</script>
		<script type="module">
		  import { dep, mid, other } from '/a/b/m.js';
		  document.getElementById('out').textContent = dep + ' ' + mid + ' ' + other;
		</script></body></html>`, map[string]string{
		origin + "/a/b/m.js": `import { v as dep } from 'dep'; import { v as mid } from 'mid'; import { v as other } from 'other';
			export { dep, mid, other };`,
		origin + "/dep-top.js":     `export const v = 'DEP-TOP';`,
		origin + "/dep-deep.js":    `export const v = 'DEP-DEEP';`,
		origin + "/mid-shallow.js": `export const v = 'MID-SHALLOW';`,
		origin + "/other-top.js":   `export const v = 'OTHER-TOP';`,
	})
	if got != "DEP-DEEP MID-SHALLOW OTHER-TOP" {
		t.Errorf("scope fallback chain wrong: got %q", got)
	}
}

func TestImportMapScopeTrailingSlashKey(t *testing.T) {
	got := renderScoped(t, `<html><body><div id="out">?</div>
		<script type="importmap">{
		  "imports": {"pkg/": "/vendor/pkg-v1/"},
		  "scopes":  {"/legacy/": {"pkg/": "/vendor/pkg-v0/", "pkg/exact.js": "/vendor/exact.js"}}
		}</script>
		<script type="module">
		  import { v as modern } from '/app/m.js';
		  import { v as legacy, e } from '/legacy/m.js';
		  document.getElementById('out').textContent = 'modern=' + modern + ' legacy=' + legacy + ' exact=' + e;
		</script></body></html>`, map[string]string{
		origin + "/app/m.js":                  `export { v } from 'pkg/sub/util.js';`,
		origin + "/legacy/m.js":               `export { v } from 'pkg/sub/util.js'; export { e } from 'pkg/exact.js';`,
		origin + "/vendor/pkg-v1/sub/util.js": `export const v = 'V1';`,
		origin + "/vendor/pkg-v0/sub/util.js": `export const v = 'V0';`,
		origin + "/vendor/exact.js":           `export const e = 'EXACT';`,
	})
	if got != "modern=V1 legacy=V0 exact=EXACT" {
		t.Errorf("trailing-slash prefix key inside a scope wrong: got %q", got)
	}
}

// The dynamic import() registry path: a classic script import()s a module that
// lives inside a scope. Its static import, its own nested import() and a relative
// import() must all resolve with THAT module as referrer, while the classic
// script's own import('dep') has the document as referrer and gets the top-level
// mapping.
func TestImportMapScopeDynamicImport(t *testing.T) {
	got := renderScoped(t, `<html><body><div id="out">?</div>
		<script type="importmap">{
		  "imports": {"dep": "/dep-top.js", "lazy": "/lazy-top.js"},
		  "scopes":  {"/scoped/": {"dep": "/dep-scoped.js", "lazy": "/lazy-scoped.js"}}
		}</script>
		<script>
		  Promise.all([import('/scoped/entry.js'), import('dep')]).then(function (m) {
		    return m[0].load().then(function (parts) {
		      document.getElementById('out').textContent =
		        'static=' + m[0].dep + ' nested=' + parts[0].v + ' sibling=' + parts[1].v + ' doc=' + m[1].v;
		    });
		  }).catch(function (e) { document.getElementById('out').textContent = 'ERR ' + e; });
		</script></body></html>`, map[string]string{
		origin + "/scoped/entry.js": `import { v } from 'dep';
			export const dep = v;
			export function load() { return Promise.all([import('lazy'), import('./sibling.js')]); }`,
		origin + "/scoped/sibling.js": `export const v = 'SIBLING';`,
		origin + "/dep-top.js":        `export const v = 'DEP-TOP';`,
		origin + "/dep-scoped.js":     `export const v = 'DEP-SCOPED';`,
		origin + "/lazy-top.js":       `export const v = 'LAZY-TOP';`,
		origin + "/lazy-scoped.js":    `export const v = 'LAZY-SCOPED';`,
	})
	if got != "static=DEP-SCOPED nested=LAZY-SCOPED sibling=SIBLING doc=DEP-TOP" {
		t.Errorf("dynamic import() did not honour scopes with the importing module as referrer: got %q", got)
	}
}

// A literal import() inside a statically bundled module is resolved by the bundler
// with that module as the importer, so it is scoped too.
func TestImportMapScopeDynamicImportInStaticModule(t *testing.T) {
	got := renderScoped(t, `<html><body><div id="out">?</div>
		<script type="importmap">{
		  "imports": {"lazy": "/lazy-top.js"},
		  "scopes":  {"/scoped/": {"lazy": "/lazy-scoped.js"}}
		}</script>
		<script type="module" src="/scoped/app.js"></script></body></html>`, map[string]string{
		origin + "/scoped/app.js":  `import('lazy').then(function (m) { document.getElementById('out').textContent = m.v; });`,
		origin + "/lazy-top.js":    `export const v = 'LAZY-TOP';`,
		origin + "/lazy-scoped.js": `export const v = 'LAZY-SCOPED';`,
	})
	if got != "LAZY-SCOPED" {
		t.Errorf("import() in a static module ignored its scope: got %q", got)
	}
}

// An invalid entry drops (or nulls) only itself: a non-string address, a
// trailing-slash key whose address lacks the slash, and an unparseable scope prefix
// leave the rest of the map working.
func TestImportMapInvalidEntriesIgnored(t *testing.T) {
	got := renderScoped(t, `<html><body><div id="out">?</div>
		<script type="importmap">{
		  "imports": {"bad": 5, "": "/empty.js", "slash/": "/no-trailing-slash", "dep": "/dep-top.js"},
		  "scopes":  {
		    "http://[bad": {"dep": "/never.js"},
		    "/scoped/":    {"dep": "/dep-scoped.js", "bad": null}
		  }
		}</script>
		<script type="module">
		  import { v } from '/scoped/a.js';
		  import { v as top } from 'dep';
		  document.getElementById('out').textContent = 'scoped=' + v + ' top=' + top;
		</script></body></html>`, map[string]string{
		origin + "/scoped/a.js":   `export { v } from 'dep';`,
		origin + "/dep-top.js":    `export const v = 'TOP';`,
		origin + "/dep-scoped.js": `export const v = 'SCOPED';`,
	})
	if got != "scoped=SCOPED top=TOP" {
		t.Errorf("one invalid import-map entry broke the rest of the map: got %q", got)
	}
}
