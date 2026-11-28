package js_test

import (
	"strings"
	"testing"
)

const namedFormPage = `<html><body>
<form id="f" name="search" action="/go">
  <input name="q" value="hello">
  <input id="byid" value="idval">
  <input type="radio" name="size" value="s">
  <input type="radio" name="size" value="m" checked>
  <input type="radio" name="size" value="l">
  <input name="submit" value="clobber">
  <select name="pick"><option value="a">A</option><option value="b" selected>B</option></select>
  <img name="logo" src="/l.png">
  <button id="go" type="button" onclick="q.value = 'from-handler:' + size.value + ':' + search.id">go</button>
</form>
<form name="other"></form>
<img name="hero" src="/h.png"><img name="pair" src="/1.png"><img name="pair" src="/2.png">
<div id="out">x</div>
<script>
  var f = document.getElementById('f'), out = [];
  function put(k, v) { out.push(k + '=' + v); }
  put('q', f.q.value);
  put('byid', f.byid.value);
  put('idx0', f[0] === f.q);
  put('idxmiss', f[99]);
  put('miss', f.nosuch);
  put('radiotype', Object.prototype.toString.call(f.size));
  put('radiolen', f.size.length);
  put('radioval', f.size.value);
  f.size.value = 'l';
  put('radioset', f.size.value + '/' + f.size[2].checked + '/' + f.size[1].checked);
  put('pick', f.pick.value);
  put('img', f.logo.tagName);
  put('builtin', typeof f.submit);
  put('action', f.action.indexOf('/go') >= 0);
  put('els', Object.prototype.toString.call(f.elements));
  put('elsq', f.elements.q === f.q);
  put('elsnamed', f.elements.namedItem('size').length + '/' + (f.elements.namedItem('q') === f.q) + '/' + f.elements.namedItem('zz'));
  put('elssize', f.elements.size.value);
  put('elslen', f.elements.length);
  put('inst', (f instanceof HTMLFormElement) + '/' + (f instanceof HTMLElement) + '/' + (f instanceof Node) + '/' + HTMLElement.prototype.isPrototypeOf(f));
  put('method', typeof f.addEventListener + '/' + typeof f.querySelector + '/' + f.tagName);
  put('expando', (f.mine = 7, f.mine));
  put('docform', document.search === f);
  put('docother', document.other === document.forms[1]);
  put('docimg', document.hero.tagName);
  put('docpair', document.pair.length);
  put('docmiss', document.nosuch);
  put('docinst', (document instanceof Document) + '/' + (document instanceof Node) + '/' + typeof document.addEventListener);
  put('forms', document.forms.length + '/' + (document.forms.namedItem('search') === f));
  put('helpers', typeof __unblinkNamedGet + '/' + typeof __unblinkNamedScope);
  document.getElementById('go').click();
  put('handler', f.q.value);
  document.getElementById('out').textContent = out.join(' | ');
</script></body></html>`

func TestFormAndDocumentNamedAccess(t *testing.T) {
	out := renderWith(t, namedFormPage, nil)
	for _, want := range []string{
		"q=hello", "byid=idval", "idx0=true", "idxmiss=undefined", "miss=undefined",
		"radiotype=[object RadioNodeList]", "radiolen=3", "radioval=m", "radioset=l/true/false",
		"pick=b", "img=IMG", "builtin=function", "action=true",
		"els=[object HTMLFormControlsCollection]", "elsq=true", "elsnamed=3/true/null", "elssize=l", "elslen=8",
		"inst=true/true/true/true", "method=function/function/FORM", "expando=7",
		"docform=true", "docother=true", "docimg=IMG", "docpair=2", "docmiss=undefined",
		"docinst=true/true/function", "forms=2/true", "helpers=undefined/undefined",
		"handler=from-handler:l:f",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if t.Failed() {
		i := strings.Index(out, `<div id="out">`)
		t.Logf("probe: %s", out[i:min(len(out), i+1400)])
	}
	// The radio write is in the tree extraction reads.
	if !strings.Contains(out, `value="l" checked=""`) || strings.Contains(out, `value="m" checked`) {
		t.Errorf("RadioNodeList.value write did not move the checked attribute")
	}
}
