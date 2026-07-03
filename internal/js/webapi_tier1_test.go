package js_test

import (
	"strings"
	"testing"
	"time"
)

// --- Batch A: element traversal gaps + CSS namespace ---

// TestElementTraversalGaps proves the small Element.prototype additions libraries
// touch while walking the tree: lastElementChild, getAttributeNode, and
// namespaceURI/prefix (HTML default vs. an inline SVG subtree).
func TestElementTraversalGaps(t *testing.T) {
	out := render(t, `<html><body>
		<ul id="list"><li id="a">A</li><li id="b">B</li><li id="c">C</li></ul>
		<a id="link" href="/x" data-k="v">link</a>
		<svg id="s"><rect></rect></svg>
		<div id="out"></div>
		<script>
		  var list = document.getElementById('list');
		  var link = document.getElementById('link');
		  var attr = link.getAttributeNode('data-k');
		  var missing = link.getAttributeNode('nope');
		  var svg = document.getElementById('s');
		  document.getElementById('out').textContent = [
		    'last=' + list.lastElementChild.id,
		    'first=' + list.firstElementChild.id,
		    'attrName=' + attr.name,
		    'attrVal=' + attr.value,
		    'missing=' + (missing === null),
		    'nsHTML=' + link.namespaceURI,
		    'nsSVG=' + svg.namespaceURI,
		    'prefix=' + (link.prefix === null)
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{
		"last=c", "first=a", "attrName=data-k", "attrVal=v", "missing=true",
		"nsHTML=http://www.w3.org/1999/xhtml", "nsSVG=http://www.w3.org/2000/svg", "prefix=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("element traversal gaps missing %q:\n%s", want, out)
		}
	}
}

// TestCSSNamespace proves CSS.escape follows the WHATWG serialize-an-identifier
// algorithm (real, not a stub) and CSS.supports is optimistic.
func TestCSSNamespace(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  document.getElementById('out').textContent = [
		    'esc=' + CSS.escape('foo#bar'),
		    'escDigit=' + CSS.escape('1a'),
		    'escLead=' + CSS.escape('-'),
		    'sup=' + CSS.supports('display', 'grid'),
		    'supCond=' + CSS.supports('(display: grid)')
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{
		`esc=foo\#bar`, `escDigit=\31 a`, `escLead=\-`, "sup=true", "supCond=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CSS namespace missing %q:\n%s", want, out)
		}
	}
}

// --- Batch B: navigator stubs + EventSource ---

// TestNavigatorStubs proves the device/permission surfaces resolve/deny instead
// of throwing, that serviceWorker.ready resolves (no await-hang), and — via the
// geolocation callback that writes #out — that the wrapped-timer async path is
// settle-visible (the content only appears after the deferred callback fires).
func TestNavigatorStubs(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var r = [];
		  r.push('sw=' + (typeof navigator.serviceWorker.register === 'function'));
		  r.push('perm=' + (typeof navigator.permissions.query === 'function'));
		  r.push('media=' + (typeof navigator.mediaDevices.getUserMedia === 'function'));
		  r.push('clip=' + (typeof navigator.clipboard.readText === 'function'));
		  var mediaRejected = false;
		  navigator.mediaDevices.getUserMedia({ video: true }).catch(function (e) { mediaRejected = e.name === 'NotAllowedError'; });
		  Promise.all([
		    navigator.clipboard.readText(),
		    navigator.permissions.query({ name: 'geolocation' }),
		    navigator.serviceWorker.ready,
		    navigator.serviceWorker.register('/sw.js')
		  ]).then(function (res) {
		    r.push('clipVal=[' + res[0] + ']');
		    r.push('permState=' + res[1].state);
		    r.push('ready=' + (res[2] === res[3]));
		    navigator.geolocation.getCurrentPosition(function () {}, function (err) {
		      r.push('geo=' + (err.code === 2));
		      r.push('media=' + mediaRejected);
		      document.getElementById('out').textContent = r.join(' ');
		    });
		  });
		</script></body></html>`)
	for _, want := range []string{
		"sw=true", "perm=true", "clip=true", "clipVal=[]", "permState=denied",
		"ready=true", "geo=true", "media=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("navigator stubs missing %q:\n%s", want, out)
		}
	}
}

// TestEventSourceStub proves EventSource constructs without throwing and reports
// a single error -> CLOSED through the wrapped timer (the onerror handler is what
// writes #out, so a missing settle wait would drop the content).
func TestEventSourceStub(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var es = new EventSource('/stream');
		  var initial = es.readyState;
		  es.onerror = function () {
		    document.getElementById('out').textContent =
		      'initial=' + initial + ' closed=' + (es.readyState === EventSource.CLOSED) + ' url=' + es.url;
		  };
		</script></body></html>`)
	for _, want := range []string{"initial=0", "closed=true", "url=/stream"} {
		if !strings.Contains(out, want) {
			t.Errorf("EventSource stub missing %q:\n%s", want, out)
		}
	}
}

// --- Batch C: FileReader + Streams ---

// TestFileReader proves the three read modes over a Blob's __bytes, with the
// content written from onload (the wrapped-timer completion) — the settle proof.
func TestFileReader(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var blob = new Blob(['héllo'], { type: 'text/plain' });
		  var done = {};
		  function check() {
		    if ('text' in done && 'url' in done && 'buf' in done) {
		      document.getElementById('out').textContent =
		        'text=' + done.text + ' url=' + done.url + ' buf=' + done.buf;
		    }
		  }
		  var r1 = new FileReader();
		  r1.onload = function () { done.text = r1.result; check(); };
		  r1.readAsText(blob);
		  var r2 = new FileReader();
		  r2.addEventListener('load', function () { done.url = r2.result; check(); });
		  r2.readAsDataURL(blob);
		  var r3 = new FileReader();
		  r3.onload = function () { done.buf = r3.result.byteLength; check(); };
		  r3.readAsArrayBuffer(blob);
		</script></body></html>`)
	for _, want := range []string{"text=héllo", "url=data:text/plain;base64,aMOpbGxv", "buf=6"} {
		if !strings.Contains(out, want) {
			t.Errorf("FileReader missing %q:\n%s", want, out)
		}
	}
}

// TestStreams proves a buffer-backed ReadableStream drains via the microtask
// read() chain (settle drains it) and a TransformStream pipes write->transform->read.
func TestStreams(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var results = {};
		  function drain(reader, key, next) {
		    var chunks = [];
		    (function pump() {
		      return reader.read().then(function (res) {
		        if (res.done) { results[key] = chunks.join(''); next(); return; }
		        chunks.push(res.value); return pump();
		      });
		    })();
		  }
		  function finish() {
		    document.getElementById('out').textContent = 'rs=' + results.rs + ' ts=' + results.ts;
		  }
		  var rs = new ReadableStream({ start: function (c) { c.enqueue('a'); c.enqueue('b'); c.enqueue('c'); c.close(); } });
		  var ts = new TransformStream({ transform: function (chunk, c) { c.enqueue(chunk.toUpperCase()); } });
		  var w = ts.writable.getWriter();
		  w.write('x'); w.write('y'); w.close();
		  drain(rs.getReader(), 'rs', function () { drain(ts.readable.getReader(), 'ts', finish); });
		</script></body></html>`)
	for _, want := range []string{"rs=abc", "ts=XY"} {
		if !strings.Contains(out, want) {
			t.Errorf("Streams missing %q:\n%s", want, out)
		}
	}
}

// --- Batch D: canvas getContext stub ---

// TestCanvasContextStub proves getContext('2d') returns an inert context whose
// draw calls don't throw (so the surrounding DOM still renders), that webgl and
// non-canvas elements return null, and toDataURL is a constant empty data URL.
func TestCanvasContextStub(t *testing.T) {
	out := render(t, `<html><body>
		<canvas id="c" width="300" height="150"></canvas>
		<div id="content">real content here</div>
		<div id="out">?</div>
		<script>
		  var c = document.getElementById('c');
		  var ctx = c.getContext('2d');
		  ctx.fillStyle = '#f00';
		  ctx.fillRect(0, 0, 100, 100);
		  ctx.beginPath(); ctx.arc(50, 50, 40, 0, Math.PI * 2); ctx.fill();
		  var w = ctx.measureText('hello').width;
		  var gl = c.getContext('webgl');
		  document.getElementById('out').textContent = [
		    'has2d=' + (ctx !== null),
		    'feature=' + (!!c.getContext),
		    'measure=' + w,
		    'glNull=' + (gl === null),
		    'divNull=' + (document.getElementById('content').getContext('2d') === null),
		    'dataurl=' + c.toDataURL()
		  ].join(' ');
		</script></body></html>`)
	for _, want := range []string{
		"has2d=true", "feature=true", "measure=0", "glNull=true", "divNull=true",
		"dataurl=data:,", "real content here",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("canvas stub missing %q:\n%s", want, out)
		}
	}
}

// --- Batch E: indexedDB in-memory stub ---

// TestIndexedDBStub drives a full open -> upgrade -> put -> get -> getAll chain.
// Every step completes through the wrapped setTimeout, and the content is written
// only from the innermost getAll onsuccess — so this is the ADR-0004 settle proof:
// if any callback escaped the audit, the render would close early and #out stay "?".
func TestIndexedDBStub(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var open = indexedDB.open('appdb', 1);
		  open.onupgradeneeded = function (e) {
		    e.target.result.createObjectStore('items', { keyPath: 'id' });
		  };
		  open.onsuccess = function (e) {
		    var db = e.target.result;
		    var store = db.transaction('items', 'readwrite').objectStore('items');
		    store.put({ id: 1, name: 'alpha' });
		    store.put({ id: 2, name: 'beta' });
		    var getReq = store.get(1);
		    getReq.onsuccess = function () {
		      var one = getReq.result;
		      var allReq = store.getAll();
		      allReq.onsuccess = function () {
		        document.getElementById('out').textContent =
		          'ver=' + db.version + ' one=' + one.name + ' count=' + allReq.result.length +
		          ' names=' + allReq.result.map(function (r) { return r.name; }).join(',');
		      };
		    };
		  };
		</script></body></html>`)
	for _, want := range []string{"ver=1", "one=alpha", "count=2", "names=alpha,beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("indexedDB stub missing %q:\n%s", want, out)
		}
	}
}

// TestTier1LiveContext confirms every Tier 1 surface is also installed in the
// persistent (interact/session) runtime, not just one-shot Render.
func TestTier1LiveContext(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body><div id="out">?</div>
		<script>
		  document.getElementById('out').textContent = [
		    'css=' + (typeof CSS.escape === 'function'),
		    'fr=' + (typeof FileReader === 'function'),
		    'rs=' + (typeof ReadableStream === 'function'),
		    'idb=' + (typeof indexedDB.open === 'function'),
		    'es=' + (typeof EventSource === 'function'),
		    'canvas=' + (typeof document.createElement('canvas').getContext === 'function'),
		    'clip=' + (typeof navigator.clipboard.writeText === 'function'),
		    'lastChild=' + ('lastElementChild' in document.body)
		  ].join(' ');
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	snap := snapshot(t, lc)
	for _, want := range []string{"css=true", "fr=true", "rs=true", "idb=true", "es=true", "canvas=true", "clip=true", "lastChild=true"} {
		if !strings.Contains(snap, want) {
			t.Errorf("live context missing %q:\n%s", want, snap)
		}
	}
}
