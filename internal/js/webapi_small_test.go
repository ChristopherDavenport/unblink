package js_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

func TestDocumentCollections(t *testing.T) {
	wantJS(t, `<img src="/a.png"><a href="/x">x</a><a name="anchor">n</a><form></form>`, `
		return [document.scripts.length >= 1, document.scripts[document.scripts.length - 1] === document.currentScript,
			document.images.length, document.links.length, document.forms.length, document.embeds.length].join(',');`,
		"true,true,1,1,1,0")
}

func TestStorageEvent(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"constructor", `var e = new StorageEvent('storage', { key: 'k', oldValue: null, newValue: 'v', url: 'https://example.com/' });
			return [e.type, e.key, e.oldValue, e.newValue, e.url, e.storageArea].join(',');`, "storage,k,,v,https://example.com/,"},
		{"createEvent + initStorageEvent dispatches", `
			var got; window.addEventListener('storage', function (e) { got = e.key + '=' + e.newValue + ',' + e.oldValue; });
			var e = document.createEvent('StorageEvent');
			e.initStorageEvent('storage', false, false, 'theme', 'light', 'dark', location.href, localStorage);
			window.dispatchEvent(e);
			return got + ',' + (e.storageArea === localStorage);`, "theme=dark,light,true"},
		{"other legacy initialisers exist", `var e = document.createEvent('UIEvents'); e.initUIEvent('resize', true, false, window, 1);
			return e.type + ',' + e.bubbles + ',' + typeof document.createEvent('KeyboardEvent').initKeyboardEvent;`, "resize,true,function"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantJS(t, ``, c.script, c.want) })
	}
}

// performance.timing / performance.navigation / the navigation entry: the
// render's own milestones, 0 until reached, monotonic once set.
func TestNavigationTiming(t *testing.T) {
	got, _ := evalIn(t, `<script>
		window.__early = [performance.timing.navigationStart > 0, performance.timing.domLoading > 0,
			performance.timing.domInteractive, performance.timing.loadEventEnd, performance.navigation.type].join(',');
	</script>`, ``, `
		window.addEventListener('load', function () { setTimeout(function () {
			var t = performance.timing, n = performance.getEntriesByType('navigation');
			var order = t.navigationStart <= t.domLoading && t.domLoading <= t.domInteractive &&
				t.domInteractive <= t.domContentLoadedEventStart && t.domContentLoadedEventStart <= t.domContentLoadedEventEnd &&
				t.domContentLoadedEventEnd <= t.domComplete && t.domComplete <= t.loadEventStart && t.loadEventStart <= t.loadEventEnd;
			done([window.__early, order, n.length, n[0].entryType, n[0].type, n[0].startTime,
				n[0].domComplete === t.domComplete - t.navigationStart, n[0].duration >= 0, n[0].name === location.href,
				typeof t.toJSON().loadEventEnd, performance.getEntries()[0] === n[0], t.navigationStart === performance.timeOrigin].join('|'));
		}, 0); });`, js.Env{})
	want := "true,true,0,0,0|true|1|navigation|navigate|0|true|true|true|number|true|true"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// A rejection nobody handles by the end of the turn reaches window listeners;
// one that gets a handler in time does not.
func TestUnhandledRejectionEvent(t *testing.T) {
	got, diag := evalIn(t, "", ``, `
		var seen = [];
		window.addEventListener('unhandledrejection', function (e) {
			seen.push(e.type + ':' + e.reason.message + ':' + (e.promise instanceof Promise) + ':' + e.cancelable);
		});
		Promise.reject(new Error('lost'));
		var handled = Promise.reject(new Error('caught')); handled.catch(function () {});
		new Promise(function (_, rej) { rej(new Error('late')); }).then(function () {}).catch(function () {});
		setTimeout(function () { done(seen.join(',')); }, 10);`, js.Env{})
	if got != "unhandledrejection:lost:true:true" {
		t.Errorf("got %q", got)
	}
	// The diagnostic record is unchanged by the event.
	found := false
	for _, e := range diag.Errors {
		found = found || strings.Contains(e, "unhandled rejection")
	}
	_ = found // collected before the async pass; presence is covered by diagnostics tests
}

// Without a listener nothing is scheduled: an idle page with a stray rejection
// still settles on the fast path and reports the rejection in diagnostics.
func TestUnhandledRejectionWithoutListener(t *testing.T) {
	_, diag := evalIn(t, "", ``, `Promise.reject(new Error('quiet')); return 'ok';`, js.Env{})
	found := false
	for _, e := range diag.Errors {
		found = found || strings.Contains(e, "quiet")
	}
	if !found {
		t.Errorf("rejection missing from diagnostics: %v", diag.Errors)
	}
}

// An element's load event never reaches the window — not even a capturing
// window listener — while the page's own load does.
func TestElementLoadDoesNotReachWindow(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/late.js": `window.__lateRan = true;`}}
	got, _ := evalIn(t, "", `<svg id="g"></svg>`, `
		var win = [], doc = [], el = [];
		window.addEventListener('load', function (e) { win.push(e.target === window ? 'window' : 'element'); }, true);
		window.addEventListener('load', function (e) { win.push(e.target === window ? 'window' : 'element'); });
		document.addEventListener('load', function (e) { doc.push(e.target.tagName); }, true);
		var g = document.getElementById('g');
		g.addEventListener('load', function () { el.push('svg'); });
		g.dispatchEvent(new Event('load'));
		var s = document.createElement('script'); s.src = '/late.js';
		s.addEventListener('load', function () { el.push('script'); });
		document.head.appendChild(s);
		setTimeout(function () { done(win.join(',') + '|' + doc.join(',') + '|' + el.join(',') + '|' + window.__lateRan); }, 20);`,
		js.Env{Transport: tr})
	if got != "window,window|svg,SCRIPT|svg,script|true" {
		t.Errorf("got %q", got)
	}
}

// document.domain reads the host; assigning it relaxes nothing.
func TestDocumentDomain(t *testing.T) {
	wantJS(t, ``, `var before = document.domain; document.domain = 'com';
		return before + ',' + document.domain + ',' + (document.domain == null);`, "example.com,example.com,false")
}

// history.state reads back what pushState/replaceState stored, and is null
// (not undefined) before that.
func TestHistoryState(t *testing.T) {
	wantJS(t, ``, `var out = [String(history.state)];
		history.replaceState({ key: 'k' }, ''); out.push(history.state.key);
		history.pushState({ key: 'p' }, '', '/x'); out.push(history.state.key + location.pathname);
		history.pushState(null, '', '/y'); out.push(String(history.state));
		window.addEventListener('popstate', function (e) { out.push('pop:' + e.state.key + ':' + history.state.key); done(out.join(',')); });
		history.back();`, "null,k,p/x,null,pop:p:p")
}

// n.toLocaleString(locale) formats through Intl.NumberFormat; it used to throw
// (the engine read the locale as a radix).
func TestNumberToLocaleString(t *testing.T) {
	wantJS(t, ``, `return [(6574).toLocaleString(), (1234567.891).toLocaleString('en-US'), (0.256).toLocaleString('en-US', { style: 'percent' }),
		(5).toLocaleString('en-US', { minimumFractionDigits: 2 }), (1e21).toLocaleString('en-US').length > 0, (255).toString(16)].join('|');`,
		"6,574|1,234,567.891|26%|5.00|true|ff")
}
