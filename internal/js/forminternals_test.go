package js_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Form-associated custom elements (ElementInternals): a design-system control such
// as <my-input name="email"> must take part in its form the way a native control
// does — for page JS (FormData, form.elements, validation, submission) and for
// unblink's own form extraction, which only ever reads the node tree.

// facePrelude is the shared page head: a log() that mirrors its lines into #out,
// thrown() to name an exception, and a minimal form-associated <my-input> that
// records every form* lifecycle callback.
const facePrelude = `<pre id="out"></pre><script>
  var L = [];
  function log(x) { L.push(String(x)); document.getElementById('out').textContent = L.join('|'); }
  function thrown(fn) { try { fn(); return 'no-throw'; } catch (e) { return e.name; } }
  class MyInput extends HTMLElement {
    static formAssociated = true;
    constructor() { super(); this.internals = this.attachInternals(); this.calls = []; }
    formAssociatedCallback(form) { this.calls.push('assoc:' + (form ? form.id : 'null')); }
    formDisabledCallback(disabled) { this.calls.push('disabled:' + disabled); }
    formResetCallback() { this.calls.push('reset'); this.internals.setFormValue(null); }
  }
  customElements.define('my-input', MyInput);
</script>`

var outRE = regexp.MustCompile(`(?s)<pre id="out">(.*?)</pre>`)

// faceLog renders body after facePrelude and returns the page's log lines.
func faceLog(t *testing.T, body string) []string {
	t.Helper()
	out := render(t, `<html><body>`+facePrelude+body+`</body></html>`)
	m := outRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no #out in render:\n%s", out)
	}
	return strings.Split(m[1], "|")
}

func wantLog(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("log mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestAttachInternalsRules(t *testing.T) {
	got := faceLog(t, `<my-input id="a"></my-input><x-plain id="p"></x-plain><x-off id="o"></x-off>
	<script>
	  class XPlain extends HTMLElement {}
	  customElements.define('x-plain', XPlain);
	  class XOff extends HTMLElement { static get disabledFeatures() { return ['internals']; } }
	  customElements.define('x-off', XOff);
	  var a = document.getElementById('a');
	  log(a.internals instanceof ElementInternals);
	  log(thrown(function () { a.attachInternals(); }));                        // second call
	  log(thrown(function () { document.createElement('div').attachInternals(); })); // not custom
	  log(thrown(function () { document.createElement('un-defined').attachInternals(); }));
	  log(thrown(function () { document.getElementById('o').attachInternals(); })); // disabledFeatures
	  log(thrown(function () { new ElementInternals(); }));
	  // A custom element that is not form-associated still gets internals, but the
	  // form-participation members refuse.
	  var pi = document.getElementById('p').attachInternals();
	  log(thrown(function () { pi.setFormValue('x'); }));
	  log(thrown(function () { return pi.form; }));
	  log(thrown(function () { return pi.validity; }));
	  log(thrown(function () { pi.states.add('ok'); pi.ariaLabel = 'fine'; }));
	</script>`)
	wantLog(t, got, "true", "NotSupportedError", "NotSupportedError", "NotSupportedError",
		"NotSupportedError", "TypeError", "NotSupportedError", "NotSupportedError", "NotSupportedError", "no-throw")
}

func TestSetFormValueReachesFormData(t *testing.T) {
	got := faceLog(t, `<form id="f">
	    <input name="first" value="1">
	    <my-input id="a" name="email"></my-input>
	    <my-input id="nameless"></my-input>
	    <input name="last" value="9">
	  </form>
	<script>
	  var f = document.getElementById('f'), a = document.getElementById('a');
	  function entries() { return Array.from(new FormData(f).entries()).map(function (p) {
	    return p[0] + '=' + (p[1] instanceof File ? 'File(' + p[1].name + ')' : p[1]); }).join(';'); }
	  log(entries());                                    // no value set: contributes nothing
	  a.internals.setFormValue('a@b.c');
	  document.getElementById('nameless').internals.setFormValue('ignored');
	  log(entries());                                    // string, in tree order; nameless skipped
	  a.internals.setFormValue(new File(['hi'], 'cv.txt'));
	  log(entries());
	  var fd = new FormData(); fd.append('from', '1'); fd.append('to', '2');
	  a.internals.setFormValue(fd, 'state');
	  fd.append('late', 'x');                            // the value was copied
	  log(entries());                                    // FormData entries keep their own names
	  a.internals.setFormValue(null);
	  log(entries());
	  log(thrown(function () { a.internals.setFormValue(); }));
	</script>`)
	wantLog(t, got,
		"first=1;last=9",
		"first=1;email=a@b.c;last=9",
		"first=1;email=File(cv.txt);last=9",
		"first=1;from=1;to=2;last=9",
		"first=1;last=9",
		"TypeError")
}

func TestInternalsFormLabelsAndElements(t *testing.T) {
	got := faceLog(t, `<form id="f">
	    <label id="l1" for="a">Email</label>
	    <label id="l2"><span>Wrapped</span><my-input id="b" name="b"></my-input></label>
	    <my-input id="a" name="a"></my-input>
	    <input id="n" name="n">
	  </form>
	  <my-input id="loose"></my-input>
	<script>
	  var f = document.getElementById('f'), a = document.getElementById('a'), b = document.getElementById('b');
	  log(a.internals.form === f);
	  log(document.getElementById('loose').internals.form);
	  log(Array.from(a.internals.labels).map(function (l) { return l.id; }).join(','));
	  log(Array.from(b.internals.labels).map(function (l) { return l.id; }).join(','));
	  log(Array.from(f.elements).map(function (e) { return e.id; }).join(','));
	  log(f.elements.namedItem('a') === a);
	</script>`)
	wantLog(t, got, "true", "null", "l1", "l2", "b,a,n", "true")
}

func TestInternalsValidity(t *testing.T) {
	got := faceLog(t, `<form id="f"><my-input id="a" name="a"><span id="in"></span></my-input><input name="n"></form>
	  <span id="outside"></span>
	<script>
	  var f = document.getElementById('f'), a = document.getElementById('a'), it = a.internals;
	  var fired = 0; a.addEventListener('invalid', function (e) { fired++; log('invalid:' + e.cancelable + ':' + e.bubbles); });
	  log(it.willValidate + ':' + it.validity.valid + ':' + it.validationMessage + ':' + it.checkValidity() + ':' + f.checkValidity());
	  it.setValidity({ valueMissing: true }, 'Required', document.getElementById('in'));
	  log(it.validity.valid + ':' + it.validity.valueMissing + ':' + it.validity.customError + ':' + it.validationMessage);
	  log(it.validity instanceof ValidityState);
	  log(it.checkValidity());
	  log(it.reportValidity());
	  log(f.checkValidity());
	  log(f.reportValidity());
	  log(fired);
	  log(thrown(function () { it.setValidity({ customError: true }); }));         // message required
	  log(thrown(function () { it.setValidity({ customError: true }, 'm', document.getElementById('outside')); }));
	  // Barred from validation while disabled or readonly: reports valid, fires nothing.
	  a.setAttribute('disabled', '');
	  log(it.willValidate + ':' + it.checkValidity() + ':' + f.checkValidity());
	  a.removeAttribute('disabled'); a.setAttribute('readonly', '');
	  log(it.willValidate + ':' + it.checkValidity());
	  a.removeAttribute('readonly');
	  it.setValidity({});
	  log(it.validity.valid + ':' + it.validationMessage + ':' + f.checkValidity() + ':' + fired);
	  log(typeof document.createElement('div').checkValidity);                    // form-only
	</script>`)
	wantLog(t, got,
		"true:true::true:true",
		"false:true:false:Required",
		"true",
		"invalid:true:false", "false",
		"invalid:true:false", "false",
		"invalid:true:false", "false",
		"invalid:true:false", "false",
		"4",
		"TypeError", "NotFoundError",
		"false:true:true",
		"false:true",
		"true::true:4",
		"undefined")
}

func TestFormLifecycleCallbacks(t *testing.T) {
	got := faceLog(t, `<form id="f1"><fieldset id="fs"><legend><my-input id="inlegend"></my-input></legend>
	      <my-input id="a" name="a"></my-input></fieldset></form>
	  <form id="f2"></form>
	  <form id="f3"><fieldset disabled><my-input id="born"></my-input></fieldset></form>
	<script>
	  var a = document.getElementById('a'), fs = document.getElementById('fs');
	  function take(el) { var c = el.calls.join(','); el.calls = []; return c || '-'; }
	  log(take(a));                                        // associated at upgrade
	  log(take(document.getElementById('born')));          // ...and disabled by its fieldset
	  fs.setAttribute('disabled', '');                     // ancestor <fieldset disabled>
	  log(take(a));
	  log(take(document.getElementById('inlegend')));      // first <legend> is exempt
	  fs.removeAttribute('disabled');
	  log(take(a));
	  a.setAttribute('disabled', ''); a.setAttribute('disabled', 'again');   // no change, no call
	  log(take(a));
	  a.removeAttribute('disabled');
	  log(take(a));
	  document.getElementById('f2').appendChild(a);        // moved to another form
	  log(take(a));
	  a.remove();                                          // owner lost
	  log(take(a));
	  var fresh = document.createElement('my-input');      // created detached: no callbacks
	  log(take(fresh));
	  document.getElementById('f2').appendChild(fresh);
	  log(take(fresh));

	  // form.reset(): a cancelable reset event, then formResetCallback.
	  var f2 = document.getElementById('f2');
	  fresh.internals.setFormValue('typed');
	  var cancel = function (e) { e.preventDefault(); };
	  f2.addEventListener('reset', cancel); f2.reset(); f2.removeEventListener('reset', cancel);
	  log(take(fresh) + ':' + new FormData(f2).has('x'));
	  fresh.setAttribute('name', 'x');
	  log(new FormData(f2).get('x'));
	  f2.reset();
	  log(take(fresh) + ':' + new FormData(f2).has('x'));
	</script>`)
	wantLog(t, got,
		"assoc:f1",
		"assoc:f3,disabled:true",
		"disabled:true",
		"assoc:f1",
		"disabled:false",
		"disabled:true",
		"disabled:false",
		"assoc:f2",
		"assoc:null",
		"-",
		"assoc:f2",
		"-:false",
		"typed",
		"reset:false")
}

func TestFormSubmissionIncludesInternalsValue(t *testing.T) {
	const form = `<form id="f" method="GET" action="/go"%s>
	    <input name="q" value="x">
	    <my-input id="a" name="email"></my-input>
	    <fieldset disabled><my-input id="off" name="off"></my-input></fieldset>
	  </form>
	<script>
	  var f = document.getElementById('f'), a = document.getElementById('a');
	  a.internals.setFormValue('a@b.c');
	  document.getElementById('off').internals.setFormValue('never');
	  %s
	</script>`
	nav := func(t *testing.T, formAttrs, script string) string {
		t.Helper()
		body := strings.Replace(strings.Replace(form, "%s", formAttrs, 1), "%s", script, 1)
		_, diag, _ := renderWaitDiag(t, `<html><body>`+facePrelude+body+`</body></html>`, js.Env{}, time.Second)
		return diag.PendingNavigation
	}

	if got := nav(t, "", `f.submit();`); !strings.HasSuffix(got, "/go?email=a%40b.c&q=x") {
		t.Errorf("submit() navigation = %q; want the internals value under its name, the disabled one absent", got)
	}
	// requestSubmit() validates first: an invalid element blocks the submission...
	invalid := `a.internals.setValidity({ customError: true }, 'nope'); f.requestSubmit();`
	if got := nav(t, "", invalid); got != "" {
		t.Errorf("requestSubmit() with an invalid element navigated to %q", got)
	}
	// ...unless the form opts out, and submit() never validates.
	if got := nav(t, " novalidate", invalid); !strings.Contains(got, "email=a%40b.c") {
		t.Errorf("novalidate requestSubmit() navigation = %q", got)
	}
	if got := nav(t, "", `a.internals.setValidity({ customError: true }, 'nope'); f.submit();`); !strings.Contains(got, "email=a%40b.c") {
		t.Errorf("submit() must skip validation; navigation = %q", got)
	}
}

func TestInternalsStatesAriaAndShadowRoot(t *testing.T) {
	got := faceLog(t, `<x-closed id="c"></x-closed>
	<script>
	  class XClosed extends HTMLElement {
	    constructor() { super(); this.root = this.attachShadow({ mode: 'closed' }); this.internals = this.attachInternals(); }
	  }
	  customElements.define('x-closed', XClosed);
	  var c = document.getElementById('c'), it = c.internals;
	  log(c.shadowRoot === null && it.shadowRoot === c.root);   // closed root still reachable
	  log(it.states instanceof CustomStateSet);
	  it.states.add('checked'); it.states.add('checked'); it.states.add('--legacy');
	  log(it.states.size + ':' + it.states.has('checked') + ':' + Array.from(it.states).join(','));
	  log(it.states.delete('checked') + ':' + it.states.has('checked'));
	  it.states.clear(); log(it.states.size);
	  log(it.role + ':' + it.ariaLabel);
	  it.role = 'checkbox'; it.ariaLabel = 'Agree'; it.ariaChecked = true;
	  log(it.role + ':' + it.ariaLabel + ':' + it.ariaChecked + ':' + typeof it.ariaChecked);
	  it.ariaLabel = null; log(it.ariaLabel);
	  log(c.hasAttribute('role') || c.hasAttribute('aria-label'));  // internals never write attributes
	</script>`)
	wantLog(t, got, "true", "true", "2:true:checked,--legacy", "true:false", "0",
		"null:null", "checkbox:Agree:true:string", "null", "false")
}

// The engine reflects each form-associated element's entry-list contribution onto
// the rendered tree (page.FormValueAttr), which is what form extraction reads.
func TestRenderReflectsFormValueOntoTree(t *testing.T) {
	out := render(t, `<html><body>`+facePrelude+`<form>
	    <my-input id="a" name="email"></my-input>
	    <my-input id="b" name="range"></my-input>
	    <my-input id="c" name="empty"></my-input>
	    <my-input id="d" name="bad"></my-input>
	    <my-input id="e" name="off" disabled></my-input>
	  </form>
	<script>
	  document.getElementById('a').internals.setFormValue('a@b.c & co');
	  var fd = new FormData(); fd.append('from', '1'); fd.append('to', '2'); fd.append('doc', new File(['x'], 'x.txt'));
	  document.getElementById('b').internals.setFormValue(fd);
	  document.getElementById('d').internals.setFormValue('v');
	  document.getElementById('d').internals.setValidity({ customError: true }, 'nope');
	  document.getElementById('e').internals.setFormValue('never');
	</script></body></html>`)
	for id, want := range map[string]string{
		"a": page.FormValueAttr + `="email=a%40b.c+%26+co"`,
		"b": page.FormValueAttr + `="from=1&amp;to=2"`, // the File entry is not reflected
		"c": page.FormValueAttr + `=""`,
		"d": page.FormInvalidAttr + `=""`,
		"e": page.FormValueAttr + `=""`, // disabled: submits nothing
	} {
		tag := regexp.MustCompile(`<my-input id="` + id + `"[^>]*>`).FindString(out)
		if !strings.Contains(tag, want) {
			t.Errorf("element %s rendered as %s; want it to carry %s", id, tag, want)
		}
	}
}

// A live session's snapshot carries the reflection too, tracks later changes, and
// never lets page JS observe the attribute.
func TestSnapshotReflectsFormValueWithoutLeaking(t *testing.T) {
	lc, done := openContext(t, `<html><body>`+facePrelude+`<form><my-input id="a" name="email"></my-input></form>
	  <button id="set">set</button><button id="peek">peek</button>
	<script>
	  var a = document.getElementById('a');
	  a.internals.setFormValue('first');
	  document.getElementById('set').addEventListener('click', function () { a.internals.setFormValue('second'); });
	  document.getElementById('peek').addEventListener('click', function () {
	    log('leaked=' + a.hasAttribute('`+page.FormValueAttr+`'));
	  });
	</script></body></html>`, 2*time.Second)
	defer done()

	if s := snapshot(t, lc); !strings.Contains(s, page.FormValueAttr+`="email=first"`) {
		t.Fatalf("snapshot missing the reflected value:\n%s", s)
	}
	dispatch(t, lc, "#set", "click")
	dispatch(t, lc, "#peek", "click")
	s := snapshot(t, lc)
	if !strings.Contains(s, page.FormValueAttr+`="email=second"`) {
		t.Errorf("snapshot did not track the new value:\n%s", s)
	}
	if !strings.Contains(s, "leaked=false") {
		t.Errorf("page JS observed the reflection attribute:\n%s", s)
	}
}
