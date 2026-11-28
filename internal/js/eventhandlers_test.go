package js_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// handlerOut renders page and returns the text of #out.
func handlerOut(t *testing.T, page string) string {
	t.Helper()
	doc, _ := renderCSP(t, page, "https://example.com/p", nil, nil)
	return divText(t, doc, "out")
}

func TestHandlerPropertyOnElement(t *testing.T) {
	got := handlerOut(t, `<html><body><button id="b">x</button><div id="out"></div><script>
		var b = document.getElementById('b'), r = [];
		r.push(b.onclick === null);
		function h(e) { r.push(this === b, e.type, e.currentTarget === b); }
		b.onclick = h;
		r.push(b.onclick === h);
		b.click();
		b.onclick = null;
		b.click();
		b.onclick = 5; r.push(b.onclick === null);
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	if got != "true,true,true,click,true,true" {
		t.Errorf("#out = %q", got)
	}
}

func TestHandlerLifecycleOnWindowAndDocument(t *testing.T) {
	got := handlerOut(t, `<html><body><div id="out"></div><script>
		var r = [];
		window.onload = function (e) { r.push('load:' + (this === window) + ':' + e.type);
			document.getElementById('out').textContent = r.join(','); };
		document.onreadystatechange = function () { r.push('rs:' + document.readyState); };
		onhashchange = function () {};  // a bare global assignment reaches the accessor
		r.push('bare:' + (typeof window.onhashchange));
	</script></body></html>`)
	if got != "bare:function,rs:interactive,rs:complete,load:true:load" {
		t.Errorf("#out = %q", got)
	}
}

func TestHandlerContentAttribute(t *testing.T) {
	got := handlerOut(t, `<html><body>
		<form id="f"><input name="who" value="form-scope">
		  <button type="button" id="b" data-k="el-scope" onclick="r.push(this.id, event.type, dataset.k, formMarker, typeof getElementById)">x</button></form>
		<div id="out"></div><script>
		var r = [], b = document.getElementById('b');
		document.getElementById('f').formMarker = 'form-scope';
		b.click();
		r.push(typeof b.onclick, b.onclick.length);
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	// this = the element; scope chain = element (dataset), form owner (who), document.
	if got != "b,click,el-scope,form-scope,function,function,1" {
		t.Errorf("#out = %q", got)
	}
}

func TestHandlerReturnFalseCancels(t *testing.T) {
	got := handlerOut(t, `<html><body>
		<a id="a" href="/elsewhere" onclick="return false">x</a>
		<a id="k" href="#k" onclick="return true">y</a>
		<div id="out"></div><script>
		var r = [];
		function fire(id) {
			var ev = new Event('click', { bubbles: true, cancelable: true });
			return document.getElementById(id).dispatchEvent(ev);
		}
		r.push(fire('a'), fire('k'));
		var btn = document.createElement('button');
		btn.onclick = function () { return false; };
		document.body.appendChild(btn);
		var ev = new Event('click', { bubbles: true, cancelable: true });
		btn.dispatchEvent(ev); r.push(ev.defaultPrevented);
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	if got != "false,true,true" {
		t.Errorf("#out = %q", got)
	}
}

// A handler keeps the position it was first registered at: markup handlers come
// before script-added listeners, a property set later comes after them, and
// re-assigning never moves it.
func TestHandlerOrderAmongListeners(t *testing.T) {
	got := handlerOut(t, `<html><body>
		<button id="m" onclick="r.push('attr')">m</button><button id="p">p</button>
		<div id="out"></div><script>
		var r = [], m = document.getElementById('m'), p = document.getElementById('p');
		m.addEventListener('click', function () { r.push('L1'); });
		m.click(); r.push('|');
		p.addEventListener('click', function () { r.push('L1'); });
		p.onclick = function () { r.push('prop'); };
		p.addEventListener('click', function () { r.push('L2'); });
		p.click(); r.push('|');
		p.onclick = function () { r.push('prop2'); };
		p.click(); r.push('|');
		m.setAttribute('onclick', "r.push('attr2')");
		m.click(); r.push('|');
		m.removeAttribute('onclick');
		m.click();
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	want := "attr,L1,|,L1,prop,L2,|,L1,prop2,L2,|,attr2,L1,|,L1"
	if got != want {
		t.Errorf("#out = %q, want %q", got, want)
	}
}

func TestHandlerBodyForwardsToWindow(t *testing.T) {
	got := handlerOut(t, `<html><body onload="r.push('body-attr:' + (this === window))">
		<div id="out"></div><script>
		var r = [];
		r.push(typeof window.onload, document.body.onload === window.onload);
		document.body.onhashchange = function () { r.push('hash'); };
		r.push(window.onhashchange === document.body.onhashchange);
		// An ordinary element's load handler is its own, not the window's.
		var d = document.createElement('div'); d.onload = function () {};
		r.push(window.onload === d.onload);
		location.assign('#x');
		window.addEventListener('load', function () {
			document.getElementById('out').textContent = r.join(',');
		});
	</script></body></html>`)
	if got != "function,true,true,false,hash,body-attr:true" {
		t.Errorf("#out = %q", got)
	}
}

// script.onload / onerror are plain handlers now: they must fire exactly once.
func TestHandlerScriptLoadFiresOnce(t *testing.T) {
	tr := &cspRecordTransport{bodies: map[string]string{"ok.js": "window.loaded = 1;"}}
	doc, _ := renderCSP(t, `<html><body><div id="out"></div><script>
		var n = 0, s = document.createElement('script');
		s.onload = function (e) { n++; document.getElementById('out').textContent = n + ':' + e.type + ':' + (this === s) + ':' + window.loaded; };
		s.src = '/ok.js';
		document.head.appendChild(s);
	</script></body></html>`, "https://example.com/p", nil, tr)
	if got := divText(t, doc, "out"); got != "1:load:true:1" {
		t.Errorf("#out = %q", got)
	}
}

func TestOnErrorReceivesUncaughtExceptions(t *testing.T) {
	doc, diag := renderCSP(t, `<html><body><div id="out"></div><script>
		var r = [];
		window.onerror = function (msg, src, line, col, err) {
			r.push([typeof msg === 'string' && msg.indexOf('boom-') >= 0, typeof src, line > 0, typeof col, err instanceof Error ? err.message : err].join('/'));
			document.getElementById('out').textContent = r.join(',');
			return true; // handled: keep it out of the diagnostics
		};
		window.addEventListener('error', function (e) { r.push('L:' + (e instanceof ErrorEvent) + ':' + e.error.message); });
		setTimeout(function () { throw new Error('boom-timer'); }, 0);
		document.addEventListener('DOMContentLoaded', function () { throw new Error('boom-listener'); });
	</script><script>throw new Error('boom-script');</script></body></html>`, "https://example.com/p", nil, nil)
	got := divText(t, doc, "out")
	for _, want := range []string{
		"true/string/true/number/boom-script", "L:true:boom-script",
		"true/string/true/number/boom-listener", "true/string/true/number/boom-timer",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("#out = %q, missing %q", got, want)
		}
	}
	for _, e := range diag.Errors {
		if strings.Contains(e, "boom-") {
			t.Errorf("a handled error reached the diagnostics: %s", e)
		}
	}
}

func TestUncaughtExceptionsReachDiagnostics(t *testing.T) {
	_, diag := renderCSP(t, `<html><body><script>
		setTimeout(function () { throw new Error('boom-timer'); }, 0);
		document.addEventListener('DOMContentLoaded', function () { throw new Error('boom-listener'); });
		window.addEventListener('error', function () {}); // observing is not handling
	</script></body></html>`, "https://example.com/p", nil, nil)
	all := strings.Join(diag.Errors, "\n")
	for _, want := range []string{"boom-timer", "boom-listener"} {
		if !strings.Contains(all, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, all)
		}
	}
}

// An onerror handler that throws must not be re-entered for its own exception.
func TestOnErrorThatThrowsDoesNotRecurse(t *testing.T) {
	doc, diag := renderCSP(t, `<html><body><div id="out">0</div><script>
		var n = 0;
		window.onerror = function () { n++; document.getElementById('out').textContent = String(n); throw new Error('handler-broke'); };
	</script><script>throw new Error('first');</script></body></html>`, "https://example.com/p", nil, nil)
	if got := divText(t, doc, "out"); got != "1" {
		t.Errorf("onerror ran %s times, want 1", got)
	}
	if all := strings.Join(diag.Errors, "\n"); !strings.Contains(all, "handler-broke") || !strings.Contains(all, "first") {
		t.Errorf("both errors should be in the diagnostics:\n%s", all)
	}
}

// A failed script load fires a plain Event named "error" at the element; a
// window.onerror must not be handed that as if it were an exception.
func TestHandlerErrorEventOnElementIsPlain(t *testing.T) {
	got := handlerOut(t, `<html><body><img id="i" onerror="r.push(arguments.length + ':' + event.type)"><div id="out"></div><script>
		var r = [];
		document.getElementById('i').dispatchEvent(new Event('error'));
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	if got != "1:error" {
		t.Errorf("#out = %q", got)
	}
}

func cspHandlerPage() string {
	return `<html><body><button id="b" onclick="document.getElementById('out').textContent = 'ran'">x</button>
		<div id="out">idle</div><script nonce="n1">document.getElementById('b').click();</script></body></html>`
}

func TestCSPInlineHandlerAttribute(t *testing.T) {
	body := "document.getElementById('out').textContent = 'ran'"
	sum := sha256.Sum256([]byte(body))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	cases := []struct {
		name, policy string
		want         string
		blocked      bool
	}{
		{"no policy", "", "ran", false},
		{"unsafe-inline allows", "script-src 'unsafe-inline'", "ran", false},
		{"nonce does not cover attributes", "script-src 'nonce-n1'", "idle", true},
		{"nonce neutralises unsafe-inline", "script-src 'nonce-n1' 'unsafe-inline'", "idle", true},
		{"hash alone does not cover attributes", "script-src 'nonce-n1' " + hash, "idle", true},
		{"unsafe-hashes with the hash allows", "script-src 'nonce-n1' 'unsafe-hashes' " + hash, "ran", false},
		{"script-src-attr none overrides script-src", "script-src 'unsafe-inline'; script-src-attr 'none'", "idle", true},
		{"script-src-attr unsafe-inline overrides script-src", "script-src 'nonce-n1'; script-src-attr 'unsafe-inline'", "ran", false},
		{"default-src is the fallback", "default-src 'none'; script-src-elem 'nonce-n1'", "idle", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var h = cspHeader()
			if tc.policy != "" {
				h = cspHeader(tc.policy)
			}
			doc, diag := renderCSP(t, cspHandlerPage(), "https://example.com/p", h, nil)
			if got := divText(t, doc, "out"); got != tc.want {
				t.Errorf("#out = %q, want %q (errors %v)", got, tc.want, diag.Errors)
			}
			if hasCSPError(diag) != tc.blocked {
				t.Errorf("CSP diagnostic present = %v, want %v: %v", hasCSPError(diag), tc.blocked, diag.Errors)
			}
		})
	}
}

func TestCSPReportOnlyInlineHandlerRuns(t *testing.T) {
	doc, diag := renderCSP(t, cspHandlerPage(), "https://example.com/p", cspReportOnly("script-src 'nonce-n1'"), nil)
	if got := divText(t, doc, "out"); got != "ran" {
		t.Errorf("report-only must not block: #out = %q", got)
	}
	if !hasCSPError(diag) {
		t.Errorf("report-only would-block not recorded: %v", diag.Errors)
	}
}

// A property-assigned handler is not inline script: CSP has nothing to say.
func TestCSPDoesNotGateHandlerProperties(t *testing.T) {
	doc, diag := renderCSP(t, `<html><body><button id="b">x</button><div id="out">idle</div><script nonce="n1">
		var b = document.getElementById('b');
		b.onclick = function () { document.getElementById('out').textContent = 'ran'; };
		b.click();
	</script></body></html>`, "https://example.com/p", cspHeader("script-src 'nonce-n1'"), nil)
	if got := divText(t, doc, "out"); got != "ran" || hasCSPError(diag) {
		t.Errorf("#out = %q, errors %v", got, diag.Errors)
	}
}

// interact's synthesized click and submit run the handlers too.
func TestInteractRunsEventHandlers(t *testing.T) {
	lc, done := openContext(t, `<html><body>
		<button id="attr" onclick="document.getElementById('out').textContent += 'A'">a</button>
		<button id="prop">p</button>
		<form id="f" action="/submitted" onsubmit="document.getElementById('out').textContent += 'S'; return false"><button id="go">go</button></form>
		<div id="out"></div><script>
		document.getElementById('prop').onclick = function () { document.getElementById('out').textContent += 'P'; };
	</script></body></html>`, 3*time.Second)
	defer done()
	for _, a := range []js.Action{
		{Selector: "#attr", Type: "click"}, {Selector: "#prop", Type: "click"}, {Selector: "#f", Type: "submit"},
	} {
		if _, err := lc.Dispatch(context.Background(), a); err != nil {
			t.Fatalf("dispatch %s: %v", a.Selector, err)
		}
	}
	if out := snapshot(t, lc); !strings.Contains(out, `<div id="out">APS</div>`) {
		t.Errorf("handlers did not all run:\n%s", out)
	}
}

func TestHandlerAttributeSyntaxErrorIsRecorded(t *testing.T) {
	doc, diag := renderCSP(t, `<html><body><button id="b" onclick="this is not js">x</button><div id="out">idle</div><script>
		var b = document.getElementById('b');
		b.click();
		document.getElementById('out').textContent = String(b.onclick);
	</script></body></html>`, "https://example.com/p", nil, nil)
	if got := divText(t, doc, "out"); got != "null" {
		t.Errorf("#out = %q, want null", got)
	}
	if all := strings.Join(diag.Errors, "\n"); !strings.Contains(all, "onclick attribute") {
		t.Errorf("syntax error not recorded: %v", diag.Errors)
	}
}

// requestSubmit() fires submit; an onsubmit attribute returning false cancels the
// navigation, and one that does not lets it through.
func TestHandlerOnSubmitCancelsNavigation(t *testing.T) {
	page := func(body string) string {
		return `<html><body><form id="f" action="/submitted" onsubmit="` + body + `"><input name="q" value="1"></form>
			<script>document.getElementById('f').requestSubmit();</script></body></html>`
	}
	_, diag := renderCSP(t, page("return false"), "https://example.com/p", nil, nil)
	if diag.PendingNavigation != "" {
		t.Errorf("canceled submit still navigated to %q", diag.PendingNavigation)
	}
	_, diag = renderCSP(t, page("window.seen = 1"), "https://example.com/p", nil, nil)
	if !strings.Contains(diag.PendingNavigation, "/submitted") {
		t.Errorf("uncanceled submit did not navigate: %q", diag.PendingNavigation)
	}
}

// Feature tests read handler presence: `name in el` decides Preact's event-name
// casing and Vue's prop-vs-attribute choice, and `'ontouchstart' in window` is
// the usual "touch device?" probe, which a desktop engine must answer with no.
func TestHandlerPresenceProbes(t *testing.T) {
	got := handlerOut(t, `<html><body><div id="d"></div><div id="out"></div><script>
		var d = document.getElementById('d'), r = [];
		r.push('onclick' in d, 'onclick' in document, 'onclick' in window, 'onhashchange' in window,
			'onhashchange' in d, 'onhashchange' in document.body, 'onreadystatechange' in document,
			'ontouchstart' in window, 'ontouchstart' in d, 'onfocusin' in window,
			typeof Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'onclick').set,
			'onclick' in document.createElementNS('http://www.w3.org/2000/svg', 'svg'));
		document.getElementById('out').textContent = r.join(',');
	</script></body></html>`)
	if got != "true,true,true,true,false,true,true,false,false,false,function,true" {
		t.Errorf("#out = %q", got)
	}
}
