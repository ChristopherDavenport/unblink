package js_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// slowTransport answers after a delay, so a request is observably in flight.
type slowTransport struct {
	inner js.Transport
	delay time.Duration
}

func (s slowTransport) Do(ctx context.Context, method, url string, h map[string]string, body []byte) (*js.Response, error) {
	time.Sleep(s.delay)
	return s.inner.Do(ctx, method, url, h, body)
}

// counterIn reads the integer text of <div id="out">.
func counterIn(t *testing.T, out string) int {
	t.Helper()
	i := strings.Index(out, `<div id="out">`)
	if i < 0 {
		t.Fatalf("no #out in:\n%s", out)
	}
	rest := out[i+len(`<div id="out">`):]
	n, err := strconv.Atoi(rest[:strings.Index(rest, "<")])
	if err != nil {
		t.Fatalf("no counter in:\n%s", out)
	}
	return n
}

// A long timer is the only thing pending: the settle runs it at once instead
// of waiting for its clamped delay, which sits near the end of the budget.
func TestTimerFastForwardWhenOnlyATimerIsPending(t *testing.T) {
	start := time.Now()
	out, diag := renderDiagBudget(t, `<html><body><div id="out">before</div>
		<script>
		  setTimeout(function () { document.getElementById('out').textContent = 'deferred'; }, 7000);
		</script></body></html>`, 5*time.Second)
	if !strings.Contains(out, ">deferred<") {
		t.Fatalf("deferred content missing:\n%s", out)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("render took %v; the timer should have been fast-forwarded, not waited for", el)
	}
	if diag.TimersFastForwarded != 1 {
		t.Errorf("TimersFastForwarded = %d, want 1", diag.TimersFastForwarded)
	}
}

// A chain of deferred timers is followed, each stage fast-forwarded in turn.
func TestTimerFastForwardFollowsAChain(t *testing.T) {
	start := time.Now()
	out, _ := renderDiagBudget(t, `<html><body><div id="out">before</div>
		<script>
		  setTimeout(function () {
		    setTimeout(function () {
		      setTimeout(function () { document.getElementById('out').textContent = 'third'; }, 8000);
		    }, 9000);
		  }, 7000);
		</script></body></html>`, 5*time.Second)
	if !strings.Contains(out, ">third<") {
		t.Fatalf("third-stage content missing:\n%s", out)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("render took %v, want the chain fast-forwarded", el)
	}
}

// Timers pulled in together run in the order they were due, and microtasks
// drain between them as between any two timer tasks.
func TestTimerFastForwardKeepsDueOrder(t *testing.T) {
	out, _ := renderDiagBudget(t, `<html><body><div id="out"></div>
		<script>
		  var log = [];
		  function note(s) { log.push(s); document.getElementById('out').textContent = log.join(','); }
		  setTimeout(function () { note('c'); }, 9000);
		  setTimeout(function () { note('a'); Promise.resolve().then(function () { note('a-micro'); }); }, 7000);
		  setTimeout(function () { note('b'); }, 8000);
		</script></body></html>`, 5*time.Second)
	if !strings.Contains(out, ">a,a-micro,b,c<") {
		t.Errorf("fast-forwarded timers ran out of order:\n%s", out)
	}
}

// A request in flight is work in progress: the timer is not run ahead of it.
func TestTimerFastForwardWaitsForTheNetwork(t *testing.T) {
	tr := slowTransport{inner: &stubTransport{routes: map[string]string{"/slow": "payload"}}, delay: 300 * time.Millisecond}
	out, _, _ := renderWaitDiag(t, `<html><body><div id="out"></div>
		<script>
		  var log = [];
		  function note(s) { log.push(s); document.getElementById('out').textContent = log.join(','); }
		  fetch('/slow').then(function () { note('fetch'); });
		  setTimeout(function () { note('timer'); }, 7000);
		</script></body></html>`, js.Env{Transport: tr}, 5*time.Second)
	if !strings.Contains(out, ">fetch,timer<") {
		t.Errorf("the long timer ran before the in-flight request settled:\n%s", out)
	}
}

// A poll that re-arms itself is pulled in once; its successor keeps its real
// delay and does not hold the settle.
func TestTimerFastForwardDoesNotLoopAPoll(t *testing.T) {
	start := time.Now()
	out, _ := renderDiagBudget(t, `<html><body><div id="out">0</div>
		<script>
		  var n = 0;
		  function poll() {
		    document.getElementById('out').textContent = String(++n);
		    setTimeout(poll, 20000);
		  }
		  setTimeout(poll, 20000);
		</script></body></html>`, 5*time.Second)
	if n := counterIn(t, out); n != 1 {
		t.Errorf("self-rearming poll ran %d times, want exactly the one pull-in", n)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("render took %v; a re-armed poll must not hold the settle", el)
	}
}

// The same loop written so no callback is ever reused, re-armed from a
// promise: nothing ties one timer to the next, so the round limit ends it.
func TestTimerFastForwardBoundsAFreshClosureLoop(t *testing.T) {
	start := time.Now()
	out, _ := renderDiagBudget(t, `<html><body><div id="out">0</div>
		<script>
		  var n = 0;
		  function arm() {
		    setTimeout(function () {
		      document.getElementById('out').textContent = String(++n);
		      Promise.resolve().then(arm);
		    }, 20000);
		  }
		  arm();
		</script></body></html>`, 5*time.Second)
	if n := counterIn(t, out); n < 1 || n > 4 {
		t.Errorf("fresh-closure loop ran %d times, want 1..4 (the round limit)", n)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("render took %v; the loop should have ended at the round limit", el)
	}
}

// A timer far beyond any deferred-content delay (a refresh interval, a session
// timeout) is not pulled in at all.
func TestTimerClampIgnoresVeryLongDelays(t *testing.T) {
	start := time.Now()
	out, diag := renderDiagBudget(t, `<html><body><div id="out">kept</div>
		<script>
		  setTimeout(function () { document.getElementById('out').textContent = 'session-expired'; }, 15 * 60 * 1000);
		</script></body></html>`, 5*time.Second)
	if !strings.Contains(out, ">kept<") {
		t.Errorf("a 15-minute timer was pulled into the render:\n%s", out)
	}
	if diag.TimersPending != 0 || diag.TimersFastForwarded != 0 {
		t.Errorf("TimersPending=%d TimersFastForwarded=%d, want 0/0", diag.TimersPending, diag.TimersFastForwarded)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("render took %v; an unpulled timer must not hold the settle", el)
	}
}
