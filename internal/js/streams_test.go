package js_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/js"
)

// A read() on an open, empty stream stays pending until the producer enqueues —
// it must not resolve {done: true} (which a reader takes for "connection
// closed"). This is the Next.js App Router pattern: the controller is captured in
// start() and fed later by inline scripts.
func TestStreamReadPendsUntilEnqueue(t *testing.T) {
	got := evalJS(t, ``, `
		var ctl, log = [];
		var rs = new ReadableStream({ start: function (c) { ctl = c; } });
		var reader = rs.getReader();
		(function pump() {
			reader.read().then(function (r) {
				if (r.done) { log.push('done'); done(log.join(',')); return; }
				log.push(r.value); pump();
			});
		})();
		setTimeout(function () { log.push('t1'); ctl.enqueue('a'); }, 5);
		setTimeout(function () { ctl.enqueue('b'); ctl.close(); }, 15);`)
	if got != "t1,a,b,done" {
		t.Errorf("got %q, want the read to wait for the late enqueue", got)
	}
}

// A read nobody ever answers must not hold the render open: the page settles
// and the read simply stays pending.
func TestStreamPendingReadDoesNotHoldSettle(t *testing.T) {
	got, diag := evalIn(t, "", ``, `
		var rs = new ReadableStream({ start: function () {} });
		var state = 'pending';
		rs.getReader().read().then(function () { state = 'resolved'; }, function () { state = 'rejected'; });
		Promise.resolve().then(function () { done(state); });`, js.Env{})
	if got != "pending" {
		t.Errorf("got %q, want pending", got)
	}
	if len(diag.Errors) != 0 {
		t.Errorf("unexpected errors: %v", diag.Errors)
	}
}

func TestStreamSemantics(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"close after queued chunks drains first", `
			var rs = new ReadableStream({ start: function (c) { c.enqueue(1); c.enqueue(2); c.close(); } });
			var r = rs.getReader(), out = [];
			r.read().then(function (a) { out.push(a.value); return r.read(); })
			 .then(function (a) { out.push(a.value); return r.read(); })
			 .then(function (a) { out.push(a.done); done(out.join(',')); });`, "1,2,true"},
		{"error rejects pending and later reads", `
			var ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			var r = rs.getReader(), out = [];
			r.read().then(null, function (e) { out.push('p:' + e.message); return r.read(); })
			 .then(null, function (e) { out.push('l:' + e.message); done(out.join(',')); });
			ctl.error(new Error('boom'));`, "p:boom,l:boom"},
		{"pull is called repeatedly, on demand", `
			var n = 0, rs = new ReadableStream({ pull: function (c) { n++; if (n > 3) c.close(); else c.enqueue(n); } }, { highWaterMark: 0 });
			var r = rs.getReader(), out = [];
			(function pump() { r.read().then(function (x) { if (x.done) return done(out.join('') + '/' + n); out.push(x.value); pump(); }); })();`, "123/4"},
		{"async pull", `
			var n = 0, rs = new ReadableStream({ pull: function (c) { return new Promise(function (res) { setTimeout(function () { n++; if (n > 2) c.close(); else c.enqueue('c' + n); res(); }, 2); }); } });
			var r = rs.getReader(), out = [];
			(function pump() { r.read().then(function (x) { if (x.done) return done(out.join(',')); out.push(x.value); pump(); }); })();`, "c1,c2"},
		{"closed promise and locked", `
			var ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			var r = rs.getReader(), threw = false;
			try { rs.getReader(); } catch (e) { threw = true; }
			r.closed.then(function () { done(threw + ',' + rs.locked + ',closed'); });
			ctl.close();`, "true,true,closed"},
		{"enqueue after close throws", `
			var ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			ctl.close();
			try { ctl.enqueue(1); return 'no throw'; } catch (e) { return e instanceof TypeError; }`, "true"},
		{"cancel calls the source and ends reads", `
			var reason, rs = new ReadableStream({ start: function (c) { c.enqueue(1); }, cancel: function (r) { reason = r; } });
			var r = rs.getReader();
			r.cancel('why').then(function () { return r.read(); }).then(function (x) { done(reason + ',' + x.done); });`, "why,true"},
		{"tee delivers late chunks to both branches", `
			var ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			var t = rs.tee(), out = [];
			function drain(s, tag) { var r = s.getReader(); return (function pump() { return r.read().then(function (x) { if (x.done) return; out.push(tag + x.value); return pump(); }); })(); }
			Promise.all([drain(t[0], 'a'), drain(t[1], 'b')]).then(function () { done(out.sort().join(',')); });
			setTimeout(function () { ctl.enqueue(1); ctl.enqueue(2); ctl.close(); }, 3);`, "a1,a2,b1,b2"},
		{"pipeThrough an async transform with flush", `
			var ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			var ts = new TransformStream({
				transform: function (chunk, c) { return new Promise(function (res) { setTimeout(function () { c.enqueue(chunk.toUpperCase()); res(); }, 1); }); },
				flush: function (c) { c.enqueue('!'); }
			});
			var r = rs.pipeThrough(ts).getReader(), out = [];
			(function pump() { r.read().then(function (x) { if (x.done) return done(out.join('')); out.push(x.value); pump(); }); })();
			setTimeout(function () { ctl.enqueue('a'); ctl.enqueue('b'); ctl.close(); }, 2);`, "AB!"},
		{"identity TransformStream and writer ordering", `
			var ts = new TransformStream(), w = ts.writable.getWriter(), r = ts.readable.getReader(), out = [];
			w.write('x'); w.write('y'); w.close();
			(function pump() { r.read().then(function (x) { if (x.done) return done(out.join('')); out.push(x.value); pump(); }); })();`, "xy"},
		{"pipeTo resolves after the sink closes", `
			var got = [], closed = false;
			var rs = new ReadableStream({ start: function (c) { c.enqueue('a'); c.enqueue('b'); c.close(); } });
			rs.pipeTo(new WritableStream({ write: function (c) { got.push(c); }, close: function () { closed = true; } }))
			  .then(function () { done(got.join('') + ',' + closed); });`, "ab,true"},
		{"pipeTo propagates a source error to the sink", `
			var aborted, ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			rs.pipeTo(new WritableStream({ abort: function (e) { aborted = e.message; } }))
			  .then(null, function (e) { done(e.message + ',' + aborted); });
			ctl.error(new Error('src'));`, "src,src"},
		{"TextDecoderStream over late bytes", `
			var ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			var r = rs.pipeThrough(new TextDecoderStream()).getReader(), out = '';
			(function pump() { r.read().then(function (x) { if (x.done) return done(out); out += x.value; pump(); }); })();
			setTimeout(function () { ctl.enqueue(new TextEncoder().encode('héllo ')); ctl.enqueue(new TextEncoder().encode('wörld')); ctl.close(); }, 2);`, "héllo wörld"},
		{"async iteration", `
			var rs = new ReadableStream({ start: function (c) { c.enqueue('p'); c.enqueue('q'); c.close(); } });
			var it = rs[Symbol.asyncIterator](), out = [];
			(function step() { it.next().then(function (x) { if (x.done) return done(out.join('')); out.push(x.value); step(); }); })();`, "pq"},
		{"ReadableStream.from", `
			var r = ReadableStream.from(['a', 'b']).getReader(), out = [];
			(function pump() { r.read().then(function (x) { if (x.done) return done(out.join('')); out.push(x.value); pump(); }); })();`, "ab"},
		{"releaseLock rejects a pending read and frees the stream", `
			var rs = new ReadableStream({ start: function () {} });
			var r = rs.getReader(), p = r.read();
			r.releaseLock();
			p.then(null, function (e) { done((e instanceof TypeError) + ',' + rs.locked + ',' + (typeof rs.getReader().read)); });`, "true,false,function"},
		{"Response.body is a byte stream of the payload", `
			var r = new Response('hi ☃').body.getReader(), n = 0;
			r.read().then(function (x) { n = x.value.length; return r.read(); }).then(function (x) { done(n + ',' + x.done); });`, "6,true"},
		{"Response over a stream fed later", `
			var ctl, rs = new ReadableStream({ start: function (c) { ctl = c; } });
			new Response(rs).text().then(function (t) { done(t); });
			setTimeout(function () { ctl.enqueue(new TextEncoder().encode('late ')); ctl.enqueue('text'); ctl.close(); }, 3);`, "late text"},
		{"a consumed body rejects", `
			var r = new Response('x');
			r.text().then(function () { return r.text(); }).then(function () { done('no'); }, function (e) { done(e instanceof TypeError); });`, "true"},
		{"Blob.stream", `
			var r = new Blob(['ab', 'c']).stream().getReader();
			r.read().then(function (x) { done(new TextDecoder().decode(x.value)); });`, "abc"},
		{"null body status has no stream", `return new Response(null, { status: 204 }).body === null;`, "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := evalJS(t, ``, c.script); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// fetch().body yields the response bytes.
func TestFetchResponseBodyStream(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/data": "payload-123"}}
	got, _ := evalIn(t, "", ``, `
		fetch('/data').then(function (res) {
			var r = res.body.getReader(), out = '';
			(function pump() { r.read().then(function (x) { if (x.done) return done(out); out += new TextDecoder().decode(x.value); pump(); }); })();
		});`, js.Env{Transport: tr})
	if !strings.Contains(got, "payload-123") {
		t.Errorf("got %q", got)
	}
}
