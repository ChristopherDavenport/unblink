package js_test

import (
	"strings"
	"testing"
)

// Intl.DurationFormat must exist ('DurationFormat' in Intl is how an app decides
// to load a polyfill chunk before it boots) and format English durations.
func TestIntlDurationFormat(t *testing.T) {
	out := renderWith(t, `<html><body><div id="out">x</div><script>
	  var o = [];
	  function f(style, d, opts) { opts = opts || {}; if (style) opts.style = style; return new Intl.DurationFormat('en', opts).format(d); }
	  o.push('in=' + ('DurationFormat' in Intl));
	  o.push('long=' + f('long', { hours: 1, minutes: 46, seconds: 40 }));
	  o.push('short=' + f('short', { hours: 1, minutes: 46, seconds: 40 }));
	  o.push('default=' + f(undefined, { days: 2, hours: 1 }));
	  o.push('narrow=' + f('narrow', { hours: 1, minutes: 46, seconds: 40 }));
	  o.push('digital=' + f('digital', { hours: 1, minutes: 46, seconds: 40 }));
	  o.push('digital0=' + f('digital', { minutes: 5, seconds: 3 }));
	  o.push('plural=' + f('long', { years: 1, months: 2, days: 1 }));
	  o.push('neg=' + f('narrow', { hours: -2, minutes: -5 }));
	  o.push('big=' + f('long', { seconds: 1234567 }));
	  o.push('parts=' + new Intl.DurationFormat('en', { style: 'long' }).formatToParts({ hours: 2, minutes: 1 }).map(function (p) { return p.type + ':' + p.value; }).join(';'));
	  o.push('opts=' + new Intl.DurationFormat('en').resolvedOptions().style);
	  try { Intl.DurationFormat('en'); o.push('call=ok'); } catch (e) { o.push('call=' + e.name); }
	  try { f('long', {}); o.push('empty=ok'); } catch (e) { o.push('empty=' + e.name); }
	  try { f('long', { hours: 1, minutes: -1 }); o.push('mixed=ok'); } catch (e) { o.push('mixed=' + e.name); }
	  try { f('bogus', { hours: 1 }); o.push('style=ok'); } catch (e) { o.push('style=' + e.name); }
	  document.getElementById('out').textContent = o.join(' | ');
	</script></body></html>`, nil)
	for _, want := range []string{
		"in=true", "long=1 hour, 46 minutes, 40 seconds", "short=1 hr, 46 min, 40 sec", "default=2 days, 1 hr",
		"narrow=1h 46m 40s", "digital=1:46:40", "digital0=0:05:03", "plural=1 year, 2 months, 1 day",
		"neg=-2h 5m", "big=1,234,567 seconds", "parts=integer:2 hours;literal:, ;integer:1 minute",
		"opts=short", "call=TypeError", "empty=TypeError", "mixed=RangeError", "style=RangeError",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if t.Failed() {
		i := strings.Index(out, `<div id="out">`)
		t.Logf("%s", out[i:min(len(out), i+700)])
	}
}

// The in-memory IndexedDB stub's objects are instances of the IDB interfaces:
// the "idb" wrapper library decides how to wrap a value with instanceof, and a
// missing IDBRequest global was an unhandled ReferenceError on Mastodon.
func TestIndexedDBInterfaceObjects(t *testing.T) {
	out := renderWith(t, `<html><body><div id="out">x</div><script>
	  var o = [];
	  var req = indexedDB.open('db', 1);
	  o.push('open=' + (req instanceof IDBOpenDBRequest) + '/' + (req instanceof IDBRequest) + '/' + Object.prototype.toString.call(req));
	  req.onupgradeneeded = function () { req.result.createObjectStore('s'); };
	  req.onsuccess = function () {
	    var db = req.result, tx = db.transaction('s', 'readwrite'), st = tx.objectStore('s');
	    var put = st.put('v', 'k');
	    o.push('db=' + (db instanceof IDBDatabase) + ' tx=' + (tx instanceof IDBTransaction) + ' store=' + (st instanceof IDBObjectStore) + ' req=' + (put instanceof IDBRequest) + '/' + (put instanceof IDBOpenDBRequest));
	    o.push('cursor=' + typeof IDBCursor.prototype.advance + '/' + (IDBCursorWithValue.prototype instanceof IDBCursor));
	    try { new IDBRequest(); o.push('ctor=ok'); } catch (e) { o.push('ctor=' + e.name); }
	    put.onsuccess = function () {
	      st.get('k').onsuccess = function (ev) { o.push('get=' + this.result); document.getElementById('out').textContent = o.join(' | '); };
	    };
	  };
	</script></body></html>`, nil)
	for _, want := range []string{
		"open=true/true/[object IDBOpenDBRequest]", "db=true tx=true store=true req=true/false",
		"cursor=function/true", "ctor=TypeError", "get=v",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if t.Failed() {
		i := strings.Index(out, `<div id="out">`)
		t.Logf("%s", out[i:min(len(out), i+500)])
	}
}
