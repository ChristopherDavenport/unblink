package js_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// document.readyState walks loading → interactive → complete at the points a
// browser reaches them, with readystatechange at each transition.
func TestReadyStateLifecycle(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{
		"/ext.js": `window.__log.push('ext:' + document.readyState);`,
		"/m.mjs":  `window.__log.push('module:' + document.readyState);`,
	}}
	got, _ := evalIn(t, `<script>
		window.__log = ['head:' + document.readyState];
		document.addEventListener('readystatechange', function () { __log.push('rsc:' + document.readyState); });
		document.addEventListener('DOMContentLoaded', function () { __log.push('dcl:' + document.readyState); });
		window.addEventListener('load', function () { __log.push('load:' + document.readyState); done(__log.join(' ')); });
	</script><script src="/ext.js"></script><script type="module" src="/m.mjs"></script>`,
		``, `__log.push('body:' + document.readyState);`, js.Env{Transport: tr})
	want := "head:loading ext:loading body:loading rsc:interactive module:interactive dcl:interactive rsc:complete load:complete"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The library idiom: wait for DOMContentLoaded only while loading. With a
// constant "complete" a <head> script initialised immediately, before a later
// script had created the element it was meant to initialise (htmx's ready()).
func TestReadyStateGatesHeadInit(t *testing.T) {
	got, _ := evalIn(t, `<script>
		window.__found = 'unset';
		function init() { window.__found = String(!!document.getElementById('late')); }
		if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init); else init();
	</script>`, ``, `
		var e = document.createElement('div'); e.id = 'late'; document.body.appendChild(e);
		window.addEventListener('load', function () { done(window.__found); });`, js.Env{})
	if got != "true" {
		t.Errorf("head init ran before the content existed: %q", got)
	}
}

// After the load, and in timers and later dispatches, it stays complete; a
// parsed/created document is always complete.
func TestReadyStateAfterLoadAndDetachedDocs(t *testing.T) {
	wantJS(t, ``, `
		var d = new DOMParser().parseFromString('<p>', 'text/html');
		setTimeout(function () { done(document.readyState + ',' + d.readyState + ',' + document.implementation.createHTMLDocument('').readyState); }, 1);`,
		"complete,complete,complete")
}

// A live session context goes through the same sequence and is complete once open.
func TestReadyStateLiveContext(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><head><script>window.__s = [document.readyState];
		document.addEventListener('DOMContentLoaded', function () { __s.push(document.readyState); });
		window.addEventListener('load', function () { __s.push(document.readyState); });</script></head>
		<body><button id="b">go</button><div id="o"></div>
		<script>document.getElementById('b').addEventListener('click', function () {
			document.getElementById('o').textContent = __s.join(',') + '|' + document.readyState; });</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://example.com/")
	eng := js.New(js.WithTimeout(2 * time.Second))
	ctx := context.Background()
	c, err := eng.Open(ctx, doc, base, js.Env{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Dispatch(ctx, js.Action{Selector: "#b", Type: "click"}); err != nil {
		t.Fatal(err)
	}
	snap, _, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.Write(snap)
	if !strings.Contains(sb.String(), "loading,interactive,complete|complete") {
		t.Errorf("live context readyState sequence wrong:\n%s", sb.String())
	}
}
