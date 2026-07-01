package js_test

import (
	"strings"
	"testing"
)

func TestListenerOnce(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  var count = 0;
		  document.getElementById('b').addEventListener('click', function () { count++; }, { once: true });
		  document.getElementById('b').click();
		  document.getElementById('b').click();
		  document.body.setAttribute('data-count', String(count));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-count="1"`) {
		t.Errorf("{once} listener should fire exactly once:\n%s", out)
	}
}

func TestListenerPassive(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  document.getElementById('b').addEventListener('click', function (e) { e.preventDefault(); }, { passive: true });
		  var ev = new Event('click', { cancelable: true });
		  document.body.setAttribute('data-result', String(document.getElementById('b').dispatchEvent(ev)));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-result="true"`) {
		t.Errorf("{passive} listener's preventDefault should be a no-op:\n%s", out)
	}
}

func TestListenerSignalRemoval(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  var count = 0;
		  var c = new AbortController();
		  document.getElementById('b').addEventListener('click', function () { count++; }, { signal: c.signal });
		  document.getElementById('b').click();
		  c.abort();
		  document.getElementById('b').click();
		  document.body.setAttribute('data-count', String(count));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-count="1"`) {
		t.Errorf("aborting the signal should remove the listener:\n%s", out)
	}
}

func TestAbortController(t *testing.T) {
	out := renderWith(t, `<html><body>
		<script>
		  var c = new AbortController();
		  var fired = false;
		  c.signal.addEventListener('abort', function () { fired = true; });
		  document.body.setAttribute('data-before', String(c.signal.aborted));
		  c.abort();
		  document.body.setAttribute('data-after', String(c.signal.aborted));
		  document.body.setAttribute('data-fired', String(fired));
		</script></body></html>`, nil)
	for _, want := range []string{`data-before="false"`, `data-after="true"`, `data-fired="true"`} {
		if !strings.Contains(out, want) {
			t.Errorf("AbortController missing %q:\n%s", want, out)
		}
	}
}

func TestPreAbortedListener(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  var count = 0;
		  document.getElementById('b').addEventListener('click', function () { count++; }, { signal: AbortSignal.abort() });
		  document.getElementById('b').click();
		  document.body.setAttribute('data-count', String(count));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-count="0"`) {
		t.Errorf("a pre-aborted signal should prevent registration:\n%s", out)
	}
}

func TestTypedMouseEvent(t *testing.T) {
	out := renderWith(t, `<html><body><button id="b">x</button>
		<script>
		  document.body.addEventListener('click', function (e) {
		    document.body.setAttribute('data-info', e.type + '/' + e.clientX + '/' + e.target.tagName);
		  });
		  document.getElementById('b').dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 5 }));
		</script></body></html>`, nil)
	if !strings.Contains(out, `data-info="click/5/BUTTON"`) {
		t.Errorf("typed MouseEvent fields/bubbling not preserved:\n%s", out)
	}
}

func TestFetchSignalPreAborted(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/api": "DATA"}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  var c = new AbortController(); c.abort();
		  fetch('/api', { signal: c.signal }).then(
		    function () { document.getElementById('out').textContent = 'RESOLVED'; },
		    function () { document.getElementById('out').textContent = 'ABORTED'; });
		</script></body></html>`, tr)
	if !strings.Contains(out, ">ABORTED<") {
		t.Errorf("fetch with a pre-aborted signal should reject:\n%s", out)
	}
}

func TestFetchSignalMidFlight(t *testing.T) {
	tr := &stubTransport{routes: map[string]string{"/api": "DATA"}}
	out := renderWith(t, `<html><body><div id="out">x</div>
		<script>
		  var c = new AbortController();
		  fetch('/api', { signal: c.signal }).then(
		    function () { document.getElementById('out').textContent = 'RESOLVED'; },
		    function () { document.getElementById('out').textContent = 'ABORTED'; });
		  c.abort();
		</script></body></html>`, tr)
	if !strings.Contains(out, ">ABORTED<") {
		t.Errorf("aborting in flight should reject the fetch:\n%s", out)
	}
}
