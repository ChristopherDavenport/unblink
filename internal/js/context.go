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

// Settle tuning. A Start()ed loop never drains its job queue (background +1, and
// any setInterval pins it), so "the page settled" is inferred, on two tiers:
//
//   - Provable idleness: every JS work source is instrumented — network through
//     the `pending` bracket, macrotasks through the prelude's wrapped-timer table
//     (ADR 0004) — so zero in-flight requests plus zero live timers means no
//     mechanism exists to run more JS and the page cannot change again. The poll
//     closes after one short confirmation tick instead of waiting out the quiet
//     window.
//   - Quiet-window heuristic (anything armed: intervals, pending timers, network):
//     no DOM change, no in-flight network, and no clamped one-shot timers for
//     settleQuietWindow of wall clock. 30ms proved too aggressive historically —
//     a framework idling between microtask batches, or an XHR scheduled but not
//     yet dispatched, could trip it and return a half-hydrated page — so the
//     window stays at 60ms, now detected at 5ms granularity instead of 15ms.
const (
	settleTick        = 5 * time.Millisecond
	settleQuietWindow = 60 * time.Millisecond
	// settleConfirm delays the provable-idle close by one short tick as
	// defense-in-depth: a work source that escaped the audit would have to
	// surface within it. Documented as droppable with field evidence.
	settleConfirm = 1 * time.Millisecond
	settleGrace   = 500 * time.Millisecond // hard-watchdog margin past the settle budget
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
	// Snapshot serializes the current live DOM to HTML bytes plus the DOM
	// mutation version the bytes correspond to (both captured in one on-loop
	// job, atomic with respect to JS/timer mutations).
	Snapshot(ctx context.Context) ([]byte, uint64, error)
	// DOMVersion returns the live DOM's mutation counter — a cheap staleness
	// probe: while it is unchanged, a prior Snapshot's bytes are still current.
	DOMVersion(ctx context.Context) (uint64, error)
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
	loop     *eventloop.EventLoop
	timeout  time.Duration
	memGuard *memGuard

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
	c := &Context{loop: loop, timeout: e.timeout, memGuard: e.memGuard, doc: doc}
	c.ctx, c.cancel = context.WithCancel(context.Background())

	scripts := collectScripts(doc)
	modules := collectModuleScripts(doc)
	err := c.run(ctx, true, nil, nil, func(vm *goja.Runtime) {
		c.vm.Store(vm)
		c.memGuard.register(vm)
		b := newBridge(vm, loop, doc, base, env.Transport, env.Cookies, env.Storage, env.SessionStorage, c.ctx, e.timeout)
		b.assets = e.assets
		b.webdriver = e.webdriver
		b.install()
		b.startPrefetch(scripts) // overlap script-body fetches with the prelude
		_, _ = vm.RunProgram(preludeProgram)
		_, _ = vm.RunProgram(preludeAPIProgram)
		b.runScripts(scripts)
		if len(modules) > 0 {
			b.runModules(modules, parseImportMap(doc))
		}
		b.fireLifecycle()
		if env.Diag != nil {
			*env.Diag = b.collectDiagnostics()
			b.fillCaptureLogs(env.Diag) // best-effort: the request/console log so far
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

// Snapshot serializes the live document tree to HTML bytes on the loop
// goroutine, with the DOM version the bytes correspond to.
func (c *Context) Snapshot(ctx context.Context) ([]byte, uint64, error) {
	var out []byte
	var ver uint64
	err := c.run(ctx, false, nil, nil, func(*goja.Runtime) {
		root := c.doc
		if c.bridge != nil {
			// Flatten shadow content (resolving <slot>) into a composed clone so the
			// snapshot shows what a browser would render. Clone-based: the live tree
			// keeps its separate shadow subtrees intact for the next Dispatch.
			root = c.bridge.flattenCloneDoc()
			ver = c.bridge.domVersion
		}
		out = []byte(outerHTML(root))
	})
	return out, ver, err
}

// DOMVersion reads the live DOM's mutation counter on the loop goroutine.
func (c *Context) DOMVersion(ctx context.Context) (uint64, error) {
	var ver uint64
	err := c.run(ctx, false, nil, nil, func(*goja.Runtime) {
		if c.bridge != nil {
			ver = c.bridge.domVersion
		}
	})
	return ver, err
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
		c.memGuard.unregister(c.vm.Load())
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
			settlePoll(vm, c.loop, c.bridge, c.timeout, cond, stats, closeDone)
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

// auditTimers reads the prelude's timer audit: clamped one-shot timers still
// unfired (deferred content pending — holds the settle open) and all live wrapped
// timers of any kind (the provable-idle signal). Runs on the loop goroutine; the
// bridge's own runtime resolves the returned object.
func auditTimers(b *bridge) (clamped, live int) {
	if b == nil || b.timerAudit == nil {
		return 0, 0
	}
	v, err := b.timerAudit(goja.Undefined())
	if err != nil || v == nil {
		return 0, 0
	}
	obj := v.ToObject(b.vm)
	if obj == nil {
		return 0, 0
	}
	if c := obj.Get("c"); c != nil {
		clamped = int(c.ToInteger())
	}
	if t := obj.Get("t"); t != nil {
		live = int(t.ToInteger())
	}
	return clamped, live
}

// settlePoll closes done once the page has settled, on two tiers: immediately
// (after one confirmation tick) when the page is provably idle — no in-flight
// network and no live wrapped timers, so no mechanism exists to run more JS — or
// once the page has been quiet (no in-flight network, no DOM mutations, no clamped
// one-shot timers) for settleQuietWindow of wall clock, bounded by the budget. It
// clears any armed interrupt before closing so background JS resumes.
//
// When cond is non-nil and non-empty it is a gate: neither tier can close the poll
// until cond holds in the live DOM, so it waits (within the same budget) for
// late-arriving content before settling. met, when non-nil, is set true the first
// check the condition holds.
//
// pending/domVersion are read on the loop goroutine (same as where they are
// written), so no synchronization is needed. settlePoll must be called on the loop
// goroutine: the first check runs synchronously — script execution and lifecycle
// dispatch have completed and their microtasks drained, so an idle page is
// detectable at entry rather than one tick later. vm is the caller's runtime,
// used for that entry check; subsequent checks run as loop timer callbacks.
func settlePoll(vm *goja.Runtime, loop *eventloop.EventLoop, b *bridge, budget time.Duration, cond *WaitCondition, stats *settleStats, closeDone func()) {
	deadline := time.Now().Add(budget)
	var lastVersion uint64
	if b != nil {
		lastVersion = b.domVersion
	}
	condMet := cond.empty()     // nil/blank condition is satisfied from the first check
	var quietSince time.Time    // zero while the page is (or was just) busy
	var lastDOMChange time.Time // last check that observed a DOM mutation
	idleArmed := false          // previous check was provably idle; this one confirms
	var tick func(*goja.Runtime)
	tick = func(vm *goja.Runtime) {
		if !condMet && b != nil && cond.satisfied(b, b.doc) {
			condMet = true
			if stats != nil {
				stats.met = true
			}
		}
		now := time.Now()
		netIdle := b == nil || b.pending.Load() == 0
		domQuiet := true
		if b != nil {
			v := b.domVersion
			domQuiet = v == lastVersion
			lastVersion = v
			if !domQuiet {
				lastDOMChange = now
			}
		}
		clamped, liveTimers := auditTimers(b)
		// Provable idleness needs a real bridge (only then is every work source
		// instrumented) and an audit-registered prelude; liveTimers==0 without a
		// bridge proves nothing, so b==nil stays on the heuristic tier.
		idle := condMet && b != nil && b.timerAudit != nil && netIdle && liveTimers == 0
		settledIdle := idle && idleArmed && domQuiet
		idleArmed = idle
		// A clamped one-shot timer that hasn't fired is pending deferred content;
		// hold the quiet window open (within the budget) so it lands before the
		// snapshot. Unclamped live timers do NOT hold it — an interval must not
		// pin the settle to the budget (matches the pre-idle-tier behavior).
		if condMet && netIdle && domQuiet && clamped == 0 {
			if quietSince.IsZero() {
				quietSince = now
			}
		} else {
			quietSince = time.Time{}
		}
		settled := settledIdle ||
			(condMet && !quietSince.IsZero() && now.Sub(quietSince) >= settleQuietWindow)
		if settled || now.After(deadline) {
			if stats != nil {
				stats.deadline = !settled
				// "Still mutating at close" is judged over the quiet window, not
				// just the final tick: at 5ms granularity a page mutating every
				// few ms can slip a single quiet tick right at the deadline, and
				// a starved render must not read as DOM-quiet. Settled closes
				// were quiet for the full window (or provably idle) by
				// construction, so this widening only applies to deadline exits.
				stats.domBusy = !domQuiet ||
					(!settled && !lastDOMChange.IsZero() && now.Sub(lastDOMChange) < settleQuietWindow)
				stats.timersPending = clamped
				stats.timersLive = liveTimers
				stats.idleExit = settledIdle
				if b != nil {
					stats.pending = b.pending.Load()
				}
			}
			vm.ClearInterrupt()
			closeDone()
			return
		}
		delay := settleTick
		if idle {
			delay = settleConfirm
		}
		if loop.SetTimeout(tick, delay) == nil {
			closeDone() // loop terminated mid-settle
		}
	}
	tick(vm)
}
