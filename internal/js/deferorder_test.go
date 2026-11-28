package js_test

import (
	"strings"
	"testing"
)

// Deferred external scripts and module scripts wait for the end of parsing and
// then run in document order, after every other classic script. The common
// shape this protects: a library loaded with defer in <head> (Alpine) that
// starts when it runs, configured by an inline script further down the page.
func TestDeferredScriptsRunAfterParsing(t *testing.T) {
	out, _, diag := renderModules(t, `<html><head>
		<script defer src="/lib.js"></script>
		<script type="module" src="/mod.mjs"></script>
		<script defer src="/lib2.js"></script>
		<script async src="/async.js"></script>
		<script defer>window.order.push('inline-defer-is-not-deferred:' + document.readyState);</script>
	</head><body><div id="out"></div>
		<script>window.order = window.order || []; window.order.push('inline:' + document.readyState);
			document.addEventListener('lib:init', function () { window.registered = true; });
			document.addEventListener('DOMContentLoaded', function () { window.order.push('DCL'); document.getElementById('out').textContent = window.order.join(' ') + ' started=' + window.started; });
		</script>
	</body></html>`, map[string]string{
		origin + "/lib.js":   `window.order.push('defer1:' + document.readyState); document.dispatchEvent(new Event('lib:init')); window.started = window.registered === true;`,
		origin + "/mod.mjs":  `window.order.push('module');`,
		origin + "/lib2.js":  `window.order.push('defer2');`,
		origin + "/async.js": `window.order = window.order || []; window.order.push('async');`,
	})
	want := "async inline-defer-is-not-deferred:loading inline:loading defer1:interactive module defer2 DCL started=true"
	if out != want {
		t.Errorf("script order:\n got %q\nwant %q\nerrors: %v", out, want, diag.Errors)
	}
}

// Without modules or deferred scripts nothing changes.
func TestScriptOrderWithoutDefer(t *testing.T) {
	out := render(t, `<html><body><p id="o"></p><script>window.o = ['a'];</script><script>window.o.push('b'); document.getElementById('o').textContent = window.o.join();</script></body></html>`)
	if !strings.Contains(out, `<p id="o">a,b</p>`) {
		t.Errorf("got:\n%s", out)
	}
}
