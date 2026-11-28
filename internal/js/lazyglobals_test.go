package js_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

// Sections of the prelude that most pages never touch (Intl, IndexedDB,
// crypto.subtle, DOMMatrix, ElementInternals) are built on first use. These
// hold the properties page code can observe about that: the global exists
// before it is built, building it yields the same object every time, a page's
// own assignment wins, and the things built alongside it appear with it.
func TestLazyPreludeSections(t *testing.T) {
	out := renderWithEnv(t, `<html><body><div id="out"></div><script>
	  var r = [];
	  function put(k, v) { r.push(k + '=' + v); }
	  put('in', ('Intl' in window) + '/' + ('indexedDB' in window) + '/' + ('DOMMatrix' in window) + '/' + ('subtle' in crypto) + '/' + ('ElementInternals' in window));
	  put('typeof', typeof Intl + '/' + typeof indexedDB + '/' + typeof DOMMatrix + '/' + typeof crypto.subtle + '/' + typeof ElementInternals);
	  put('same', (Intl === window.Intl) + '/' + (indexedDB === window.indexedDB) + '/' + (crypto.subtle === crypto.subtle));
	  var d = Object.getOwnPropertyDescriptor(window, 'Intl');
	  put('data', ('value' in d) + '/' + d.writable + '/' + d.configurable);
	  put('nf', new Intl.NumberFormat('en-US').format(1234567.5));
	  put('tls', (1234.5).toLocaleString('en-US'));
	  put('idb', typeof indexedDB.open + '/' + typeof IDBKeyRange.bound + '/' + (typeof IDBRequest) + '/' + (indexedDB.open('x') instanceof IDBOpenDBRequest));
	  put('matrix', new DOMMatrix().a + '/' + (new DOMMatrix() instanceof DOMMatrixReadOnly) + '/' + (WebKitCSSMatrix === DOMMatrix));
	  put('aria', ('ariaLabel' in ElementInternals.prototype) + '/' + ('role' in ElementInternals.prototype));
	  crypto.subtle.digest('SHA-256', new Uint8Array([1, 2, 3])).then(function (h) {
	    put('digest', h.byteLength);
	    document.getElementById('out').textContent = r.join(' | ');
	  });
	</script></body></html>`, js.Env{})
	for _, want := range []string{
		"in=true/true/true/true/true",
		"typeof=object/object/function/object/function",
		"same=true/true/true",
		"data=true/true/true",
		"nf=1,234,567.5",
		"tls=1,234.5",
		"idb=function/function/function/true",
		"matrix=1/true/true",
		"aria=true/true",
		"digest=32",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A page that defines its own value before reading the lazy one keeps it, and
// the built-in never overwrites it afterwards.
func TestLazyPreludeSectionYieldsToPageAssignment(t *testing.T) {
	out := renderWithEnv(t, `<html><body><div id="out"></div><script>
	  window.Intl = { mine: true };
	  var before = Intl.mine === true;
	  var n = (5).toLocaleString === Number.prototype.toLocaleString;
	  window.indexedDB = 'page';
	  document.getElementById('out').textContent = before + '/' + (window.Intl.mine === true) + '/' + n + '/' + window.indexedDB + '/' + typeof IDBKeyRange;
	</script></body></html>`, js.Env{})
	if !strings.Contains(out, ">true/true/true/page/object<") {
		t.Errorf("page assignment did not win over the lazy section:\n%s", out)
	}
}

// An element with attachInternals() works without the page ever naming the
// ElementInternals global: the ARIA accessors are filled on that first call.
func TestLazyElementInternalsAriaViaAttachInternals(t *testing.T) {
	out := renderWithEnv(t, `<html><body><x-f></x-f><div id="out"></div><script>
	  class F extends HTMLElement {
	    static formAssociated = true;
	    constructor() { super(); this.i = this.attachInternals(); this.i.ariaLabel = 'named'; this.i.role = 'switch'; }
	  }
	  customElements.define('x-f', F);
	  var el = document.querySelector('x-f');
	  document.getElementById('out').textContent = el.i.ariaLabel + '/' + el.i.role;
	</script></body></html>`, js.Env{})
	if !strings.Contains(out, ">named/switch<") {
		t.Errorf("ElementInternals ARIA accessors missing after attachInternals():\n%s", out)
	}
}
