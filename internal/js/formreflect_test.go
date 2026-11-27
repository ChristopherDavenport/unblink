package js_test

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Regression tests for the form-associated reflection: the stamped form owner, the
// id-indexed form="id" re-association, the :state() rewrite and innerHTML upgrades.
// Shares facePrelude with forminternals_test.go.

// faceLogSlow is faceLog with a render budget sized for pages of thousands of
// elements under the race detector.
func faceLogSlow(t *testing.T, body string) []string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(`<html><body>` + facePrelude + body + `</body></html>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	if err := js.New(js.WithTimeout(60*time.Second)).Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	m := outRE.FindStringSubmatch(buf.String())
	if m == nil {
		t.Fatalf("no #out in render")
	}
	return strings.Split(m[1], "|")
}

// The reflection carries the owner the engine resolved. A form-associated element in
// a shadow root is not owned by a form outside it — by ancestry or by form="id" —
// and says so on the composed tree, where it now sits inside that form.
func TestReflectionStampsTheEngineFormOwner(t *testing.T) {
	out := render(t, `<html><body>`+facePrelude+`
	<form id="f"><my-input id="light" name="light"></my-input><x-wrap id="w"></x-wrap></form>
	<form id="g"></form>
	<my-input id="byid" name="byid" form="g"></my-input>
	<script>
	  var sr = document.getElementById('w').attachShadow({ mode: 'open' });
	  var a = document.createElement('my-input'); a.setAttribute('name', 'inshadow');
	  var b = document.createElement('my-input'); b.setAttribute('name', 'shadowref'); b.setAttribute('form', 'f');
	  sr.appendChild(a); sr.appendChild(b);
	  [a, b, document.getElementById('light'), document.getElementById('byid')].forEach(function (e) { e.internals.setFormValue('v'); });
	  var f = document.getElementById('f');
	  log(a.internals.form === null && b.internals.form === null);
	  log(Array.prototype.map.call(f.elements, function (e) { return e.getAttribute('name'); }).join(','));
	</script></body></html>`)
	if m := outRE.FindStringSubmatch(out); m == nil || m[1] != "true|light" {
		t.Fatalf("engine view = %v, want true|light", m)
	}
	key := func(id string) string {
		m := regexp.MustCompile(`<form id="` + id + `" ` + page.FormKeyAttr + `="([^"]+)"`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("form %s carries no %s:\n%s", id, page.FormKeyAttr, out)
		}
		return m[1]
	}
	fKey, gKey := key("f"), key("g")
	if fKey == gKey {
		t.Fatalf("forms share key %q", fKey)
	}
	for name, want := range map[string]string{"light": fKey, "byid": gKey, "inshadow": "", "shadowref": ""} {
		m := regexp.MustCompile(`<my-input[^>]*name="` + name + `"[^>]*>`).FindString(out)
		if !strings.Contains(m, page.FormOwnerAttr+`="`+want+`"`) {
			t.Errorf("%s: want %s=%q, tag is %s", name, page.FormOwnerAttr, want, m)
		}
	}
}

// An id write re-resolves only the elements whose form attribute names the id that
// changed, and resolves that id once — not once per element.
func TestFormRefResyncFollowsTheChangedID(t *testing.T) {
	const n = 3000
	got := faceLogSlow(t, `<form id="f"></form><form id="g"></form><div id="box"></div>
	<my-input id="other" name="o" form="g"></my-input>
	<script>
	  var box = document.getElementById('box'), f = document.getElementById('f'), g = document.getElementById('g');
	  var els = [], frag = document.createDocumentFragment();
	  for (var i = 0; i < `+fmt.Sprint(n)+`; i++) {
	    var e = document.createElement('my-input'); e.setAttribute('form', i % 2 ? 'f' : 'nope'); els.push(e); frag.appendChild(e);
	  }
	  box.appendChild(frag);
	  var other = document.getElementById('other');
	  function reset() { els.forEach(function (e) { e.calls = []; }); other.calls = []; }
	  // count(k, id): how many elements with index parity k are owned by form id.
	  function count(k, form) { var c = 0; els.forEach(function (e, i) { if (i % 2 === k && e.internals.form === form) c++; }); return c; }
	  function fired(k) { var c = 0; els.forEach(function (e, i) { if (i % 2 === k) c += e.calls.length; }); return c; }
	  function step(what) { log(what + ':f=' + count(1, f) + ',g=' + count(1, g) + ',nope=' + count(0, null) + ',fired=' + fired(1) + '/' + fired(0) + '/' + other.calls.length); reset(); }
	  step('start');
	  // An unrelated id: nothing is re-resolved. Many writes, to make a per-write
	  // full re-resolution (formerly n full-tree scans each) unmissable.
	  var t0 = Date.now();
	  for (var j = 0; j < 400; j++) document.body.id = 'zz' + j;
	  var ms = Date.now() - t0;
	  step('unrelated');
	  f.id = 'was-f'; step('renamed');        // the named id disappears
	  g.id = 'f'; step('moved');              // ...appears on another form (other loses g)
	  g.id = 'g'; f.id = 'f'; step('restored');
	  f.remove(); step('removed');            // the form leaves the tree
	  document.body.insertBefore(f, box); step('reinserted');
	  var late = document.createElement('form'); late.id = 'nope';
	  document.body.appendChild(late);
	  log('late:' + els.filter(function (e, i) { return i % 2 === 0 && e.internals.form === late; }).length);
	  log('fast:' + (ms < 10000));
	</script>`)
	half := fmt.Sprint(n / 2)
	wantLog(t, got,
		"start:f="+half+",g=0,nope="+half+",fired="+half+"/0/1",
		"unrelated:f="+half+",g=0,nope="+half+",fired=0/0/0",
		"renamed:f=0,g=0,nope="+half+",fired="+half+"/0/0",
		"moved:f=0,g="+half+",nope="+half+",fired="+half+"/0/1",
		// g.id='g' drops the elements and gives other its form back; f.id='f' re-owns them.
		"restored:f="+half+",g=0,nope="+half+",fired="+fmt.Sprint(n)+"/0/1",
		"removed:f=0,g=0,nope="+half+",fired="+half+"/0/0",
		"reinserted:f="+half+",g=0,nope="+half+",fired="+half+"/0/0",
		"late:"+half,
		"fast:true")
}

// A callback that mutates the tree while the elements naming an id are being
// re-resolved must not leave later elements with a stale shared lookup.
func TestFormRefResyncSurvivesReentrantMutation(t *testing.T) {
	got := faceLog(t, `<form id="f"></form>
	<my-input id="a" form="f"></my-input><my-input id="b" form="f"></my-input><my-input id="c" form="f"></my-input>
	<script>
	  var f = document.getElementById('f'), ids = ['a', 'b', 'c'];
	  var el = function (id) { return document.getElementById(id); };
	  // While #f is being renamed away, the first element re-resolved puts a new #f in.
	  var fresh = document.createElement('form'); fresh.id = 'f';
	  el('a').formAssociatedCallback = function (form) { if (!form && !fresh.parentNode) document.body.appendChild(fresh); };
	  f.id = 'gone';
	  log(ids.map(function (id) { return el(id).internals.form === fresh; }).join(','));
	  log(fresh.elements.length);
	</script>`)
	wantLog(t, got, "true,true,true", "3")
}

// .a\::state(x) — an escaped colon ends a class name; the colon after it starts the
// pseudo-class. Only an unescaped "::" is a pseudo-element.
func TestStateSelectorAfterEscapedColon(t *testing.T) {
	got := faceLog(t, `<x-s id="s" class="a:"></x-s><x-s id="t" class="a:"></x-s>
	<script>
	  class XS extends HTMLElement { constructor() { super(); this.internals = this.attachInternals(); } }
	  customElements.define('x-s', XS);
	  document.getElementById('s').internals.states.add('x');
	  log(Array.prototype.map.call(document.querySelectorAll('.a\\::state(x)'), function (e) { return e.id; }).join(','));
	  log(document.getElementById('s').matches('.a\\::state(x)') + ',' + document.getElementById('t').matches('.a\\::state(x)'));
	  log(document.querySelectorAll('x-s::state(x)').length);
	</script>`)
	wantLog(t, got, "s", "true,false", "0")
}

// Markup assigned through innerHTML upgrades its custom elements when it lands in a
// connected tree — a shadow root included — so a form-associated element created
// that way has internals to reflect. Detached and <template> markup stays inert.
func TestInnerHTMLUpgradesCustomElements(t *testing.T) {
	out := render(t, `<html><body>`+facePrelude+`
	<div id="light"></div><div id="host"></div><template id="tpl"></template>
	<script>
	  class XP extends HTMLElement {
	    constructor() { super(); log('ctor'); }
	    connectedCallback() { log('connected:' + this.id); }
	  }
	  customElements.define('x-p', XP);
	  document.getElementById('light').innerHTML = '<p><x-p id="in-light"></x-p></p>';
	  var sr = document.getElementById('host').attachShadow({ mode: 'open' });
	  sr.innerHTML = '<x-p id="in-shadow"></x-p><my-input name="face"></my-input>';
	  log(sr.querySelector('x-p') instanceof XP);
	  sr.querySelector('my-input').internals.setFormValue('v');
	  var d = document.createElement('div'); d.innerHTML = '<x-p id="detached"></x-p>';
	  document.getElementById('tpl').innerHTML = '<x-p id="in-template"></x-p>';
	  log('inert:' + (d.firstChild instanceof XP));
	  document.body.appendChild(d);
	</script></body></html>`)
	m := outRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no #out:\n%s", out)
	}
	wantLog(t, strings.Split(m[1], "|"),
		"ctor", "connected:in-light", "ctor", "connected:in-shadow", "true", "inert:false", "ctor", "connected:detached")
	if !strings.Contains(out, page.FormValueAttr+`="face=v"`) {
		t.Errorf("the innerHTML-created form-associated element was not stamped:\n%s", out)
	}
}
