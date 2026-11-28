package js_test

import (
	"strings"
	"testing"
)

// evalChecks runs each named expression in a page and returns name → result.
func evalChecks(t *testing.T, body string, checks [][2]string) map[string]string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`<html><head><base href="https://example.com/dir/"></head><body>` + body + `<pre id="__out"></pre><script>
	var __r = [];
	function __c(n, f) { var v; try { v = f(); } catch (e) { v = 'ERR ' + e; } __r.push(n + '=' + v); }
	`)
	for _, c := range checks {
		sb.WriteString("__c(" + jsString(c[0]) + ", function () { return " + c[1] + "; });\n")
	}
	sb.WriteString(`document.getElementById('__out').textContent = __r.join('\n');</script></body></html>`)
	out := render(t, sb.String())
	i := strings.Index(out, `<pre id="__out">`)
	j := strings.Index(out[i:], "</pre>")
	res := map[string]string{}
	for _, ln := range strings.Split(htmlUnescape(out[i+len(`<pre id="__out">`):i+j]), "\n") {
		if k, v, ok := strings.Cut(ln, "="); ok {
			res[k] = v
		}
	}
	return res
}

func jsString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&amp;", "&").Replace(s)
}

func expectChecks(t *testing.T, body string, checks [][3]string) {
	t.Helper()
	in := make([][2]string, len(checks))
	for i, c := range checks {
		in[i] = [2]string{c[0], c[1]}
	}
	got := evalChecks(t, body, in)
	for _, c := range checks {
		if got[c[0]] != c[2] {
			t.Errorf("%s: %s = %q, want %q", c[0], c[1], got[c[0]], c[2])
		}
	}
}

// Ember decides whether it is in a browser with exactly this expression
// (@ember/-internals/browser-environment hasDOM). When it is false the app boots
// non-interactive with a "none" location: Discourse rendered nothing.
func TestEmberHasDOMCheckHolds(t *testing.T) {
	expectChecks(t, "", [][3]string{
		{"hasDOM", "typeof self === 'object' && self !== null && self.Object === Object && typeof Window !== 'undefined' && self.constructor === Window && typeof document === 'object' && document !== null && self.document === document && typeof location === 'object' && location !== null && self.location === location && typeof history === 'object' && history !== null && self.history === history && typeof navigator === 'object' && navigator !== null && self.navigator === navigator && typeof navigator.userAgent === 'string'", "true"},
	})
}

func TestHostSingletonsHaveInterfaces(t *testing.T) {
	expectChecks(t, "", [][3]string{
		{"window", "window instanceof Window && window instanceof EventTarget", "true"},
		{"window tag", "Object.prototype.toString.call(window)", "[object Window]"},
		{"window listeners still work", "(function(){ var n = 0; window.addEventListener('x', function(){ n++; }); window.dispatchEvent(new Event('x')); return n; })()", "1"},
		{"location", "location instanceof Location && Object.prototype.toString.call(location)", "[object Location]"},
		{"location still works", "location.pathname + '|' + String(location).slice(0, 8)", "/page|https://"},
		{"history", "history instanceof History", "true"},
		{"navigator", "navigator instanceof Navigator && typeof navigator.userAgent", "string"},
		{"screen", "screen instanceof Screen && screen.width", "1280"},
		{"performance", "performance instanceof Performance && typeof performance.now()", "number"},
		{"storage", "localStorage instanceof Storage && sessionStorage instanceof Storage", "true"},
		{"storage still works", "(localStorage.setItem('k', 'v'), localStorage.getItem('k'))", "v"},
		{"crypto", "crypto instanceof Crypto && crypto.subtle instanceof SubtleCrypto", "true"},
		{"customElements", "customElements instanceof CustomElementRegistry", "true"},
		{"matchMedia", "matchMedia('(min-width: 1px)') instanceof MediaQueryList && matchMedia('(min-width: 1px)').matches", "true"},
		{"illegal constructor", "(function(){ try { new Window(); return 'constructed'; } catch (e) { return e.name; } })()", "TypeError"},
		{"origin", "window.origin", "https://example.com"},
		{"length", "window.length", "0"},
		{"baseURI", "document.baseURI + ' ' + document.body.baseURI", "https://example.com/dir/ https://example.com/dir/"},
		// Interfaces the engine does not implement stay undefined.
		{"absent", "[typeof TouchEvent, typeof ServiceWorker, typeof Navigation, typeof trustedTypes, typeof OffscreenCanvas].join()", "undefined,undefined,undefined,undefined,undefined"},
	})
}

func TestEventsAreInstancesOfTheirInterfaces(t *testing.T) {
	expectChecks(t, `<button id="b">b</button><input id="i">`, [][3]string{
		{"constructed chain", "(function(){ var e = new PointerEvent('pointerdown', {button: 2, ctrlKey: true}); return [e instanceof PointerEvent, e instanceof MouseEvent, e instanceof UIEvent, e instanceof Event, e.button, e.ctrlKey, e.pointerId, e.detail].join(); })()", "true,true,true,true,2,true,0,0"},
		{"custom", "new CustomEvent('x', {detail: 7}) instanceof Event && new CustomEvent('x').detail === null", "true"},
		{"tag", "Object.prototype.toString.call(new KeyboardEvent('keydown'))", "[object KeyboardEvent]"},
		{"subclass", "(function(){ class My extends Event { constructor() { super('my', {bubbles: true}); this.extra = 1; } } var e = new My(); return [e instanceof My, e instanceof Event, e.type, e.bubbles, e.extra].join(); })()", "true,true,my,true,1"},
		{"dispatched own event keeps its class", "(function(){ var got; var b = document.getElementById('b'); b.addEventListener('k', function (e) { got = [e instanceof KeyboardEvent, e.key, e.target === b].join(); }); b.dispatchEvent(new KeyboardEvent('k', {key: 'q'})); return got; })()", "true,q,true"},
		{"element.click()", "(function(){ var got; var b = document.getElementById('b'); b.addEventListener('click', function (e) { got = [e instanceof Event, e instanceof MouseEvent, e instanceof PointerEvent, e.button, e.ctrlKey, typeof e.preventDefault, e.constructor.name].join(); }); b.click(); return got; })()", "true,true,true,0,false,function,PointerEvent"},
		{"lifecycle", "(function(){ var got; document.addEventListener('readystatechange', function(){}); window.addEventListener('zz', function (e) { got = e instanceof Event; }); window.dispatchEvent(new Event('zz')); return got; })()", "true"},
		{"phases", "[Event.NONE, Event.CAPTURING_PHASE, Event.AT_TARGET, Event.BUBBLING_PHASE, new Event('x').AT_TARGET].join()", "0,1,2,3,2"},
		{"message event", "new MessageEvent('message', {data: 1}) instanceof Event", "true"},
		{"new classes", "[new FocusEvent('focus') instanceof UIEvent, new WheelEvent('wheel', {deltaY: 3}).deltaY, new SubmitEvent('submit').submitter, new PopStateEvent('popstate', {state: {a: 1}}).state.a, new HashChangeEvent('hashchange', {newURL: 'u'}).newURL, new ProgressEvent('progress', {loaded: 5}).loaded].join()", "true,3,,1,u,5"},
	})
}

func TestCollectionsHaveInterfaces(t *testing.T) {
	expectChecks(t, `<ul id="u"><li id="a" class="x">1</li><li name="b" class="x">2</li></ul><form id="f"><input name="q"></form>`, [][3]string{
		{"querySelectorAll", "(function(){ var l = document.querySelectorAll('li'); return [l instanceof NodeList, l instanceof HTMLCollection, Object.prototype.toString.call(l), l.length, l.item(1) === l[1], l.item(5)].join(); })()", "true,false,[object NodeList],2,true,"},
		{"childNodes", "document.getElementById('u').childNodes instanceof NodeList", "true"},
		{"forEach", "(function(){ var s = ''; document.querySelectorAll('li').forEach(function (e) { s += e.textContent; }); return s; })()", "12"},
		{"children", "(function(){ var l = document.getElementById('u').children; return [l instanceof HTMLCollection, l instanceof NodeList, Object.prototype.toString.call(l), l.namedItem('a') === l[0], l.namedItem('b') === l[1], l.namedItem('zz')].join(); })()", "true,false,[object HTMLCollection],true,true,"},
		{"getElementsBy*", "document.getElementsByTagName('li') instanceof HTMLCollection && document.getElementsByClassName('x') instanceof HTMLCollection && document.getElementById('u').getElementsByTagName('li').length", "2"},
		{"document.forms", "document.forms instanceof HTMLCollection && document.forms.length === 1 && document.forms.item(0).id", "f"},
		{"form.elements", "document.getElementById('f').elements.namedItem('q').name", "q"},
		{"prototype patching", "(function(){ NodeList.prototype.__probe = function () { return this.length; }; return document.querySelectorAll('li').__probe(); })()", "2"},
		{"spread", "[...document.querySelectorAll('li')].length + Array.from(document.getElementById('u').children).length", "4"},
	})
}
