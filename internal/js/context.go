package js

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/christopherdavenport/unblink/internal/webext"
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
	// settleTaskHoldMax bounds how long pending queued tasks (setImmediate,
	// MessageChannel, postMessage — a scheduler's continuation chain) may hold
	// the quiet window open in one settle. Time-sliced work that needs longer is
	// cut off as it was before the hold existed; a page that re-queues itself
	// forever costs this much instead of the whole budget.
	settleTaskHoldMax = 2 * time.Second
	// settleFastForwardMax is a backstop on how many clamped timers one settle
	// runs early. The real bound is the prelude's: it stops pulling timers in
	// after a few fast-forward rounds, so nothing is left to run.
	settleFastForwardMax = 512
)

// SettleTaskHoldMax is settleTaskHoldMax, exported for tests.
const SettleTaskHoldMax = settleTaskHoldMax

var errContextClosed = errors.New("js: live context is closed")

// ErrRunaway is returned by every operation on a live context the idle watchdog
// has stopped: page code kept running tasks that never yielded while no operation
// was in flight (see Context.watch). The runtime is gone; the caller should drop
// the handle and say so, not treat it as an ordinary close.
var ErrRunaway = errors.New("js: live context stopped: page script kept running without yielding")

// Idle-watchdog defaults (see Context.watch). A task is one event-loop job: a
// timer callback, a fetch continuation, a listener.
const (
	// DefaultLiveTaskBudget is how long one page task may run between operations
	// before it is interrupted.
	DefaultLiveTaskBudget = 2 * time.Second
	// liveMaxStrikes consecutive over-budget tasks stop the context.
	liveMaxStrikes = 3
	// liveProbeEvery is how often the watchdog checks that the loop still turns.
	liveProbeEvery = 250 * time.Millisecond
)

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
	closedCh  chan struct{} // closed by Close; stops the idle watchdog

	// Idle watchdog state (see watch). wdMu orders the watchdog's Interrupt
	// against an operation's start, so an interrupt aimed at a runaway page task
	// can never land on the operation that follows it.
	taskBudget time.Duration
	wdMu       sync.Mutex
	opRunning  bool   // an operation's job (fn + settle) is on the loop; guarded by wdMu
	opSeq      uint64 // bumped as each operation's job starts; guarded by wdMu
	runaway    atomic.Bool
}

// Open starts a persistent runtime, loads doc into it (scripts + lifecycle), and
// returns a live handle. doc becomes owned by the loop goroutine and is mutated in
// place for the Context's lifetime; read it only via Snapshot.
func (e *Engine) Open(ctx context.Context, doc *html.Node, base *url.URL, env Env) (LiveContext, error) {
	if doc == nil {
		return nil, fmt.Errorf("js: open: nil document")
	}
	warm := e.takeRuntime()
	loop := warm.loop
	c := &Context{loop: loop, timeout: e.timeout, memGuard: e.memGuard, doc: doc, closedCh: make(chan struct{}), taskBudget: e.liveTaskBudget}
	c.ctx, c.cancel = context.WithCancel(context.Background())

	scripts := collectScripts(doc)
	modules := collectModuleScripts(doc)
	err := c.run(ctx, true, nil, nil, func(vm *goja.Runtime) {
		c.vm.Store(vm)
		c.memGuard.register(vm)
		b := warm.b
		if b == nil {
			b = e.prepare(vm, loop)
		}
		pinClock(vm, env.ClockStart)
		b.bind(doc, base, env.Transport, env.Cookies, env.Storage, env.SessionStorage, c.ctx, e.timeout)
		b.allowCrossOrigin = env.AllowCrossOrigin
		b.sriEnabled = !env.DisableSRI
		b.respHeaders = env.ResponseHeaders
		b.cspEnabled = !env.DisableCSP
		if b.cspEnabled && b.base != nil {
			b.csp = buildCSP(b.respHeaders, b.doc)
			if b.csp != nil {
				b.hideNonces() // capture + blank nonces before page JS can read them
			}
		}
		b.finishBind()
		b.startPrefetch(scripts)  // overlap script-body fetches with the prelude
		b.startModulePreload(doc) // warm the modulepreload chunk graph concurrently
		// Tell the extension background this tab navigated to the page before any request.
		e.extHost.notifyNavigation(b.tabID, baseURLString(base))
		// Extension content scripts share the page world (ADR 0010): gather hiding CSS
		// once, then inject JS at each run_at around the page's own scripts.
		b.injectContentScriptCSS()
		b.readyState = "loading" // the page load begins (see setReadyState)
		b.injectContentScripts(webext.RunAtStart)
		b.installCSPEvalGate()                 // CSP unsafe-eval gate before page scripts
		b.runDocumentScripts(scripts, modules) // sets readyState "interactive" once parsing is done
		b.injectContentScripts(webext.RunAtEnd)
		b.fireLifecycle()
		b.injectContentScripts(webext.RunAtIdle)
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
	go c.watch()
	return c, nil
}

// watch is the idle watchdog: it bounds page code that runs while no operation
// is in flight. An operation has its own wall-clock guard (run), but between
// operations a live context's loop keeps serving the page's timers and network
// callbacks with nobody waiting on it — a callback that never returns
// (setInterval(function(){for(;;){}},10)) would spin a core until the next
// operation or the close, and a session can sit idle for a long time.
//
// The loop is single-goroutine, so "is a task stuck?" is asked the only way that
// cannot race it: a no-op job is queued, and if it has not run within the task
// budget the loop has been inside one job that long. Unless an operation owns
// the loop (its guard applies, and a long Dispatch within its budget is
// legitimate), that job is page code and is interrupted. An interrupt unwinds
// one task only — the page may schedule another — so consecutive over-budget
// tasks are counted, and at liveMaxStrikes the context is stopped: the runtime
// is closed and every later operation returns ErrRunaway. A task budget's worth
// of promptly answered probes resets the count, so occasional long tasks never
// add up to a stop.
func (c *Context) watch() {
	tick := time.NewTicker(liveProbeEvery)
	defer tick.Stop()
	strikes := 0
	var quietSince time.Time // start of the current run of prompt, operation-free probes
	for {
		select {
		case <-c.closedCh:
			return
		case <-tick.C:
		}
		opBefore, seqBefore := c.opState()
		ack := make(chan struct{})
		if !c.loop.RunOnLoop(func(vm *goja.Runtime) {
			// Whatever task the watchdog interrupted has unwound by the time this
			// runs; drop a flag that arrived just after it returned on its own so
			// it cannot unwind the next, innocent task.
			c.wdMu.Lock()
			if !c.opRunning {
				vm.ClearInterrupt()
			}
			c.wdMu.Unlock()
			close(ack)
		}) {
			return
		}
		prompt := true
		budget := time.NewTimer(c.taskBudget)
	wait:
		for {
			select {
			case <-ack:
				break wait
			case <-c.closedCh:
				budget.Stop()
				return
			case <-budget.C:
				prompt = false
				if c.interruptIdleTask() {
					strikes++
					if strikes >= liveMaxStrikes {
						c.runaway.Store(true)
						slog.Warn("js: live context stopped: page tasks kept exceeding the idle task budget", "budget", c.taskBudget, "strikes", strikes)
						c.Close()
						return
					}
				}
				budget.Reset(c.taskBudget)
			}
		}
		budget.Stop()
		// Strikes are forgiven only after the loop has answered promptly for a
		// whole task budget, with no operation on it in that time. One prompt
		// answer proves nothing: a callback that was just interrupted leaves the
		// loop free until the page's next timer fires, and a probe landing in
		// that gap is answered at once. An operation says nothing about the
		// page's own tasks either way — a slow answer may be the operation's
		// work, a prompt one its guard interrupting the page every 10ms.
		opAfter, seqAfter := c.opState()
		switch {
		case !prompt:
			quietSince = time.Time{}
		case opBefore || opAfter || seqBefore != seqAfter:
			// neutral
		case quietSince.IsZero():
			quietSince = time.Now()
		case time.Since(quietSince) >= c.taskBudget:
			strikes = 0
		}
	}
}

// opState reports whether an operation owns the loop, and how many have started.
func (c *Context) opState() (running bool, seq uint64) {
	c.wdMu.Lock()
	defer c.wdMu.Unlock()
	return c.opRunning, c.opSeq
}

// interruptIdleTask interrupts the task on the loop unless an operation owns it,
// and reports whether it did.
func (c *Context) interruptIdleTask() bool {
	c.wdMu.Lock()
	defer c.wdMu.Unlock()
	if c.opRunning {
		return false
	}
	if vm := c.vm.Load(); vm != nil {
		vm.Interrupt("unblink: live task budget")
	}
	return true
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
	// matched is written on the loop and read here: when run returns early (the
	// runtime was stopped, the context ended) the loop may still be inside
	// runActions, so the result is handed over atomically rather than read out of
	// the action it is still writing.
	var matched atomic.Bool
	err := c.run(ctx, true, cond, &st, func(*goja.Runtime) {
		// Each dispatch is one agent action; give it a fresh download budget so a
		// long-lived live session isn't starved by a cumulative cap (the one-shot
		// render path gets a new transport per call, so this only matters here).
		c.bridge.resetTransportBudget()
		before = len(c.bridge.diagErrors)
		c.bridge.runActions(acts)
		matched.Store(acts[0].Matched)
	})
	res := DispatchResult{Matched: matched.Load(), WaitMet: st.met}
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
			// Form-associated custom elements' submission values ride on the snapshot
			// as attributes, removed again below so page JS never observes them.
			defer c.bridge.reflectInternals()()
			// Flatten shadow content (resolving <slot>) into a composed clone so the
			// snapshot shows what a browser would render. Clone-based: the live tree
			// keeps its separate shadow subtrees intact for the next Dispatch.
			root = c.bridge.flattenCloneDoc()
			// Detach extension-hidden ad markup from the clone (the live tree keeps it,
			// so a later Dispatch still sees the real DOM).
			c.bridge.applyCosmeticFilters(root)
			ver = c.bridge.domVersion + c.bridge.styleVersion
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
			ver = c.bridge.domVersion + c.bridge.styleVersion
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
		close(c.closedCh)
		if b := c.bridge; b != nil {
			b.extHost.notifyTabRemoved(b.tabID) // drop the extension's page store for this session
		}
		if c.cancel != nil {
			c.cancel()
		}
		// Terminate only stops the loop between jobs: keep interrupting until it has
		// joined, so a page callback that never returns cannot block the close
		// (see interruptUntil).
		joined := make(chan struct{})
		go func() {
			c.loop.Terminate()
			close(joined)
		}()
		interruptUntil(c.vm.Load(), "unblink: context closed", joined)
		c.memGuard.unregister(c.vm.Load())
	})
}

// run executes fn on the loop goroutine, optionally waiting for the page to settle
// (with an optional wait condition + met out-param), under a wall-clock cap enforced
// via vm.Interrupt. Operations are serialized.
func (c *Context) run(ctx context.Context, waitSettle bool, cond *WaitCondition, stats *settleStats, fn func(vm *goja.Runtime)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runaway.Load() {
		return ErrRunaway
	}
	if c.closed.Load() {
		return errContextClosed
	}

	done := make(chan struct{})
	var once sync.Once
	closeDone := func() {
		once.Do(func() {
			c.wdMu.Lock()
			c.opRunning = false
			c.wdMu.Unlock()
			close(done)
		})
	}

	scheduled := c.loop.RunOnLoop(func(vm *goja.Runtime) {
		// A Go panic here would crash the loop goroutine; recover and unblock the
		// caller. (JS exceptions/interrupts surface as errors, not panics.)
		defer func() {
			if r := recover(); r != nil {
				closeDone()
			}
		}()
		// From here the operation owns the loop: the idle watchdog stands down,
		// and any flag a previously-interrupted op or the watchdog left armed is
		// cleared under the same lock so neither can unwind this operation.
		c.wdMu.Lock()
		c.opRunning = true
		c.opSeq++
		vm.ClearInterrupt()
		c.wdMu.Unlock()
		fn(vm)
		if waitSettle {
			settlePoll(vm, c.loop, c.bridge, c.timeout, cond, stats, closeDone)
		} else {
			closeDone()
		}
	})
	if !scheduled {
		if c.runaway.Load() {
			return ErrRunaway
		}
		return errContextClosed
	}

	// The job is dropped, not run, if the context closes first (eviction, or the
	// idle watchdog stopping a runaway page): wait on the close too, or this
	// operation would wait on done forever.
	stop := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-c.closedCh:
		}
		close(stop)
	}()
	closedErr := func() error {
		select {
		case <-done:
			return nil
		default:
		}
		if c.runaway.Load() {
			return ErrRunaway
		}
		return errContextClosed
	}

	hard := time.NewTimer(c.timeout + settleGrace)
	defer hard.Stop()
	select {
	case <-stop:
		return closedErr()
	case <-hard.C:
		interruptUntil(c.vm.Load(), "unblink: live op timeout", stop)
		return closedErr()
	case <-ctx.Done():
		interruptUntil(c.vm.Load(), "unblink: cancelled", stop)
		return ctx.Err()
	}
}

// auditTimers reads the prelude's timer audit: clamped one-shot timers still
// unfired (deferred content pending — holds the settle open) and all live wrapped
// timers of any kind (the provable-idle signal). Runs on the loop goroutine; the
// bridge's own runtime resolves the returned object.
func auditTimers(b *bridge) (clamped, live, tasks int) {
	if b == nil || b.timerAudit == nil {
		return 0, 0, 0
	}
	v, err := b.timerAudit(goja.Undefined())
	if err != nil || v == nil {
		return 0, 0, 0
	}
	obj := v.ToObject(b.vm)
	if obj == nil {
		return 0, 0, 0
	}
	if q := obj.Get("q"); q != nil {
		tasks = int(q.ToInteger())
	}
	if c := obj.Get("c"); c != nil {
		clamped = int(c.ToInteger())
	}
	if t := obj.Get("t"); t != nil {
		live = int(t.ToInteger())
	}
	return clamped, live, tasks
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
	var taskHeld time.Duration  // how long pending queued tasks have held the window
	var lastTick time.Time
	var ffQuietSince time.Time // quiet-but-for-clamped-timers since
	ffArmed := false           // previous check also saw only clamped timers live
	fastForwards := 0
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
		clamped, liveTimers, queuedTasks := auditTimers(b)
		// A pending queued task is a scheduler's continuation: the page yielded
		// in the middle of work and will pick it up again at once. That is
		// activity — it resets the quiet window like a DOM mutation does — for
		// at most settleTaskHoldMax in total, so an endless chain cannot pin the
		// settle to the budget.
		working := queuedTasks > 0 && taskHeld < settleTaskHoldMax
		if working && !lastTick.IsZero() {
			taskHeld += now.Sub(lastTick)
		}
		lastTick = now
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
		if condMet && netIdle && domQuiet && clamped == 0 && !working {
			if quietSince.IsZero() {
				quietSince = now
			}
		} else {
			quietSince = time.Time{}
		}
		// Fast-forward: the only thing still pending is long timers the clamp
		// pulled in. Waiting out their clamped delay (most of the budget) adds
		// latency and nothing else, so run them now, in the order they were
		// due, one call each so microtasks drain between them as between any
		// two timer tasks. "Nothing else" is judged the same two ways the
		// settle itself is: at once when no other timer is live (nothing can
		// run before them), otherwise after a quiet window. The prelude stops
		// pulling timers in after a few rounds, which is what ends this.
		if clamped > 0 && condMet && netIdle && domQuiet && !working && fastForwards < settleFastForwardMax && b != nil && b.timerFastForward != nil {
			if ffQuietSince.IsZero() {
				ffQuietSince = now
			}
			if (liveTimers == clamped && ffArmed) || now.Sub(ffQuietSince) >= settleQuietWindow {
				ffQuietSince, ffArmed = time.Time{}, false
				for i := 0; i < clamped && fastForwards < settleFastForwardMax; i++ {
					fired, err := b.timerFastForward(goja.Undefined(), vm.ToValue(i == 0))
					if err != nil || fired == nil || !fired.ToBoolean() {
						break
					}
					fastForwards++
				}
				// The callbacks ran inside this tick; look again on the next
				// one (their DOM changes and requests are activity like any).
				if loop.SetTimeout(tick, settleConfirm) == nil {
					closeDone()
				}
				return
			}
			ffArmed = true
		} else {
			ffQuietSince, ffArmed = time.Time{}, false
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
				stats.taskHeld = taskHeld
				stats.fastForwards = fastForwards
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
