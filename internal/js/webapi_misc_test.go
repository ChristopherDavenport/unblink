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

// renderWithEngine renders pageHTML on a caller-configured engine (for options
// like WithWebdriver) and returns the serialized document.
func renderWithEngine(t *testing.T, eng *js.Engine, pageHTML string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String()
}

// TestNavigatorFields proves the Phase 21 navigator fill-in, including the
// honest webdriver=true default.
func TestNavigatorFields(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  navigator.sendBeacon('/analytics', 'payload');
		  document.getElementById('out').textContent = [
		    'webdriver=' + navigator.webdriver,
		    'online=' + navigator.onLine,
		    'hc=' + navigator.hardwareConcurrency,
		    'touch=' + navigator.maxTouchPoints,
		    'vendor=[' + navigator.vendor + ']',
		    'beacon=' + navigator.sendBeacon('/x'),
		    'uad=' + (navigator.userAgentData && navigator.userAgentData.mobile === false),
		    'brands=' + navigator.userAgentData.brands.length
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{"webdriver=true", "online=true", "hc=4", "touch=0", "vendor=[]", "beacon=true", "uad=true", "brands=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("navigator missing %q:\n%s", want, out)
		}
	}
}

// TestNavigatorWebdriverMimic proves WithWebdriver(false) — the --tls-mimic
// posture — flips the flag, and only then.
func TestNavigatorWebdriverMimic(t *testing.T) {
	eng := js.New(js.WithTimeout(3*time.Second), js.WithWebdriver(false))
	defer eng.Close()
	out := renderWithEngine(t, eng, `<html><body><div id="out"></div>
		<script>document.getElementById('out').textContent = 'wd=' + navigator.webdriver;</script>
		</body></html>`)
	if !strings.Contains(out, "wd=false") {
		t.Errorf("WithWebdriver(false) did not flip navigator.webdriver:\n%s", out)
	}
}

// TestDialogsAndWindowShell proves the dialog no-ops (confirm auto-accepts,
// prompt dismisses) and the single-window shell (top/parent/frames self-refs,
// popup-blocked window.open).
func TestDialogsAndWindowShell(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  alert('ignored');
		  document.getElementById('out').textContent = [
		    'confirm=' + confirm('proceed?'),
		    'prompt=' + (prompt('name?') === null),
		    'open=' + (window.open('https://example.com/popup') === null),
		    'top=' + (window.top === window && window.parent === window && window.frames === window),
		    'opener=' + (window.opener === null),
		    'frameEl=' + (window.frameElement === null),
		    'closed=' + window.closed
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{"confirm=true", "prompt=true", "open=true", "top=true", "opener=true", "frameEl=true", "closed=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("window shell missing %q:\n%s", want, out)
		}
	}
}

// TestWeakRefShim proves the strong-ref WeakRef (deref always returns the
// target — spec-legal) and inert FinalizationRegistry don't throw where modern
// bundles construct them at module scope.
func TestWeakRefShim(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var target = { v: 42 };
		  var ref = new WeakRef(target);
		  var threw = false;
		  try { new WeakRef(7); } catch (e) { threw = true; }
		  var reg = new FinalizationRegistry(function () {});
		  reg.register(target, 'held', target);
		  var unreg = reg.unregister(target) === false;
		  document.getElementById('out').textContent =
		    'deref=' + (ref.deref() === target) + ' threw=' + threw + ' unreg=' + unreg;
		</script></body></html>`)
	for _, want := range []string{"deref=true", "threw=true", "unreg=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("WeakRef shim missing %q:\n%s", want, out)
		}
	}
}

// TestIntlShim proves the crash-avoidance Intl produces readable en-US-flavored
// output for the constructors i18n bundles build eagerly.
func TestIntlShim(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var nf = new Intl.NumberFormat('en-US');
		  var cur = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' });
		  var pct = new Intl.NumberFormat('en-US', { style: 'percent' });
		  var dt = new Intl.DateTimeFormat('en-US', { dateStyle: 'medium' });
		  var pr = new Intl.PluralRules('en-US');
		  var col = new Intl.Collator('en-US');
		  var rtf = new Intl.RelativeTimeFormat('en-US');
		  document.getElementById('out').textContent = [
		    'n=' + nf.format(1234567.891),
		    'c=' + cur.format(-42.5),
		    'p=' + pct.format(0.375),
		    'd=' + dt.format(new Date(2026, 6, 2)),
		    'pl=' + pr.select(1) + '/' + pr.select(3),
		    'co=' + col.compare('a', 'b') + col.compare('b', 'a') + col.compare('a', 'a'),
		    'r=' + rtf.format(-3, 'day') + '|' + rtf.format(1, 'hour'),
		    'loc=' + Intl.getCanonicalLocales('en-US').join(',')
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{
		"n=1,234,567.891", "c=-$42.50", "p=38%", "d=Jul 2, 2026",
		"pl=one/other", "co=-110", "r=3 days ago|in 1 hour", "loc=en-US",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Intl shim missing %q:\n%s", want, out)
		}
	}
}

// TestConstructableStylesheets proves the Lit adoption path: feature-detect
// passes, sheets construct and accept CSS, adoption is accepted-and-ignored.
func TestConstructableStylesheets(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  // Lit's exact feature check for constructable-stylesheet support:
		  var supportsAdopting = 'adoptedStyleSheets' in Document.prototype && 'replace' in CSSStyleSheet.prototype;
		  var sheet = new CSSStyleSheet();
		  sheet.replaceSync(':host { color: red; }');
		  var adopted = false;
		  sheet.replace('p { margin: 0; }').then(function (s) {
		    document.adoptedStyleSheets = [sheet];
		    adopted = document.adoptedStyleSheets.length === 1 && s === sheet;
		    document.getElementById('out').textContent =
		      'detect=' + supportsAdopting + ' adopted=' + adopted +
		      ' sheets=' + document.styleSheets.length + ' rule=' + sheet.insertRule('a{}');
		  });
		</script></body></html>`)
	for _, want := range []string{"detect=true", "adopted=true", "sheets=0", "rule=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("constructable stylesheets missing %q:\n%s", want, out)
		}
	}
}

// TestAPIsInLiveContext spot-checks that the prelude_api layer is installed in
// the persistent (interact/session) runtime too, not just one-shot Render.
func TestAPIsInLiveContext(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body><div id="out">?</div>
		<script>
		  document.getElementById('out').textContent =
		    'b64=' + atob(btoa('live')) +
		    ' mm=' + matchMedia('(min-width: 768px)').matches +
		    ' perf=' + (performance.now() >= 0) +
		    ' intl=' + new Intl.NumberFormat().format(1000);
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	snap := snapshot(t, lc)
	for _, want := range []string{"b64=live", "mm=true", "perf=true", "intl=1,000"} {
		if !strings.Contains(snap, want) {
			t.Errorf("live context missing %q:\n%s", want, snap)
		}
	}
}
