package js_test

import (
	"strings"
	"testing"
)

func TestModuleInlineRelativeImport(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/m.js": "export const msg = 'MODULE-OK';",
	}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script type="module">
		  import { msg } from './m.js';
		  document.getElementById('out').textContent = msg;
		</script></body></html>`, tr)
	if !strings.Contains(out, "MODULE-OK") {
		t.Errorf("inline module with relative import did not render:\n%s", out)
	}
}

func TestModuleExternalSrc(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/app.mjs": "import { v } from './dep.js'; document.getElementById('out').textContent = v;",
		"/dep.js":  "export const v = 'EXT-MODULE';",
	}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script type="module" src="/app.mjs"></script></body></html>`, tr)
	if !strings.Contains(out, "EXT-MODULE") {
		t.Errorf("external module + its import did not render:\n%s", out)
	}
}

func TestModuleImportMap(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/lib.js": "export const greet = 'IMPORTMAP-OK';",
	}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script type="importmap">{"imports":{"lib":"/lib.js"}}</script>
		<script type="module">
		  import { greet } from 'lib';
		  document.getElementById('out').textContent = greet;
		</script></body></html>`, tr)
	if !strings.Contains(out, "IMPORTMAP-OK") {
		t.Errorf("bare specifier via import map did not resolve:\n%s", out)
	}
}

func TestModuleDynamicImport(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/d.js": "export const value = 'DYNAMIC-OK';",
	}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script type="module">
		  import('./d.js').then(function (m) { document.getElementById('out').textContent = m.value; });
		</script></body></html>`, tr)
	if !strings.Contains(out, "DYNAMIC-OK") {
		t.Errorf("dynamic import() did not settle:\n%s", out)
	}
}

func TestModuleNoNetworkSkipped(t *testing.T) {
	// No transport: module scripts are skipped (never compiled as classic, which
	// would be a syntax error), the page is left intact, no panic.
	out := renderWith(t, `<html><body><div id="out">placeholder</div>
		<script type="module">
		  import { msg } from './m.js';
		  document.getElementById('out').textContent = msg;
		</script></body></html>`, nil)
	if !strings.Contains(out, "placeholder") {
		t.Errorf("module without network should leave the page intact:\n%s", out)
	}
}

func TestModuleUnresolvedBareSkipped(t *testing.T) {
	// A bare specifier with no import map entry fails the build → module skipped,
	// no panic, placeholder intact.
	tr := &stubTransport{routes: map[string]string{}}
	out := renderWith(t, `<html><body><div id="out">placeholder</div>
		<script type="module">
		  import x from 'nonexistent-bare';
		  document.getElementById('out').textContent = x;
		</script></body></html>`, tr)
	// The div text unchanged proves the (failed) module never ran. (Inspecting the
	// element specifically, since the script's own source is serialized too.)
	if !strings.Contains(out, `<div id="out">placeholder</div>`) {
		t.Errorf("unresolved bare specifier should skip the module:\n%s", out)
	}
}
