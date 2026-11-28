//go:build unix

package js_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/js"
	"golang.org/x/net/html"
)

func openLiveIdle(t *testing.T, eng *js.Engine, src string) js.LiveContext {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/page")
	lc, err := eng.Open(context.Background(), doc, base, js.Env{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(lc.Close)
	return lc
}

// processCPU is the user+system CPU time this process has used.
func processCPU(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// Between operations nobody waits on a live context, so a page callback that
// never returns used to spin a core until the next operation or the close. The
// idle watchdog interrupts it, and stops the context when it keeps happening.
func TestLiveIdleRunawayIsStopped(t *testing.T) {
	eng := js.New(js.WithTimeout(time.Second), js.WithLiveTaskBudget(150*time.Millisecond))
	lc := openLiveIdle(t, eng, `<html><body><button id="b">go</button><script>
		document.getElementById('b').addEventListener('click', function () {
			setTimeout(function () { setInterval(function () { for (;;) {} }, 10); }, 1300);
		});
	</script></body></html>`)
	// The spin starts after the dispatch has settled, i.e. while the context is idle.
	if _, err := lc.Dispatch(context.Background(), js.Action{Selector: "#b", Type: "click"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if _, err = lc.DOMVersion(context.Background()); errors.Is(err, js.ErrRunaway) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !errors.Is(err, js.ErrRunaway) {
		t.Fatalf("context was never stopped; last error %v", err)
	}
	if _, err := lc.Dispatch(context.Background(), js.Action{Selector: "#b", Type: "click"}); !errors.Is(err, js.ErrRunaway) {
		t.Errorf("Dispatch after the stop = %v, want ErrRunaway", err)
	}
	if _, _, err := lc.Snapshot(context.Background()); !errors.Is(err, js.ErrRunaway) {
		t.Errorf("Snapshot after the stop = %v, want ErrRunaway", err)
	}
	// The core is released: a spinning loop would burn the whole window.
	const window = 600 * time.Millisecond
	before := processCPU(t)
	time.Sleep(window)
	if used := processCPU(t) - before; used > window/3 {
		t.Errorf("process used %v CPU in %v after the stop: page code is still spinning", used, window)
	}
}

// One long task is interrupted but does not cost the page its runtime, and a
// later prompt probe forgets it.
func TestLiveIdleSingleLongTaskKeepsTheContext(t *testing.T) {
	eng := js.New(js.WithTimeout(time.Second), js.WithLiveTaskBudget(150*time.Millisecond))
	lc := openLiveIdle(t, eng, `<html><body><button id="b">go</button><p id="out">start</p><script>
		var b = document.getElementById('b'), out = document.getElementById('out');
		b.addEventListener('click', function () {
			setTimeout(function () {
				out.textContent = 'spinning';
				var end = Date.now() + 30000;
				while (Date.now() < end) {}
				out.textContent = 'ran to the end';
			}, 1300);
		});
		b.addEventListener('mouseover', function () { out.textContent = 'alive'; });
	</script></body></html>`)
	if _, err := lc.Dispatch(context.Background(), js.Action{Selector: "#b", Type: "click"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Wait for the long task to start and be cut short.
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, _, err := lc.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if strings.Contains(string(snap), ">spinning<") {
			break
		}
		if strings.Contains(string(snap), ">ran to the end<") {
			t.Fatal("a 30s task ran to completion on an idle context")
		}
		if time.Now().After(deadline) {
			t.Fatalf("the task never started: %s", snap)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second) // several quiet probes
	res, err := lc.Dispatch(context.Background(), js.Action{Selector: "#b", Type: "hover"})
	if err != nil || !res.Matched {
		t.Fatalf("the context is not usable after one interrupted task: matched=%v err=%v", res.Matched, err)
	}
	snap, _, _ := lc.Snapshot(context.Background())
	if !strings.Contains(string(snap), ">alive<") {
		t.Errorf("handler did not run after the interrupted task: %s", snap)
	}
}

// The idle watchdog stands down while an operation owns the loop: a handler
// that legitimately runs longer than the idle task budget, inside its
// operation's own budget, finishes.
func TestLiveLongDispatchIsNotAnIdleTask(t *testing.T) {
	eng := js.New(js.WithTimeout(3*time.Second), js.WithLiveTaskBudget(100*time.Millisecond))
	lc := openLiveIdle(t, eng, `<html><body><button id="b">go</button><p id="out">start</p><script>
		document.getElementById('b').addEventListener('click', function () {
			var end = Date.now() + 700;
			while (Date.now() < end) {}
			document.getElementById('out').textContent = 'handler finished';
		});
	</script></body></html>`)
	for i := 0; i < 3; i++ {
		res, err := lc.Dispatch(context.Background(), js.Action{Selector: "#b", Type: "click"})
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		if len(res.Errors) > 0 {
			t.Fatalf("dispatch %d: handler was interrupted: %v", i, res.Errors)
		}
	}
	snap, _, err := lc.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !strings.Contains(string(snap), ">handler finished<") {
		t.Errorf("a 700ms handler inside a 3s operation budget did not finish: %s", snap)
	}
}

// Busy-but-yielding background work never strikes: each task is under budget.
func TestLiveIdleYieldingWorkIsLeftAlone(t *testing.T) {
	eng := js.New(js.WithTimeout(time.Second), js.WithLiveTaskBudget(200*time.Millisecond))
	lc := openLiveIdle(t, eng, `<html><body><p id="out">0</p><script>
		var n = 0;
		setInterval(function () {
			var end = Date.now() + 40;
			while (Date.now() < end) {}
			document.getElementById('out').textContent = String(++n);
		}, 10);
	</script></body></html>`)
	time.Sleep(2500 * time.Millisecond)
	if _, err := lc.DOMVersion(context.Background()); err != nil {
		t.Fatalf("a page whose tasks all yield was stopped: %v", err)
	}
}
