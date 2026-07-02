package js_test

// The live-page refresh skip (browser.refreshFromLive) trusts DOMVersion: any
// mutation a snapshot would show MUST bump it, or a session read serves a stale
// page. This table drives every JS-visible mutation API through a live context
// and asserts the version moved — plus a no-op control asserting a dispatch
// with no mutation does NOT move it (that's what makes Interact's Changed
// version-bracketing meaningful).

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

const domVersionPage = `<html><body>
	<div id="el" data-y="1"><span>child</span></div>
	<input id="inp">
	<input type="checkbox" id="chk">
	<textarea id="ta">old</textarea>
	<template id="tpl"><span>t</span></template>
	<button id="go">go</button>
	<script>
	  document.getElementById('go').addEventListener('click', function () {
	    var el = document.getElementById('el');
	    __MUTATE__
	  });
	</script>
</body></html>`

func openLive(t *testing.T, pageHTML string) (js.LiveContext, func()) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/app")
	eng := js.New(js.WithTimeout(3 * time.Second))
	lc, err := eng.Open(context.Background(), doc, base, js.Env{})
	if err != nil {
		eng.Close()
		t.Fatalf("open: %v", err)
	}
	return lc, func() { lc.Close(); eng.Close() }
}

func TestMutationAPIsBumpDOMVersion(t *testing.T) {
	cases := []struct {
		name   string
		mutate string // body of window.__mutate(el)
	}{
		{"textContent", "el.textContent = 'x';"},
		{"innerHTML", "el.innerHTML = '<b>x</b>';"},
		{"appendChild", "el.appendChild(document.createElement('span'));"},
		{"removeChild", "el.removeChild(el.firstChild);"},
		{"insertBefore", "el.insertBefore(document.createElement('i'), el.firstChild);"},
		{"replaceChild", "el.replaceChild(document.createElement('u'), el.firstChild);"},
		{"remove", "el.firstChild.remove ? el.firstChild.remove() : el.removeChild(el.firstChild);"},
		{"setAttribute", "el.setAttribute('data-x', '1');"},
		{"removeAttribute", "el.removeAttribute('data-y');"},
		{"classList.add", "el.classList.add('c');"},
		{"className", "el.className = 'k';"},
		{"input value property", "document.getElementById('inp').value = 'v';"},
		{"textarea value property", "document.getElementById('ta').value = 'v2';"},
		{"checked property", "document.getElementById('chk').checked = true;"},
		{"template content access", "void document.getElementById('tpl').content;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := strings.Replace(domVersionPage, "__MUTATE__", tc.mutate, 1)
			lc, done := openLive(t, page)
			defer done()
			ctx := context.Background()
			v0, err := lc.DOMVersion(ctx)
			if err != nil {
				t.Fatalf("version before: %v", err)
			}
			res, err := lc.Dispatch(ctx, js.Action{Selector: "#go", Type: "click"})
			if err != nil || !res.Matched {
				t.Fatalf("dispatch: matched=%v err=%v jsErrors=%v", res.Matched, err, res.Errors)
			}
			v1, err := lc.DOMVersion(ctx)
			if err != nil {
				t.Fatalf("version after: %v", err)
			}
			if v1 == v0 {
				t.Errorf("%s did not bump domVersion (still %d) — live-page refresh would serve a stale snapshot", tc.name, v1)
			}
		})
	}
}

// The control: a click whose handler mutates nothing snapshot-visible keeps the
// version stable, so Interact can report Changed=false and reads can skip the
// refresh. el.style belongs here: the engine's style is a non-serialized
// property bag (no CSSOM), so style writes never appear in a snapshot and
// correctly do not bump the version.
func TestNoOpDispatchKeepsDOMVersion(t *testing.T) {
	page := strings.Replace(domVersionPage, "__MUTATE__",
		"el.style.display = 'none'; void 0;", 1)
	lc, done := openLive(t, page)
	defer done()
	ctx := context.Background()
	v0, err := lc.DOMVersion(ctx)
	if err != nil {
		t.Fatalf("version before: %v", err)
	}
	res, err := lc.Dispatch(ctx, js.Action{Selector: "#go", Type: "click"})
	if err != nil || !res.Matched {
		t.Fatalf("dispatch: matched=%v err=%v", res.Matched, err)
	}
	v1, err := lc.DOMVersion(ctx)
	if err != nil {
		t.Fatalf("version after: %v", err)
	}
	if v1 != v0 {
		t.Errorf("no-op dispatch bumped domVersion %d -> %d; Changed would always report true", v0, v1)
	}
}

// Snapshot returns the version its bytes correspond to (atomic pair).
func TestSnapshotVersionPairsWithBytes(t *testing.T) {
	lc, done := openLive(t, domVersionPage)
	defer done()
	ctx := context.Background()
	_, sv, err := lc.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	v, err := lc.DOMVersion(ctx)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if sv != v {
		t.Errorf("snapshot version %d != DOMVersion %d with no interleaving mutation", sv, v)
	}
}
