package js_test

import (
	"testing"
)

// TestSOPDocumentIsolationStubs locks the cross-document half of the Same-Origin
// Policy (ADR 0015), which unblink calls "moot by architecture": single document,
// single window, one runtime per render — no reachable foreign document. That claim
// was asserted by no test. Here a page with an <iframe> checks that every
// cross-document handle a page could reach is inert: window.opener is null,
// window.parent/top are the window itself (no parent frame to escape to),
// window.name is fresh/empty, there are no reachable child frames, and the iframe
// exposes no contentWindow — so a page's own JS cannot read across an origin
// boundary that does not exist.
func TestSOPDocumentIsolationStubs(t *testing.T) {
	page := `<html><body><div id="out">pending</div>
		<iframe id="f" src="https://cross-origin.example/secret"></iframe>
		<script>
		  var f = document.getElementById('f');
		  var isolated =
		    (window.opener == null) &&
		    (window.parent === window) &&
		    (window.top === window) &&
		    (window.name === '') &&
		    (!window.frames || !window.frames.length) &&
		    (!f || !f.contentWindow);
		  document.getElementById('out').textContent = isolated ? 'isolated' : 'LEAK';
		</script></body></html>`
	doc, _ := renderCSP(t, page, "https://page.example/", nil, nil)
	if got := divText(t, doc, "out"); got != "isolated" {
		t.Errorf("#out = %q, want isolated (no reachable foreign document / opener / parent / frame)", got)
	}
}

// TestSOPWindowNameFreshPerRender confirms window.name does not carry a value from
// the page into a fresh render (a one-shot render begins with an empty name; the
// cross-render reset is the session's concern).
func TestSOPWindowNameFreshPerRender(t *testing.T) {
	page := `<html><body><div id="out"></div><script>
		document.getElementById('out').textContent = 'name=[' + window.name + ']';
	</script></body></html>`
	doc, _ := renderCSP(t, page, "https://page.example/", nil, nil)
	if got := divText(t, doc, "out"); got != "name=[]" {
		t.Errorf("#out = %q, want name=[] (fresh window.name)", got)
	}
}
