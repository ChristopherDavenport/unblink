package js_test

import (
	"strings"
	"testing"
)

// Phase B1: a dynamically created <script src> (createElement + appendChild — the webpack
// "JSONP" chunk mechanism) is fetched, executed, and its load/error handler fired.

// onload property handler (the shape webpack uses most).
func TestInsertedScriptOnloadProperty(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/chunk.js": "window.__CHUNK_VALUE = 'JSONP-LOADED';",
	}}
	out := renderWith(t, `<html><head></head><body><div id="out">before</div>
		<script>
		  var s = document.createElement('script');
		  s.src = '/chunk.js';
		  s.onload = function () { document.getElementById('out').textContent = window.__CHUNK_VALUE; };
		  document.head.appendChild(s);
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">JSONP-LOADED<`) {
		t.Errorf("inserted <script src> onload did not run:\n%s", out)
	}
}

// addEventListener('load', ...) path.
func TestInsertedScriptLoadListener(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/chunk.js": "window.__V = 'ADDEL';",
	}}
	out := renderWith(t, `<html><head></head><body><div id="out">before</div>
		<script>
		  var s = document.createElement('script');
		  s.src = '/chunk.js';
		  s.addEventListener('load', function () { document.getElementById('out').textContent = window.__V; });
		  document.body.appendChild(s);
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">ADDEL<`) {
		t.Errorf("inserted <script src> load listener did not run:\n%s", out)
	}
}

// A failed (404) chunk fires onerror, not onload.
func TestInsertedScriptErrorOn404(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{}} // every path 404s
	out := renderWith(t, `<html><head></head><body><div id="out">before</div>
		<script>
		  var s = document.createElement('script');
		  s.src = '/missing.js';
		  s.onload = function () { document.getElementById('out').textContent = 'LOADED'; };
		  s.onerror = function () { document.getElementById('out').textContent = 'ERRORED'; };
		  document.head.appendChild(s);
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">ERRORED<`) {
		t.Errorf("inserted <script src> onerror did not fire on 404:\n%s", out)
	}
}

// A <script src> that itself contains dynamic import() still runs (Phase A/B fallback is
// shared via compileAndRun): the chunk's synchronous code executes and onload fires.
func TestInsertedScriptWithDynamicImportRuns(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/entry.js": "window.__M = 'ENTRY-RAN'; var l = './x.js'; import(l).catch(function(){});",
	}}
	out := renderWith(t, `<html><head></head><body><div id="out">before</div>
		<script>
		  var s = document.createElement('script');
		  s.src = '/entry.js';
		  s.onload = function () { document.getElementById('out').textContent = window.__M; };
		  document.head.appendChild(s);
		</script></body></html>`, tr)
	if !strings.Contains(out, `id="out">ENTRY-RAN<`) {
		t.Errorf("inserted entry chunk with import() did not run:\n%s", out)
	}
}

// Inline scripts arriving via innerHTML must NOT execute (browser security semantics):
// only directly-inserted external scripts load.
func TestInsertedInlineScriptDoesNotRun(t *testing.T) {
	out := render(t, `<html><body><div id="out">before</div>
		<script>
		  var d = document.createElement('div');
		  d.innerHTML = '<script>window.__ranInline = true;<\/script>';
		  document.body.appendChild(d);
		  document.getElementById('out').textContent = window.__ranInline ? 'RAN' : 'safe';
		</script></body></html>`)
	// Check the rendered <div>, not the "RAN"/"safe" literals in the script source.
	if !strings.Contains(out, `id="out">safe<`) || strings.Contains(out, `id="out">RAN<`) {
		t.Errorf("inline inserted script executed (should not):\n%s", out)
	}
}
