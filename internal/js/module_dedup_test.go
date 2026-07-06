package js_test

import (
	"strings"
	"testing"
)

// The module registry must give dynamic import() browser-correct module-map
// semantics: a module imported by several import() graphs is instantiated ONCE and
// its live namespace shared. Without dedup, each import() bundled its own copy, so a
// stateful singleton (React's hooks dispatcher was the field case — "Cannot read
// property 'useMemoCache' of null") was duplicated and cross-chunk communication
// through it silently broke.

func TestModuleDedupAcrossImports(t *testing.T) {
	routes := map[string]string{
		"//cdn.test/singleton.mjs": `export const box = { value: null };`,
		"//cdn.test/writer.mjs":    `import { box } from '/singleton.mjs'; export function set(){ box.value = 'SHARED'; }`,
		"//cdn.test/reader.mjs":    `import { box } from '/singleton.mjs'; export function get(){ return box.value; }`,
	}
	page := `<html><body><div id="out">?</div>
		<script>
		  Promise.all([
		    import('https://cdn.test/writer.mjs'),
		    import('https://cdn.test/reader.mjs')
		  ]).then(function(m){
		    m[0].set();
		    document.getElementById('out').textContent = 'value=' + m[1].get();
		  }).catch(function(e){ document.getElementById('out').textContent = 'ERR ' + e; });
		</script></body></html>`

	out := renderWith(t, page, &stubTransport{routes: routes})
	if !strings.Contains(out, "value=SHARED") {
		t.Errorf("shared module was duplicated across import() calls (writer and reader saw different instances)\n%s", sliceAround(out, `id="out"`))
	}
}

// TestModuleDedupSingletonDispatcher mirrors the exact React shape: a "renderer"
// module writes onto a shared framework module's mutable field, and a separately
// imported "component" module reads it back — as ReactDOM sets the shared hooks
// dispatcher that the component's hooks then read.
func TestModuleDedupSingletonDispatcher(t *testing.T) {
	routes := map[string]string{
		"//cdn.test/framework.mjs": `export const internals = { dispatcher: null };
			export function useThing(){ if(!internals.dispatcher) throw new Error('dispatcher is null'); return internals.dispatcher.value(); }`,
		"//cdn.test/renderer.mjs": `import { internals } from '/framework.mjs';
			export function mount(){ internals.dispatcher = { value: () => 'HOOK-OK' }; }`,
		"//cdn.test/component.mjs": `import { useThing } from '/framework.mjs';
			export function render(){ return useThing(); }`,
	}
	page := `<html><body><div id="out">?</div>
		<script>
		  Promise.all([
		    import('https://cdn.test/renderer.mjs'),
		    import('https://cdn.test/component.mjs')
		  ]).then(function(m){
		    m[0].mount();                 // renderer sets the shared dispatcher
		    document.getElementById('out').textContent = m[1].render(); // component reads it
		  }).catch(function(e){ document.getElementById('out').textContent = 'ERR ' + e; });
		</script></body></html>`

	out := renderWith(t, page, &stubTransport{routes: routes})
	if !strings.Contains(out, "HOOK-OK") {
		t.Errorf("component could not read the singleton the renderer set — framework module was duplicated\n%s", sliceAround(out, `id="out"`))
	}
}

func sliceAround(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return s
	}
	end := i + 80
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}
