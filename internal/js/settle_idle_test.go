package js_test

import (
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// The provable-idle fast path (ADR 0004) closes the settle as soon as no
// in-flight network and no live wrapped timers remain — instead of waiting out
// the 60ms quiet window. These tests pin down both halves of that contract:
// truly idle pages settle fast, and every macrotask source that could still
// mutate the DOM (timers, immediates, message ports, network) holds the settle
// open until its content has landed.

// A page whose scripts finish synchronously is provably idle at the first
// check: nothing can run more JS, so the settle must not pay the quiet window.
func TestRenderIdlePageSettlesFast(t *testing.T) {
	out, diag, _ := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script>document.getElementById('out').textContent = 'hydrated';</script>
		</body></html>`, js.Env{}, 2*time.Second)
	if !strings.Contains(out, "hydrated") {
		t.Fatalf("content missing:\n%s", out)
	}
	if !diag.SettledIdle {
		t.Errorf("synchronous-only page should settle via the provable-idle fast path, diag=%+v", diag)
	}
	if diag.SettleDur >= 50*time.Millisecond {
		t.Errorf("idle settle should close well under the 60ms quiet window, took %v", diag.SettleDur)
	}
}

// An armed one-shot timer is a live work source: the settle must wait for it,
// and its content must be in the snapshot.
func TestRenderArmedTimerContentLands(t *testing.T) {
	out, _, elapsed := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script>setTimeout(function () {
			document.getElementById('out').textContent = 'deferred';
		}, 30);</script></body></html>`, js.Env{}, 2*time.Second)
	if !strings.Contains(out, "deferred") {
		t.Fatalf("timer content missing — settle closed before an armed timer fired:\n%s", out)
	}
	if elapsed < 30*time.Millisecond {
		t.Errorf("settle closed in %v, before the 30ms timer could have fired", elapsed)
	}
}

// An in-flight fetch is a live work source via the pending bracket: content
// inserted by its .then must be in the snapshot.
func TestRenderDelayedFetchContentLands(t *testing.T) {
	tr := &delayedTransport{delay: 50 * time.Millisecond, body: "payload"}
	out, _, _ := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script>fetch('/data').then(function (r) { return r.text(); }).then(function (txt) {
			document.getElementById('out').textContent = 'fetched:' + txt;
		});</script></body></html>`, js.Env{Transport: tr}, 2*time.Second)
	if !strings.Contains(out, "fetched:payload") {
		t.Fatalf("fetch .then content missing — settle closed while a request was in flight:\n%s", out)
	}
}

// setImmediate regression: goja's event loop exposes a native setImmediate that
// the prelude re-routes through the wrapped setTimeout so immediates land in
// the audited timer table. Unwrapped, a chain of immediates would be invisible
// to the provable-idle check and the settle could close between links. React's
// scheduler prefers setImmediate when it exists, so this is load-bearing.
func TestRenderSetImmediateChainContentLands(t *testing.T) {
	out, _, _ := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script>
		var steps = [];
		setImmediate(function () {
			steps.push('a');
			setImmediate(function () {
				steps.push('b');
				setImmediate(function () {
					steps.push('c');
					document.getElementById('out').textContent = 'chain:' + steps.join('');
				});
			});
		});
		</script></body></html>`, js.Env{}, 2*time.Second)
	if !strings.Contains(out, "chain:abc") {
		t.Fatalf("setImmediate chain content missing — immediates escaped the settle audit:\n%s", out)
	}
}

// MessageChannel delivery is timer-backed in the prelude, so a pending port
// message must hold the settle open until the handler has run.
func TestRenderMessageChannelContentLands(t *testing.T) {
	out, _, _ := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script>
		var ch = new MessageChannel();
		ch.port1.onmessage = function (ev) {
			document.getElementById('out').textContent = 'ported:' + ev.data;
		};
		ch.port2.postMessage('42');
		</script></body></html>`, js.Env{}, 2*time.Second)
	if !strings.Contains(out, "ported:42") {
		t.Fatalf("MessageChannel content missing — port delivery escaped the settle audit:\n%s", out)
	}
}

// A live-session dispatch whose handler runs synchronously (no timers, no
// network) is provably idle the moment the handler returns; interact must not
// pay the 60ms quiet window per click.
func TestDispatchIdleSettlesFast(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body>
		<button id="b">go</button><div id="out"></div>
		<script>document.getElementById('b').addEventListener('click', function () {
			document.getElementById('out').textContent = 'clicked';
		});</script></body></html>`, 2*time.Second)
	defer cleanup()

	start := time.Now()
	if ok := dispatch(t, lc, "#b", "click"); !ok {
		t.Fatal("dispatch did not match #b")
	}
	elapsed := time.Since(start)
	if out := snapshot(t, lc); !strings.Contains(out, "clicked") {
		t.Fatalf("handler content missing:\n%s", out)
	}
	if elapsed >= 50*time.Millisecond {
		t.Errorf("synchronous-only dispatch should settle well under the 60ms quiet window, took %v", elapsed)
	}
}
