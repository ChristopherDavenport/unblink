package js_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
)

// slicedWork is a page that does its work the way a time-slicing scheduler does:
// slices of busy CPU with a yield between them through `yield`, and no DOM
// mutation until the very end — the shape of React hydration, whose render
// phase touches nothing and commits once. N×sliceMs is far beyond the 60ms
// quiet window.
func slicedWork(yield string, slices, sliceMs int) string {
	return `<html><body><div id="out">server</div><script>
		var n = 0, slices = ` + itoa(slices) + `, sliceMs = ` + itoa(sliceMs) + `;
		function work() {
			var end = Date.now() + sliceMs; while (Date.now() < end) {}
			if (++n < slices) next(); else document.getElementById('out').textContent = 'hydrated:' + n;
		}
		` + yield + `
		next();
	</script></body></html>`
}

func itoa(n int) string { return strconv.Itoa(n) }

const (
	yieldImmediate = `function next() { setImmediate(work); }`
	yieldChannel   = `var ch = new MessageChannel(); ch.port1.onmessage = work; function next() { ch.port2.postMessage(null); }`
	yieldPost      = `window.addEventListener('message', work); function next() { window.postMessage('go', '*'); }`
	yieldTimeout   = `function next() { setTimeout(work, 0); }`
)

// A scheduler's continuation chain is the page still working. It holds the
// settle open even though nothing in the DOM or on the network moves.
func TestSettleWaitsForTimeSlicedWork(t *testing.T) {
	for name, yield := range map[string]string{
		"setImmediate": yieldImmediate, "MessageChannel": yieldChannel, "postMessage": yieldPost,
	} {
		t.Run(name, func(t *testing.T) {
			out, diag, elapsed := renderWaitDiag(t, slicedWork(yield, 20, 10), js.Env{}, 3*time.Second)
			if !strings.Contains(out, "hydrated:20") {
				t.Fatalf("render closed mid-work after %v (deadline hit %v):\n%s", elapsed, diag.DeadlineHit, out)
			}
			if diag.DeadlineHit {
				t.Errorf("finished work must settle on its own, not at the budget (%v)", elapsed)
			}
			if elapsed > 1500*time.Millisecond {
				t.Errorf("200ms of work plus the quiet window took %v", elapsed)
			}
			if diag.TasksHeld < 100*time.Millisecond {
				t.Errorf("TasksHeld = %v, want about the 200ms the chain ran", diag.TasksHeld)
			}
		})
	}
}

// The hold is bounded: a page that re-queues itself forever through the same
// primitives must not pin the render to its budget.
func TestSettleBoundsAnEndlessTaskChain(t *testing.T) {
	for name, yield := range map[string]string{
		"setImmediate":   `(function spin() { setImmediate(spin); })();`,
		"MessageChannel": `var ch = new MessageChannel(); ch.port1.onmessage = function () { ch.port2.postMessage(1); }; ch.port2.postMessage(1);`,
	} {
		t.Run(name, func(t *testing.T) {
			_, diag, elapsed := renderWaitDiag(t, `<html><body><p>content</p><script>`+yield+`</script></body></html>`,
				js.Env{}, 8*time.Second)
			if diag.DeadlineHit {
				t.Fatalf("an endless task chain ran the render to its 8s budget (%v)", elapsed)
			}
			if elapsed < js.SettleTaskHoldMax || elapsed > js.SettleTaskHoldMax+time.Second {
				t.Errorf("settled after %v, want just past the %v bound", elapsed, js.SettleTaskHoldMax)
			}
		})
	}
}

// Timers with delay semantics are not continuations: a zero-delay setTimeout
// loop and a requestAnimationFrame loop keep the quiet-window behaviour.
func TestSettleDoesNotHoldForTimerLoops(t *testing.T) {
	for name, loop := range map[string]string{
		"setTimeout0": `(function spin() { setTimeout(spin, 0); })();`,
		"rAF":         `(function frame() { requestAnimationFrame(frame); })();`,
	} {
		t.Run(name, func(t *testing.T) {
			_, diag, elapsed := renderWaitDiag(t, `<html><body><p>content</p><script>`+loop+`</script></body></html>`,
				js.Env{}, 5*time.Second)
			if elapsed > 500*time.Millisecond || diag.TasksHeld != 0 {
				t.Errorf("a %s loop held the settle: %v (TasksHeld %v)", name, elapsed, diag.TasksHeld)
			}
		})
	}
}

// Pages with no continuation work keep the provable-idle fast path.
func TestSettleIdleTierUnaffectedByTaskHold(t *testing.T) {
	_, diag, elapsed := renderWaitDiag(t, `<html><body><div id="o"></div><script>
		setImmediate(function () { document.getElementById('o').textContent = 'done'; });
	</script></body></html>`, js.Env{}, 3*time.Second)
	if !diag.SettledIdle {
		t.Errorf("a one-shot immediate should still end provably idle (took %v)", elapsed)
	}
	if diag.TasksHeld != 0 {
		t.Errorf("TasksHeld = %v for a single immediate", diag.TasksHeld)
	}
	// Generous on purpose: the tier is asserted by SettledIdle; this only catches
	// the render waiting out a hold (seconds), and must not flake on a busy host.
	if elapsed > 500*time.Millisecond {
		t.Errorf("idle-tier render took %v", elapsed)
	}
}
