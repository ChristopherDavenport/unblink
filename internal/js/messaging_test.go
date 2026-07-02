package js_test

import (
	"strings"
	"testing"
)

// TestWindowPostMessage proves same-window postMessage delivers asynchronously
// to both addEventListener('message') and onmessage, with structured-cloned
// data and the window origin.
func TestWindowPostMessage(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var got = [];
		  var payload = { n: 1, list: [1, 2] };
		  window.addEventListener('message', function (e) {
		    got.push('listener:' + e.data.n + ':' + (e.data !== payload) + ':' + (e.origin === location.origin) + ':' + (e.source === window));
		  });
		  window.onmessage = function (e) { got.push('onmessage:' + e.data.n); };
		  window.postMessage(payload, '*');
		  var syncFirst = got.length === 0; // delivery must be async
		  setTimeout(function () {
		    document.getElementById('out').textContent = 'sync=' + syncFirst + ' ' + got.join(' ');
		  }, 5);
		</script></body></html>`)
	for _, want := range []string{"sync=true", "listener:1:true:true:true", "onmessage:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("postMessage missing %q:\n%s", want, out)
		}
	}
}

// TestMessageChannel proves the port pair is functional: messages cross to the
// peer, are buffered until the receiving port starts (onmessage assignment
// starts it implicitly), and close() stops delivery.
func TestMessageChannel(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var mc = new MessageChannel();
		  var got = [];
		  mc.port1.postMessage('early'); // buffered: port2 not started yet
		  mc.port2.onmessage = function (e) { got.push(e.data); };
		  mc.port1.postMessage('later');
		  var mc2 = new MessageChannel();
		  var closedGot = [];
		  mc2.port2.onmessage = function (e) { closedGot.push(e.data); };
		  mc2.port1.close();
		  mc2.port1.postMessage('dropped');
		  setTimeout(function () {
		    document.getElementById('out').textContent =
		      'got=' + got.join(',') + ' closed=' + closedGot.length;
		  }, 5);
		</script></body></html>`)
	for _, want := range []string{"got=early,later", "closed=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("MessageChannel missing %q:\n%s", want, out)
		}
	}
}

// TestBroadcastChannelInert proves BroadcastChannel constructs and swallows
// posts silently — one context, no cross-tab peers, so silence is spec-shaped.
func TestBroadcastChannelInert(t *testing.T) {
	out := render(t, `<html><body><div id="out"></div>
		<script>
		  var bc = new BroadcastChannel('sync');
		  var fired = false;
		  bc.onmessage = function () { fired = true; };
		  bc.postMessage('anyone?');
		  bc.close();
		  setTimeout(function () {
		    document.getElementById('out').textContent = 'name=' + bc.name + ' fired=' + fired;
		  }, 5);
		</script></body></html>`)
	for _, want := range []string{"name=sync", "fired=false"} {
		if !strings.Contains(out, want) {
			t.Errorf("BroadcastChannel missing %q:\n%s", want, out)
		}
	}
}
