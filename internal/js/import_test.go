package js_test

import (
	"strings"
	"testing"
	"time"
)

// These tests cover the classic-script dynamic import() fallback. goja has no
// ImportCall node, so a literal import() in a classic <script> is a hard parse error;
// runScripts lowers it via esbuild (lowerDynamicImport) and retries. The lowered
// require() hits the disabled loader and REJECTS, so we assert only that (i)
// synchronous code ran and (ii) the import rejected gracefully — never that a chunk
// actually loaded (the lowering neither resolves nor bundles it).

// (a) A classic script with a string-literal import() compiles and its synchronous code
// runs. Pre-fix the whole bundle fails to compile and "before" survives.
func TestClassicDynamicImportStringLiteralRuns(t *testing.T) {
	out := render(t, `<html><body><div id="out">before</div>
		<script>
		  document.getElementById("out").textContent = "sync-ran";
		  import('./chunk.js').then(function () {}, function () {});
		</script></body></html>`)
	if !strings.Contains(out, "sync-ran") || strings.Contains(out, "before") {
		t.Errorf("classic string-literal import() did not run synchronously:\n%s", out)
	}
}

// (b) The mobalytics shape: `await import(l)` with a runtime-variable specifier inside
// an async IIFE with try/catch. Sync code runs, the lowered require() throws, the await
// rejects, and the catch writes "caught".
func TestClassicDynamicImportRuntimeVarRejectsGracefully(t *testing.T) {
	out := render(t, `<html><body><div id="out">before</div>
		<script>
		  (async function () {
		    var l = './chunk-' + 1 + '.js';
		    try { await import(l); document.getElementById("out").textContent = "loaded"; }
		    catch (e) { document.getElementById("out").textContent = "caught"; }
		  })();
		</script></body></html>`)
	// Match the rendered <div> text, not the "caught"/"loaded" literals in the script source.
	if !strings.Contains(out, `id="out">caught<`) {
		t.Errorf("runtime-variable import() did not reject into catch:\n%s", out)
	}
	if strings.Contains(out, `id="out">loaded<`) {
		t.Errorf("import() unexpectedly resolved:\n%s", out)
	}
}

// (c) The fix rides the shared runScripts, so it also covers the persistent live
// (interact/session) runtime opened via context.go.
func TestClassicDynamicImportInLiveContext(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body><div id="out">before</div>
		<script>
		  (async function () {
		    var l = './chunk-' + 2 + '.js';
		    try { await import(l); } catch (e) { document.getElementById("out").textContent = "caught"; }
		  })();
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	if out := snapshot(t, lc); !strings.Contains(out, `id="out">caught<`) {
		t.Errorf("live-context import() did not reject into catch:\n%s", out)
	}
}

// (d) A classic script containing BOTH #private fields AND a dynamic import() forces
// goja's first compile to fail (→ fallback runs the transformed output), yet private
// fields must still work — proving Target ESNext did not lower them to goja's WeakMap.
func TestClassicDynamicImportKeepsPrivateFieldsNative(t *testing.T) {
	out := render(t, `<html><body><div id="out">before</div>
		<script>
		  class Counter { #n = 0; bump() { this.#n += 7; return this.#n; } }
		  var l = './lazy' + '.js';
		  (async function () { try { await import(l); } catch (e) {} })();
		  document.getElementById("out").textContent = "ok-" + new Counter().bump();
		</script></body></html>`)
	if !strings.Contains(out, "ok-7") || strings.Contains(out, "before") {
		t.Errorf("private fields corrupted by the import() fallback:\n%s", out)
	}
}

// (e) An unhandled dynamic-import rejection is non-fatal: the render succeeds and the
// rejection surfaces as a best-effort diagnostic rather than a thrown error.
func TestClassicDynamicImportUnhandledRejectionIsDiagnostic(t *testing.T) {
	diag := renderDiag(t, `<html><body><div id="out">x</div>
		<script>import('./chunk.js');</script></body></html>`)
	if !strings.Contains(strings.Join(diag.Errors, "\n"), "unhandled rejection") {
		t.Errorf("expected an unhandled-rejection diagnostic, got %v", diag.Errors)
	}
}

// --- Phase B2: the dynamic import() loader actually fetches, bundles, and runs the
// chunk (through the same guarded transport + modulePlugin as runModules), so the
// resolved namespace exposes the chunk's real default and named exports. ---

// B2 string-literal: `await import('./chunk.js')` resolves with the chunk namespace.
func TestClassicDynamicImportLoadsChunkNamespace(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/chunk.js": "export default 'LAZY-DEFAULT'; export const named = 'LAZY-NAMED';",
	}}
	out := renderWith(t, `<html><body><div id="out">before</div>
		<script>
		  (async function () {
		    var m = await import('./chunk.js');
		    document.getElementById("out").textContent = m.default + '|' + m.named;
		  })();
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">LAZY-DEFAULT|LAZY-NAMED<`) {
		t.Errorf("dynamic import() did not load the chunk namespace:\n%s", out)
	}
}

// B2 mobalytics shape: `(await import(l)).default` with a runtime-variable specifier
// resolves the chunk's default export (here a lazy component function).
func TestClassicDynamicImportRuntimeVarLoadsDefault(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/lazy.js": "export default function () { return 'MOUNTED'; }",
	}}
	out := renderWith(t, `<html><body><div id="out">before</div>
		<script>
		  (async function () {
		    var l = './lazy' + '.js';
		    var Comp = (await import(l)).default;
		    document.getElementById("out").textContent = Comp();
		  })();
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">MOUNTED<`) {
		t.Errorf("runtime-variable import() did not resolve the chunk default:\n%s", out)
	}
}

// B2 named-only chunk: no default export → the namespace has the named export and an
// undefined default (the surrounding __toESM() must not wrap it in a fake default).
func TestClassicDynamicImportNamedOnlyChunk(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/util.js": "export const value = 'NAMED-ONLY';",
	}}
	out := renderWith(t, `<html><body><div id="out">before</div>
		<script>
		  import('./util.js').then(function (m) {
		    document.getElementById("out").textContent = m.value + ':' + (m.default === undefined);
		  });
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">NAMED-ONLY:true<`) {
		t.Errorf("named-only chunk import failed:\n%s", out)
	}
}

// B2 in the live (interact/session) runtime — the path that motivated this: a dynamic
// import resolves the chunk namespace inside a persistent context, so lazy content lands
// where interact can act on it.
func TestClassicDynamicImportLoadsChunkInLiveContext(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/lazy.js": "export default 'LIVE-LAZY';",
	}}
	lc, cleanup := openContextWith(t, `<html><body><div id="out">before</div>
		<script>
		  (async function () {
		    var m = await import('./lazy.js');
		    document.getElementById('out').textContent = m.default;
		  })();
		</script></body></html>`, 2*time.Second, tr)
	defer cleanup()
	if out := snapshot(t, lc); !strings.Contains(out, `id="out">LIVE-LAZY<`) {
		t.Errorf("live-context dynamic import did not load chunk:\n%s", out)
	}
}

// B2 transitive graph: the chunk statically imports a dependency, which the loader
// bundles in — proving the whole chunk graph (not just the entry) is fetched.
func TestClassicDynamicImportBundlesChunkGraph(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/main.js": "import { dep } from './dep.js'; export default 'main+' + dep;",
		"/dep.js":  "export const dep = 'DEP';",
	}}
	out := renderWith(t, `<html><body><div id="out">before</div>
		<script>
		  import('./main.js').then(function (m) {
		    document.getElementById("out").textContent = m.default;
		  });
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">main+DEP<`) {
		t.Errorf("transitive chunk graph not bundled:\n%s", out)
	}
}
