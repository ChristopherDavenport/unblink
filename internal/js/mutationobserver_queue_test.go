package js_test

import (
	"strings"
	"testing"
)

// Each observer has its own record queue, filled at mutation time. The cases
// below are the stop / mutate / start pattern editors such as CodeMirror use:
// a mutation made while an observer is disconnected must never reach it, and
// a re-observed observer must receive records and callbacks again.
func TestMutationObserverPerObserverQueue(t *testing.T) {
	out := renderWith(t, `<html><body><div id="host"></div><div id="out"></div><script>
		function rec(k, v) { document.body.setAttribute('data-' + k, String(v)); }
		var opts = {childList: true, characterData: true, subtree: true, attributes: true};
		var el = document.createElement('div');
		// A second, always-connected observer on the same node: before the fix its
		// presence kept records in a shared queue that the first observer then stole.
		var otherCalls = 0;
		new MutationObserver(function (r) { otherCalls += r.length; }).observe(el, opts);

		var calls = 0;
		var mo = new MutationObserver(function (r) { calls += r.length; });
		mo.observe(el, opts);
		mo.disconnect();
		el.appendChild(document.createElement('span'));
		el.setAttribute('x', '1');
		mo.observe(el, opts);
		rec('while-disconnected', mo.takeRecords().length);

		el.setAttribute('y', '1');
		rec('while-observing', mo.takeRecords().length);
		rec('after-take', mo.takeRecords().length);

		el.setAttribute('z', '1');
		mo.disconnect();
		rec('disconnect-clears', mo.takeRecords().length);

		mo.observe(el, opts);
		mo.observe(el, opts); // observing the same node twice must not double records
		document.getElementById('host').appendChild(document.createElement('i'));
		rec('unrelated-tree', mo.takeRecords().length);
		el.setAttribute('w', '1');
		rec('no-duplicates', mo.takeRecords().length);

		el.setAttribute('v', '1'); // left queued: delivered by callback after re-observe
		Promise.resolve().then(function () { return Promise.resolve(); }).then(function () {
			rec('callback-after-reobserve', calls);
			rec('other-observer', otherCalls);
		});
		</script></body></html>`, nil)
	for _, w := range []string{
		`data-while-disconnected="0"`, `data-while-observing="1"`, `data-after-take="0"`,
		`data-disconnect-clears="0"`, `data-unrelated-tree="0"`, `data-no-duplicates="1"`,
		`data-callback-after-reobserve="1"`, `data-other-observer="6"`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in:\n%s", w, out)
		}
	}
}
