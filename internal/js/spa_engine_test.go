package js_test

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// renderAt renders pageHTML with an explicit page URL and env, so tests can probe
// location/base behavior that the fixed-URL render helper can't reach.
func renderAt(t *testing.T, pageURL, pageHTML string, env js.Env) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		t.Fatalf("bad url: %v", err)
	}
	eng := js.New(js.WithTimeout(3 * time.Second))
	if err := eng.Render(context.Background(), doc, base, env); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String()
}

// location reflects the deep-link page URL, not the <base href>. Regression: the
// engine used to receive dom.BaseURL (which resolves <base href="/">) as its base,
// so a client router at /item/123 saw location.pathname="/" and rendered the wrong
// view. The page URL must seed location while <base> only resolves relative URLs.
func TestLocationIsPageURLNotBaseHref(t *testing.T) {
	out := renderAt(t, "https://example.com/item/48753715",
		`<html><head><base href="/"></head><body><div id="out"></div>
		<script>
		  document.getElementById('out').textContent =
		    location.pathname + '|' + location.href;
		</script></body></html>`, js.Env{})
	if !strings.Contains(out, "/item/48753715|https://example.com/item/48753715") {
		t.Errorf("location did not reflect deep-link page URL:\n%s", out)
	}
}

// Relative fetch resolves against <base href>, not the (deeper) page URL — the
// other half of separating the two concepts.
func TestRelativeFetchResolvesAgainstBaseHref(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"https://example.com/api/data": "BASE-REL-OK"}}
	out := renderAt(t, "https://example.com/deep/path/item",
		`<html><head><base href="/"></head><body><div id="out">x</div>
		<script>
		  fetch('api/data').then(function(r){return r.text();})
		    .then(function(t){ document.getElementById('out').textContent = t; });
		</script></body></html>`, js.Env{Transport: tr})
	if !strings.Contains(out, "BASE-REL-OK") {
		t.Errorf("relative fetch did not resolve against <base href>:\n%s", out)
	}
}

// An <a> element parses a URL via its href-relative component accessors — the
// createElement('a') URL-parser trick Angular's getBaseHref and many libs rely on.
func TestAnchorURLComponents(t *testing.T) {
	out := renderAt(t, "https://example.com/dir/page",
		`<html><body><div id="out"></div>
		<script>
		  var a = document.createElement('a');
		  a.setAttribute('href', '/foo/bar?q=1#frag');
		  document.getElementById('out').textContent =
		    a.pathname + '|' + a.search + '|' + a.hash + '|' + a.host;
		</script></body></html>`, js.Env{})
	if !strings.Contains(out, "/foo/bar|?q=1|#frag|example.com") {
		t.Errorf("anchor URL components wrong:\n%s", out)
	}
}

// document.implementation.createHTMLDocument yields an inert, disconnected tree
// whose innerHTML/querySelector work — the DomSanitizer path behind every Angular
// [innerHTML] binding. Without it, sanitizing threw and the binding silently
// dropped its content.
func TestCreateHTMLDocumentInertParse(t *testing.T) {
	out := render(t, `<html><body><div id="out">x</div>
		<script>
		  var d = document.implementation.createHTMLDocument('inert');
		  d.body.innerHTML = '<p>hello <b>world</b></p><script>evil()<\/script>';
		  var p = d.querySelector('p');
		  document.getElementById('out').textContent =
		    (d.body ? 'hasbody:' : 'nobody:') + p.textContent;
		</script></body></html>`)
	if !strings.Contains(out, "hasbody:hello world") {
		t.Errorf("createHTMLDocument inert parse failed:\n%s", out)
	}
}

// The inert document is disconnected: mutating it must not touch the live page.
// The live #live node is probed after the inert manipulation and its state written
// to a data attribute (checking the serialized string for the inert marker would
// false-positive on the inline script's own source text).
func TestCreateHTMLDocumentIsDetached(t *testing.T) {
	out := render(t, `<html><body><div id="live">LIVE</div>
		<script>
		  var d = document.implementation.createHTMLDocument('inert');
		  d.body.innerHTML = '<div id="live">' + 'IN' + 'ERT</div>';
		  var live = document.getElementById('live');
		  live.setAttribute('data-r',
		    live.textContent + ':' + document.querySelectorAll('#live').length);
		</script></body></html>`)
	if !strings.Contains(out, `data-r="LIVE:1"`) {
		t.Errorf("inert document leaked into live tree:\n%s", out)
	}
}

// Node exposes its numeric constants on both the constructor and the prototype.
func TestNodeConstants(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var el = document.getElementById('out');
		  document.getElementById('out').textContent =
		    Node.ELEMENT_NODE + ',' + Node.TEXT_NODE + ',' + Node.DOCUMENT_NODE +
		    ',' + el.ELEMENT_NODE + ',' + Node.DOCUMENT_POSITION_CONTAINED_BY;
		</script></body></html>`)
	if !strings.Contains(out, ">1,3,9,1,16<") {
		t.Errorf("Node constants wrong:\n%s", out)
	}
}

// compareDocumentPosition reports containment and document order.
func TestCompareDocumentPosition(t *testing.T) {
	out := render(t, `<html><body><div id="parent"><span id="child"></span></div><em id="after"></em>
		<script>
		  var p = document.getElementById('parent');
		  var c = document.getElementById('child');
		  var a = document.getElementById('after');
		  var contained = (p.compareDocumentPosition(c) & 16) !== 0;   // CONTAINED_BY
		  var following = (p.compareDocumentPosition(a) & 4) !== 0;    // FOLLOWING
		  document.getElementById('parent').setAttribute('data-r', contained + ',' + following);
		</script></body></html>`)
	if !strings.Contains(out, `data-r="true,true"`) {
		t.Errorf("compareDocumentPosition wrong:\n%s", out)
	}
}

// HTMLMediaElement (and its audio/video subclasses) exist so zone.js's unguarded
// property-descriptor patch doesn't ReferenceError during Angular bootstrap.
func TestMediaElementConstructors(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var ok = (typeof HTMLMediaElement === 'function') &&
		    (HTMLAudioElement.prototype instanceof HTMLMediaElement) &&
		    (HTMLVideoElement.prototype instanceof HTMLMediaElement) &&
		    (HTMLMediaElement.prototype instanceof HTMLElement);
		  document.getElementById('out').textContent = ok ? 'OK' : 'FAIL';
		</script></body></html>`)
	if !strings.Contains(out, ">OK<") {
		t.Errorf("media element constructor chain wrong:\n%s", out)
	}
}

// setTimeout returns a plain numeric id (goja hands out a host Timer object). Code
// that stamps an expando onto the handle (zone.js) or compares ids with === needs a
// number, not a host object.
func TestSetTimeoutReturnsNumericID(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var id = setTimeout(function(){}, 0);
		  var ok = (typeof id === 'number');
		  try { id.__zone_symbol__ = 1; } catch (e) { ok = false; }  // host object would throw
		  clearTimeout(id);
		  document.getElementById('out').textContent = ok ? 'NUM' : ('BAD:' + typeof id);
		</script></body></html>`)
	if !strings.Contains(out, ">NUM<") {
		t.Errorf("setTimeout id not a plain number:\n%s", out)
	}
}

// A nomodule classic script never runs: the engine always executes the module
// set, so on a differential-loading build (paired -es2015/-es5 bundles) running
// the nomodule half too would boot the app twice.
func TestNomoduleScriptSkipped(t *testing.T) {
	out := render(t, `<html><body><div id="out">start</div>
		<script nomodule>document.getElementById('out').textContent = 'legacy';</script>
		<script>document.getElementById('out').setAttribute('data-ran', 'yes');</script>
		</body></html>`)
	if strings.Contains(out, ">legacy<") {
		t.Errorf("nomodule script should not have run:\n%s", out)
	}
	if !strings.Contains(out, `data-ran="yes"`) {
		t.Errorf("ordinary classic script did not run (harness sanity):\n%s", out)
	}
}

// console.error surfaces in render diagnostics (frameworks report fatal boot
// errors through it); the other console methods stay silent.
func TestConsoleErrorFeedsDiagnostics(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(
		`<html><body><script>console.log('quiet'); console.error('boom-message');</script></body></html>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/")
	var diag js.RenderResult
	eng := js.New(js.WithTimeout(2 * time.Second))
	if err := eng.Render(context.Background(), doc, base, js.Env{Diag: &diag}); err != nil {
		t.Fatalf("render: %v", err)
	}
	found := false
	for _, e := range diag.Errors {
		if strings.Contains(e, "boom-message") {
			found = true
		}
		if strings.Contains(e, "quiet") {
			t.Errorf("console.log leaked into diagnostics: %q", e)
		}
	}
	if !found {
		t.Errorf("console.error not in diagnostics: %v", diag.Errors)
	}
}
