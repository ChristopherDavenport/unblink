package js_test

import (
	"strings"
	"testing"
)

// goja parses neither async generators nor for-await. A classic script that
// contains one is lowered through esbuild rather than skipped whole.
func TestClassicScriptWithAsyncGeneratorRuns(t *testing.T) {
	out := render(t, `<html><body><p id="sync">-</p><p id="async">-</p><script>
		// The rest of the bundle must run even though it only *contains* one.
		document.getElementById('sync').textContent = 'bundle ran';
		var api = {
			stream: async function* (n) { for (var i = 0; i < n; i++) { yield await Promise.resolve(i * 2); } },
			async *other() { yield 'm'; }
		};
		(async function () {
			var seen = [];
			for await (var v of api.stream(3)) seen.push(v);
			for await (var w of api.other()) seen.push(w);
			var it = api.stream(1);
			seen.push(typeof it[Symbol.asyncIterator], (await it.next()).value, (await it.next()).done);
			document.getElementById('async').textContent = seen.join();
		})();
	</script></body></html>`)
	if !strings.Contains(out, `<p id="sync">bundle ran</p>`) {
		t.Errorf("a script containing an async generator was skipped:\n%s", out)
	}
	if !strings.Contains(out, `<p id="async">0,2,4,m,function,0,true</p>`) {
		t.Errorf("async generator / for-await did not run correctly:\n%s", out)
	}
}

// With a dynamic import in the same script: both lowerings apply together.
func TestClassicScriptWithAsyncGeneratorAndDynamicImport(t *testing.T) {
	out := render(t, `<html><body><p id="o">-</p><script>
		var lazy = function () { return import('./never-called.js'); };
		async function* gen() { yield 1; yield 2; }
		(async function () { var s = 0; for await (var v of gen()) s += v; document.getElementById('o').textContent = 'sum ' + s; })();
	</script></body></html>`)
	if !strings.Contains(out, `<p id="o">sum 3</p>`) {
		t.Errorf("got:\n%s", out)
	}
}
