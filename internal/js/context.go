package js

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"golang.org/x/net/html"
)

// Settle tuning for the persistent (live Context) path. A Start()ed loop never
// drains its job queue (background +1, and any setInterval pins it), so "the page
// settled" is a heuristic: the in-flight network count has held at zero across a
// short quiet period, bounded by a wall-clock budget.
const (
	settleTick = 15 * time.Millisecond
	// 4 quiet ticks ≈ 60ms with no DOM change and no in-flight network before the
	// page is declared settled. 2 ticks (~30ms) proved too aggressive: a framework
	// idling between microtask batches, or an XHR scheduled but not yet dispatched,
	// could trip the quiet counter and return a half-hydrated page.
	settleQuietTicks = 4
	settleGrace      = 500 * time.Millisecond // hard-watchdog margin past the settle budget
)

var errContextClosed = errors.New("js: live context is closed")

// LiveContext is a persistent per-session JavaScript runtime bound to one page.
// Unlike Render (one-shot), the runtime, its event loop, the DOM bridge, and all
// listeners/timers survive across calls — a "true session". All methods marshal
// their work onto the loop goroutine, so callers may use it from any goroutine.
type LiveContext interface {
	// Dispatch fires one synthetic DOM event and settles, honoring any wait
	// condition carried on the action.
	Dispatch(ctx context.Context, action Action) (DispatchResult, error)
	// Snapshot serializes the current live DOM to HTML bytes (taken on the loop
	// goroutine, atomic with respect to JS/timer mutations).
	Snapshot(ctx context.Context) ([]byte, error)
	// PendingNavigation returns (and clears) any cross-document navigation the page's
	// JS requested via location.href/assign/replace since the last read; "" if none.
	PendingNavigation(ctx context.Context) (string, error)
	// Close tears the runtime down. Idempotent.
	Close()
}

// DispatchResult reports the outcome of one Dispatch: whether the selector resolved,
// whether the action's wait condition (if any) was met before the settle ended, and
// any uncaught script errors recorded during the dispatch + settle window (so a
// handler that threw is visible to the caller, not just a silent no-op).
type DispatchResult struct {
	Matched bool
	WaitMet bool
	Errors  []string
}

// Context is the concrete LiveContext: a long-lived goja runtime + event loop +
// bridge. Operations are serialized by mu; each runs as a single RunOnLoop job
// guarded by a wall-clock watchdog that uses vm.Interrupt (never loop.Terminate,
// which would kill the persistent loop).
type Context struct {
	loop    *eventloop.EventLoop
	timeout time.Duration

	ctx    context.Context // session-lived; backs page-JS network (not per-op)
	cancel context.CancelFunc

	mu     sync.Mutex
	vm     atomic.Pointer[goja.Runtime] // set on the load job; read by the watchdog
	bridge *bridge                      // set on the load job; touched only on-loop
	doc    *html.Node

	closed    atomic.Bool
	closeOnce sync.Once
}

// Open starts a persistent runtime, loads doc into it (scripts + lifecycle), and
// returns a live handle. doc becomes owned by the loop goroutine and is mutated in
// place for the Context's lifetime; read it only via Snapshot.
func (e *Engine) Open(ctx context.Context, doc *html.Node, base *url.URL, env Env) (LiveContext, error) {
	if doc == nil {
		return nil, fmt.Errorf("js: open: nil document")
	}
	loop := e.takeLoop()
	loop.Start()
	c := &Context{loop: loop, timeout: e.timeout, doc: doc}
	c.ctx, c.cancel = context.WithCancel(context.Background())

	scripts := collectScripts(doc)
	modules := collectModuleScripts(doc)
	err := c.run(ctx, true, nil, nil, func(vm *goja.Runtime) {
		c.vm.Store(vm)
		b := newBridge(vm, loop, doc, base, env.Transport, env.Cookies, env.Storage, env.SessionStorage, c.ctx, e.timeout)
		b.assets = e.assets
		b.install()
		_, _ = vm.RunProgram(preludeProgram)
		b.runScripts(scripts)
		if len(modules) > 0 {
			b.runModules(modules, parseImportMap(doc))
		}
		b.fireLifecycle()
		if env.Diag != nil {
			*env.Diag = b.collectDiagnostics()
		}
		c.bridge = b
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Dispatch fires one event on the live DOM and waits for it to settle, holding the
// settle open until the action's wait condition (if any) is met or the budget elapses.
func (c *Context) Dispatch(ctx context.Context, action Action) (DispatchResult, error) {
	acts := []Action{action}
	var cond *WaitCondition
	if action.WaitFor != "" || action.WaitText != "" {
		cond = &WaitCondition{Selector: action.WaitFor, Text: action.WaitText}
	}
	var st settleStats
	var before int
	err := c.run(ctx, true, cond, &st, func(*goja.Runtime) {
		before = len(c.bridge.diagErrors)
		c.bridge.runActions(acts)
	})
	res := DispatchResult{Matched: acts[0].Matched, WaitMet: st.met}
	// Read errors recorded during the dispatch + settle window on the loop (which
	// serializes with the settle above). Best-effort: a failure here loses only
	// diagnostics, never the dispatch outcome.
	_ = c.run(ctx, false, nil, nil, func(*goja.Runtime) {
		if n := len(c.bridge.diagErrors); n > before {
			res.Errors = append(res.Errors, c.bridge.diagErrors[before:n]...)
		}
	})
	return res, err
}

// Snapshot serializes the live document tree to HTML bytes on the loop goroutine.
func (c *Context) Snapshot(ctx context.Context) ([]byte, error) {
	var out []byte
	err := c.run(ctx, false, nil, nil, func(*goja.Runtime) {
		out = []byte(outerHTML(c.doc))
	})
	return out, err
}

// PendingNavigation reads and clears any cross-document navigation the page's JS
// requested (location.href/assign/replace). Read on the loop goroutine, no settle.
func (c *Context) PendingNavigation(ctx context.Context) (string, error) {
	var out string
	err := c.run(ctx, false, nil, nil, func(*goja.Runtime) {
		if c.bridge != nil && c.bridge.pendingNav != nil {
			out = c.bridge.pendingNav.String()
			c.bridge.pendingNav = nil
		}
	})
	return out, err
}

// Close cancels page-JS network and terminates the loop (clearing timers). Safe to
// call from any goroutine and more than once.
func (c *Context) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.cancel != nil {
			c.cancel()
		}
		c.loop.Terminate()
	})
}

// run executes fn on the loop goroutine, optionally waiting for the page to settle
// (with an optional wait condition + met out-param), under a wall-clock cap enforced
// via vm.Interrupt. Operations are serialized.
func (c *Context) run(ctx context.Context, waitSettle bool, cond *WaitCondition, stats *settleStats, fn func(vm *goja.Runtime)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return errContextClosed
	}

	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	scheduled := c.loop.RunOnLoop(func(vm *goja.Runtime) {
		// A Go panic here would crash the loop goroutine; recover and unblock the
		// caller. (JS exceptions/interrupts surface as errors, not panics.)
		defer func() {
			if r := recover(); r != nil {
				closeDone()
			}
		}()
		vm.ClearInterrupt() // clear any flag a previously-interrupted op left armed
		fn(vm)
		if waitSettle {
			settlePoll(c.loop, c.bridge, c.timeout, cond, stats, closeDone)
		} else {
			closeDone()
		}
	})
	if !scheduled {
		return errContextClosed
	}

	hard := time.NewTimer(c.timeout + settleGrace)
	defer hard.Stop()
	select {
	case <-done:
		return nil
	case <-hard.C:
		c.interrupt("unblink: live op timeout")
		<-done
		return nil
	case <-ctx.Done():
		c.interrupt("unblink: cancelled")
		<-done
		return ctx.Err()
	}
}

func (c *Context) interrupt(msg string) {
	if vm := c.vm.Load(); vm != nil {
		vm.Interrupt(msg)
	}
}

// settleThenClose runs the shared settle poll for the live Context: quiet-period
// only, no wait condition.
func (c *Context) settleThenClose(closeDone func()) {
	settlePoll(c.loop, c.bridge, c.timeout, nil, nil, closeDone)
}

// settlePoll schedules an on-loop quiet-period poll that closes done once the page
// has been quiet — no in-flight network *and* no DOM mutations (frameworks render
// after microtasks/rAF, not network) — for settleQuietTicks, or the budget elapses.
// It clears any armed interrupt before closing so background JS resumes.
//
// When cond is non-nil and non-empty it is a gate: the quiet counter is held at zero
// until cond holds in the live DOM, so the poll waits (within the same budget) for
// late-arriving content before settling. met, when non-nil, is set true the first
// tick the condition holds.
//
// pending/domVersion are read on the loop goroutine (same as where they are written),
// so no synchronization is needed. A page that stops mutating after load has a stable
// domVersion from the first tick, so with no condition it settles on the same schedule
// as before — the DOM-quiet gate only adds latency while a framework is rendering. All
// SetTimeout callbacks run on-loop, so callers must schedule the first tick from there.
func settlePoll(loop *eventloop.EventLoop, b *bridge, budget time.Duration, cond *WaitCondition, stats *settleStats, closeDone func()) {
	deadline := time.Now().Add(budget)
	quiet := 0
	var lastVersion uint64
	if b != nil {
		lastVersion = b.domVersion
	}
	condMet := cond.empty() // nil/blank condition is satisfied from the first tick
	var tick func(*goja.Runtime)
	tick = func(vm *goja.Runtime) {
		if !condMet && b != nil && cond.satisfied(b.doc) {
			condMet = true
			if stats != nil {
				stats.met = true
			}
		}
		netIdle := b == nil || b.pending.Load() == 0
		domQuiet := true
		if b != nil {
			v := b.domVersion
			domQuiet = v == lastVersion
			lastVersion = v
		}
		if condMet && netIdle && domQuiet {
			quiet++
		} else {
			quiet = 0
		}
		settled := condMet && quiet >= settleQuietTicks
		if settled || time.Now().After(deadline) {
			if stats != nil {
				stats.deadline = !settled
				stats.domBusy = !domQuiet
				if b != nil {
					stats.pending = b.pending.Load()
				}
			}
			vm.ClearInterrupt()
			closeDone()
			return
		}
		if loop.SetTimeout(tick, settleTick) == nil {
			closeDone() // loop terminated mid-settle
		}
	}
	if loop.SetTimeout(tick, settleTick) == nil {
		closeDone()
	}
}
