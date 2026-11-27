package js_test

import (
	"strings"
	"testing"
)

// passiveProbe renders a page whose script records results as data-* attributes
// on <body>, and fails for every expected attribute that is missing.
func passiveProbe(t *testing.T, script string, want ...string) {
	t.Helper()
	out := renderWith(t, `<html><body><div id="d"><button id="b">x</button></div><script>
		function ev(type) { return new Event(type, { bubbles: true, cancelable: true }); }
		function rec(k, v) { document.body.setAttribute('data-' + k, String(v)); }
		`+script+`
		</script></body></html>`, nil)
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s in:\n%s", w, out)
		}
	}
}

func TestPassiveExplicitIgnoresPreventDefault(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b'), inside;
		b.addEventListener('click', function (e) { e.preventDefault(); inside = e.defaultPrevented; }, { passive: true });
		var e = ev('click');
		rec('ret', b.dispatchEvent(e));
		rec('inside', inside);
		rec('after', e.defaultPrevented);`,
		`data-ret="true"`, `data-inside="false"`, `data-after="false"`)
}

// A passive listener only neuters its own preventDefault: a non-passive listener
// on the same dispatch still cancels the event, and a later passive listener
// observes that cancellation.
func TestPassiveFlagScopedToListener(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b');
		b.addEventListener('click', function (e) { e.preventDefault(); }, { passive: true });
		b.addEventListener('click', function (e) { rec('mid', e.defaultPrevented); e.preventDefault(); });
		document.body.addEventListener('click', function (e) { e.preventDefault(); rec('late', e.defaultPrevented); }, { passive: true });
		var e = ev('click');
		rec('ret', b.dispatchEvent(e));
		rec('after', e.defaultPrevented);`,
		`data-mid="false"`, `data-late="true"`, `data-ret="false"`, `data-after="true"`)
}

func TestPassiveDefaultTargets(t *testing.T) {
	passiveProbe(t, `
		var targets = { win: window, doc: document, html: document.documentElement, body: document.body };
		var types = ['touchstart', 'touchmove', 'wheel', 'mousewheel'];
		var res = [], calls = 0;
		Object.keys(targets).forEach(function (k) {
			types.forEach(function (type) {
				var fn = function (e) { calls++; e.preventDefault(); };
				targets[k].addEventListener(type, fn);
				var e = ev(type);
				document.getElementById('b').dispatchEvent(e);
				targets[k].removeEventListener(type, fn);
				if (e.defaultPrevented) res.push(k + ':' + type);
			});
		});
		rec('calls', calls);
		rec('canceled', res.length ? res.join(',') : 'none');`,
		`data-calls="16"`, `data-canceled="none"`)
}

// The default is false for other event types on those targets, and for the
// touch/wheel types on any other target.
func TestPassiveDefaultFalseElsewhere(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b'), d = document.getElementById('d');
		function canceled(target, type) {
			var fn = function (e) { e.preventDefault(); };
			target.addEventListener(type, fn);
			var e = ev(type);
			b.dispatchEvent(e);
			target.removeEventListener(type, fn);
			return e.defaultPrevented;
		}
		rec('el-wheel', canceled(d, 'wheel'));
		rec('el-touchstart', canceled(b, 'touchstart'));
		rec('win-click', canceled(window, 'click'));
		rec('doc-touchend', canceled(document, 'touchend'));
		rec('body-keydown', canceled(document.body, 'keydown'));
		rec('detached-wheel', (function () {
			var el = document.createElement('body'), e = ev('wheel');
			el.addEventListener('wheel', function (x) { x.preventDefault(); });
			el.dispatchEvent(e);
			return e.defaultPrevented;
		})());`,
		`data-el-wheel="true"`, `data-el-touchstart="true"`, `data-win-click="true"`,
		`data-doc-touchend="true"`, `data-body-keydown="true"`, `data-detached-wheel="true"`)
}

func TestPassiveExplicitOverridesDefault(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b');
		function canceled(target, type, opts) {
			var fn = function (e) { e.preventDefault(); };
			target.addEventListener(type, fn, opts);
			var e = ev(type);
			b.dispatchEvent(e);
			target.removeEventListener(type, fn, opts);
			return e.defaultPrevented;
		}
		rec('win-false', canceled(window, 'wheel', { passive: false }));
		rec('body-false', canceled(document.body, 'touchmove', { passive: false }));
		rec('doc-undef', canceled(document, 'touchstart', { passive: undefined }));
		rec('doc-capture-bool', canceled(document, 'touchstart', true));
		rec('doc-capture-obj', canceled(document, 'wheel', { capture: true }));
		rec('el-true', canceled(b, 'click', { passive: true }));`,
		`data-win-false="true"`, `data-body-false="true"`, `data-doc-undef="false"`,
		`data-doc-capture-bool="false"`, `data-doc-capture-obj="false"`, `data-el-true="false"`)
}

func TestPassiveWithOnceCaptureSignal(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b'), order = [], once = 0, sig = 0;
		var c = new AbortController();
		b.addEventListener('click', function (e) { once++; e.preventDefault(); }, { passive: true, once: true });
		b.addEventListener('click', function (e) { sig++; e.preventDefault(); }, { passive: true, signal: c.signal });
		document.body.addEventListener('click', function (e) { order.push('capture'); e.preventDefault(); }, { passive: true, capture: true });
		b.addEventListener('click', function () { order.push('target'); });
		var e1 = ev('click'); b.dispatchEvent(e1);
		c.abort();
		var e2 = ev('click'); b.dispatchEvent(e2);
		rec('once', once); rec('sig', sig);
		rec('order', order.join(','));
		rec('prevented', e1.defaultPrevented || e2.defaultPrevented);`,
		`data-once="1"`, `data-sig="1"`, `data-order="capture,target,capture,target"`, `data-prevented="false"`)
}

// Listener identity is (type, callback, capture): passive is not part of it, so
// re-adding with a different passive is a no-op and one remove clears it.
func TestPassiveNotPartOfListenerIdentity(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b'), n = 0;
		var fn = function (e) { n++; e.preventDefault(); };
		b.addEventListener('click', fn, { passive: true });
		b.addEventListener('click', fn, { passive: false });
		b.addEventListener('click', fn);
		var e = ev('click'); b.dispatchEvent(e);
		rec('calls', n); rec('prevented', e.defaultPrevented);
		b.addEventListener('click', fn, { capture: true });
		b.dispatchEvent(ev('click'));
		rec('with-capture', n);
		b.removeEventListener('click', fn, { passive: false });
		b.removeEventListener('click', fn, true);
		b.dispatchEvent(ev('click'));
		rec('after-remove', n);`,
		`data-calls="1"`, `data-prevented="false"`, `data-with-capture="3"`, `data-after-remove="3"`)
}

func TestPassiveNestedDispatch(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b'), d = document.getElementById('d');
		// A non-passive listener cancels the inner event while the outer passive
		// listener is still running.
		d.addEventListener('inner', function (e) { e.preventDefault(); });
		var innerRet, outerInside;
		b.addEventListener('outer', function (e) {
			innerRet = d.dispatchEvent(ev('inner'));
			e.preventDefault(); // still inside the passive listener after the nested dispatch
			outerInside = e.defaultPrevented;
		}, { passive: true });
		rec('outer-ret', b.dispatchEvent(ev('outer')));
		rec('inner-ret', innerRet);
		rec('outer-inside', outerInside);

		// The reverse: a non-passive outer listener nests a dispatch that has a
		// passive listener, then cancels its own event.
		d.addEventListener('inner2', function (e) { e.preventDefault(); }, { passive: true });
		var inner2Ret;
		b.addEventListener('outer2', function (e) {
			inner2Ret = d.dispatchEvent(ev('inner2'));
			e.preventDefault();
		});
		rec('outer2-ret', b.dispatchEvent(ev('outer2')));
		rec('inner2-ret', inner2Ret);`,
		`data-outer-ret="true"`, `data-inner-ret="false"`, `data-outer-inside="false"`,
		`data-outer2-ret="false"`, `data-inner2-ret="true"`)
}

func TestPassiveFlagRestoredAfterThrow(t *testing.T) {
	passiveProbe(t, `
		var b = document.getElementById('b');
		b.addEventListener('click', function (e) { throw new Error('boom'); }, { passive: true });
		b.addEventListener('click', function (e) { e.preventDefault(); });
		var e = ev('click');
		rec('ret', b.dispatchEvent(e));
		rec('prevented', e.defaultPrevented);`,
		`data-ret="false"`, `data-prevented="true"`)
}
