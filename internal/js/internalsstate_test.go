package js_test

import (
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/page"
)

// The rest of ElementInternals: form="id" association, the :state() selector, and
// the ARIA defaults that must reach unblink's own extraction. Shares facePrelude
// (a form-associated <my-input> that records its form* callbacks) with
// forminternals_test.go.

// A form attribute names the form owner by id, for a custom element and a native
// control alike, wherever the control sits; without a match there is no owner at
// all, not the ancestor form.
func TestFormAttributeNamesTheOwner(t *testing.T) {
	got := faceLog(t, `
	<form id="f"><input name="in" value="1"><input name="away" value="x" form="g"></form>
	<form id="g"><my-input id="stay" name="stay"></my-input><my-input id="lost" name="lost" form="nope"></my-input></form>
	<my-input id="a" name="email" form="f"></my-input>
	<input name="out" value="2" form="f">
	<script>
	  function names(form) { return Array.prototype.map.call(form.elements, function (e) { return e.getAttribute('name'); }).join(','); }
	  var f = document.getElementById('f'), g = document.getElementById('g');
	  var a = document.getElementById('a'), lost = document.getElementById('lost');
	  a.internals.setFormValue('me@x.y');
	  lost.internals.setFormValue('nobody');
	  log(a.internals.form === f);
	  log(lost.internals.form === null);
	  log(names(f));
	  log(names(g));
	  function entries(form) { return Array.from(new FormData(form).entries()).map(function (p) { return p[0] + '=' + p[1]; }).join(';'); }
	  log(entries(f));
	  log(entries(g));
	  log(a.calls.join(' ') + ' / ' + lost.calls.join(' ') + ' / ' + document.getElementById('stay').calls.join(' '));
	</script>`)
	wantLog(t, got,
		"true", "true",
		"in,email,out", "away,stay",
		"in=1;email=me@x.y;out=2", "away=x",
		// #lost never had an owner (its form attribute dangles), so nothing fired.
		"assoc:f /  / assoc:g")
}

// Changing or removing the attribute — or changing which element its id names —
// re-associates the element and fires formAssociatedCallback each time.
func TestFormAttributeReassociates(t *testing.T) {
	got := faceLog(t, `
	<form id="f"></form><form id="g"><my-input id="a" name="v"></my-input></form>
	<script>
	  var a = document.getElementById('a'), f = document.getElementById('f'), g = document.getElementById('g');
	  var step = function (what) { log(what + ':' + a.calls.join(' ') + '=' + (a.internals.form ? a.internals.form.id : 'null')); a.calls = []; };
	  step('start');
	  a.setAttribute('form', 'f'); step('set');
	  a.setAttribute('form', 'missing'); step('dangling');
	  var late = document.createElement('form'); late.id = 'missing';
	  document.body.appendChild(late); step('form-added');
	  late.id = 'other'; step('id-changed');
	  a.removeAttribute('form'); step('removed');
	  a.setAttribute('form', 'f'); a.calls = [];
	  a.remove(); step('detached');          // a form attribute needs a connected tree
	  document.body.appendChild(a); step('reattached');
	  f.remove(); step('owner-removed');
	</script>`)
	wantLog(t, got,
		"start:assoc:g=g",
		"set:assoc:f=f",
		"dangling:assoc:null=null",
		"form-added:assoc:missing=missing",
		"id-changed:assoc:null=null",
		"removed:assoc:g=g",
		"detached:assoc:null=null",
		"reattached:assoc:f=f",
		"owner-removed:assoc:null=null")
}

// form.submit() and reset follow the same owner rule as form.elements.
func TestFormAttributeSubmitAndReset(t *testing.T) {
	_, diag, _ := renderWaitDiag(t, `<html><body>`+facePrelude+`
	<form id="f" action="/go"><input name="in" value="1"></form>
	<my-input id="a" name="email" form="f"></my-input>
	<select name="pick" form="f"><option value="p" selected>P</option></select>
	<script>
	  var a = document.getElementById('a');
	  a.internals.setFormValue('v');
	  document.getElementById('f').reset();
	  if (a.calls.indexOf('reset') < 0) throw new Error('formResetCallback not delivered');
	  a.internals.setFormValue('v2');
	  document.getElementById('f').submit();
	</script></body></html>`, js.Env{}, time.Second)
	if got := diag.PendingNavigation; !strings.HasSuffix(got, "/go?email=v2&in=1&pick=p") {
		t.Errorf("submit navigated to %q, want the out-of-form controls' values too", got)
	}
}

// :state(x) matches an element whose CustomStateSet holds x, through matches,
// closest, querySelector and querySelectorAll, alone or inside a larger selector.
func TestStateSelector(t *testing.T) {
	got := faceLog(t, `
	<div id="wrap"><my-input id="a" class="k"><span id="in"></span></my-input><my-input id="b"></my-input></div>
	<script>
	  var a = document.getElementById('a'), b = document.getElementById('b');
	  var ids = function (sel) { return Array.prototype.map.call(document.querySelectorAll(sel), function (e) { return e.id; }).join(','); };
	  log(a.matches(':state(on)'));
	  a.internals.states.add('on'); a.internals.states.add('big');
	  b.internals.states.add('on');
	  log(a.matches(':state(on)') + ',' + a.matches(':state(big)') + ',' + b.matches(':state(big)') + ',' + a.matches(':state(o)'));
	  log(ids(':state(on)') + ' ' + ids('my-input.k:state(on):state(big)') + ' ' + ids('#wrap > :state(on) span'));
	  log(ids(':not(:state(big)):state(on)') + ' ' + ids('span, :state(big)'));
	  log(document.querySelector(':state(big)') === a);
	  log(document.getElementById('in').closest(':state(on)') === a);
	  log(document.getElementById('wrap').querySelector(':STATE( on )') === a);
	  b.internals.states.delete('on');
	  log(ids(':state(on)'));
	  a.internals.states.clear();
	  log(ids(':state(on)') === '' && !a.matches(':state(big)'));
	  // An escaped name, and a name only an escape can spell.
	  a.internals.states.add('a.b');
	  log(a.matches(':state(a\\.b)') + ',' + a.matches(':state(a\\2e b)'));
	  // The match leaves nothing behind for the page to see.
	  log(a.attributes.length + ',' + a.outerHTML.indexOf('state'));
	  // Text that only looks like the pseudo-class is left alone.
	  a.setAttribute('title', ':state(on)');
	  log(ids('[title=":state(on)"]'));
	</script>`)
	wantLog(t, got,
		"false",
		"true,true,false,false",
		"a,b a in",
		"b a,in",
		"true", "true", "true",
		"a",
		"true",
		"true,true",
		"2,-1",
		"a")
}

// A malformed :state( is an invalid selector and behaves like any other one. This
// engine reports an invalid selector as "no match" rather than a thrown SyntaxError;
// the point is that :state( does not change that either way.
func TestStateSelectorMalformedIsInvalid(t *testing.T) {
	got := faceLog(t, `<my-input id="a"></my-input>
	<script>
	  var a = document.getElementById('a');
	  a.internals.states.add('on');
	  var probe = function (sel) {
	    return thrown(function () { a.matches(sel); }) + '/' + a.matches(sel) + '/' +
	      document.querySelectorAll(sel).length + '/' + (document.querySelector(sel) === null) + '/' + (a.closest(sel) === null);
	  };
	  var baseline = probe('my-input:nonsense(');
	  [':state()', ':state(on', ':state(on off)', ':state(1x)', ':state("on")', ':state(on,on)', '::state(on)'].forEach(function (sel) {
	    log(probe(sel) === baseline);
	  });
	  // A selector with no :state( is untouched by any of this.
	  log(a.matches('my-input#a') + ',' + document.querySelectorAll('my-input, pre').length);
	</script>`)
	wantLog(t, got, "true", "true", "true", "true", "true", "true", "true", "true,2")
}

// The ARIA defaults a custom element sets through its internals are reflected onto
// the rendered tree, which is all extraction reads. Unreflected mixin members and
// cleared values leave nothing.
func TestRenderReflectsARIADefaults(t *testing.T) {
	out := render(t, `<html><body>`+facePrelude+`
	<span id="l1">Dark</span><span id="l2" aria-label="mode">x</span>
	<my-input id="a"></my-input><my-input id="b"></my-input><my-input id="c"></my-input>
	<script>
	  var a = document.getElementById('a').internals;
	  a.role = 'switch'; a.ariaLabel = 'Theme'; a.ariaChecked = 'true'; a.ariaRequired = 'true';
	  a.ariaInvalid = 'true'; a.ariaDisabled = 'true'; a.ariaExpanded = 'false';
	  a.ariaPressed = 'mixed'; a.ariaSelected = 'true';
	  a.ariaDescription = 'not read by extraction';
	  var b = document.getElementById('b').internals;
	  b.ariaLabelledByElements = [document.getElementById('l1'), document.getElementById('l2')];
	  var c = document.getElementById('c').internals;
	  c.role = 'button'; c.role = null;
	  c.ariaLabelledByElements = [document.getElementById('l1')]; c.ariaLabelledByElements = null;
	  log(document.getElementById('a').attributes.length + ',' + a.role + ',' + a.ariaLabel);
	</script></body></html>`)
	p := page.AriaDefaultPrefix
	for id, want := range map[string][]string{
		"a": {p + `role="switch"`, p + `aria-label="Theme"`, p + `aria-checked="true"`, p + `aria-required="true"`,
			p + `aria-invalid="true"`, p + `aria-disabled="true"`, p + `aria-expanded="false"`,
			p + `aria-pressed="mixed"`, p + `aria-selected="true"`},
		"b": {page.AriaDefaultLabelledByAttr + `="Dark mode"`},
	} {
		tag := openTag(t, out, id)
		for _, w := range want {
			if !strings.Contains(tag, w) {
				t.Errorf("#%s missing %s in %s", id, w, tag)
			}
		}
	}
	if tag := openTag(t, out, "a"); strings.Contains(tag, "description") {
		t.Errorf("unreflected ARIA member reached the tree: %s", tag)
	}
	if tag := openTag(t, out, "c"); strings.Contains(tag, p) {
		t.Errorf("cleared defaults still reflected: %s", tag)
	}
	if !strings.Contains(out, "1,switch,Theme") {
		t.Errorf("page JS saw a reflection attribute, or lost its own state:\n%s", out)
	}
}

// A live session's snapshot carries the defaults, tracks changes, and removes them
// again before page JS runs.
func TestSnapshotReflectsARIADefaultsWithoutLeaking(t *testing.T) {
	lc, done := openContext(t, `<html><body>`+facePrelude+`<my-input id="a"></my-input>
	  <button id="set">set</button><button id="peek">peek</button>
	<script>
	  var a = document.getElementById('a');
	  a.internals.ariaLabel = 'first';
	  document.getElementById('set').addEventListener('click', function () { a.internals.ariaLabel = 'second'; });
	  document.getElementById('peek').addEventListener('click', function () { log('attrs=' + a.attributes.length); });
	</script></body></html>`, 2*time.Second)
	defer done()

	attr := page.AriaDefaultPrefix + "aria-label"
	if s := snapshot(t, lc); !strings.Contains(s, attr+`="first"`) {
		t.Fatalf("snapshot missing the reflected label:\n%s", s)
	}
	dispatch(t, lc, "#set", "click")
	dispatch(t, lc, "#peek", "click")
	s := snapshot(t, lc)
	if !strings.Contains(s, attr+`="second"`) {
		t.Errorf("snapshot did not track the new label:\n%s", s)
	}
	if !strings.Contains(s, "attrs=1") {
		t.Errorf("page JS observed the reflection attribute:\n%s", s)
	}
}

// openTag returns the start tag of the element with the given id in rendered HTML.
func openTag(t *testing.T, out, id string) string {
	t.Helper()
	i := strings.Index(out, `id="`+id+`"`)
	if i < 0 {
		t.Fatalf("no element #%s in:\n%s", id, out)
	}
	start := strings.LastIndex(out[:i], "<")
	return out[start : i+strings.Index(out[i:], ">")+1]
}
