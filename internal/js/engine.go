// Package js is unblink's quarantined JavaScript engine. It runs a page's inline
// scripts against a minimal DOM backed by the canonical golang.org/x/net/html
// tree, mutating that tree in place so the rest of the pipeline (extract, reduce,
// emit) sees the post-script content with no changes of its own.
//
// A *goja.Runtime is not goroutine-safe, so each Render gets its own runtime and
// event loop, used exactly once, bounded by a concurrency semaphore. An optional
// pre-warm pool creates those (fresh, never-reused) loops in the background so the
// runtime-creation cost is off the render critical path.
package js

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/christopherdavenport/unblink/internal/webext"
	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"github.com/dop251/goja_nodejs/require"
	"golang.org/x/net/html"
)

// Defaults for the engine. The 5s render budget fits real code-split SPAs
// (shell + lazy chunks + data fetches); 2s proved too tight for them, and a
// settled page returns well before the budget anyway.
const (
	DefaultTimeout       = 5 * time.Second
	DefaultMaxConcurrent = 4
	// maxRenderBudget caps an Env.Timeout override so a caller's wait_timeout can't
	// pin a runtime (and its concurrency slot) open indefinitely.
	maxRenderBudget = 30 * time.Second
	// timerClampMargin is how far before the budget deadline a clamped one-shot
	// timer fires, leaving room for its callback's DOM mutation to be captured by
	// the settle poll before the snapshot is taken.
	timerClampMargin = 600 * time.Millisecond
)

// Engine runs scripts over an *html.Node tree. It is safe for concurrent use;
// concurrency is bounded by a semaphore because each render holds a goja runtime.
type Engine struct {
	timeout   time.Duration
	sem       chan struct{}
	pool      chan *eventloop.EventLoop // pre-warmed, fresh, single-use loops; nil when disabled
	stop      chan struct{}
	closeOnce sync.Once
	assets    *assetCache    // TTL'd script/module/bundle cache; nil when disabled
	webdriver bool           // navigator.webdriver; true unless the operator opted into --tls-mimic parity
	memGuard  *memGuard      // heap watchdog over all live/one-shot runtimes; nil when disabled
	extHost   *ExtensionHost // loaded WebExtensions (network rules); nil when none configured
}

// Option configures an Engine.
type Option func(*config)

type config struct {
	timeout    time.Duration
	concurrent int
	prewarm    int
	assetTTL   time.Duration
	webdriver  bool
	memLimit   uint64
	extensions []*webext.Bundle
}

// WithTimeout sets the wall-clock budget for a single render.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithConcurrency caps how many renders may run at once.
func WithConcurrency(n int) Option { return func(c *config) { c.concurrent = n } }

// WithPrewarm keeps n fresh event loops ready in the background so runtime
// creation is off the render critical path. 0 disables the pool (loops are
// created per render). Loops are always single-use; the pool never reuses one.
func WithPrewarm(n int) Option { return func(c *config) { c.prewarm = n } }

// WithAssetCache caches page-JS *asset* downloads (external scripts, module
// sources) and esbuild bundle outputs across renders for ttl. Page data
// requests (fetch/XHR) are never cached. 0 disables (the default).
func WithAssetCache(ttl time.Duration) Option { return func(c *config) { c.assetTTL = ttl } }

// WithWebdriver sets navigator.webdriver. The default is true — unblink is
// automation and self-identifies (the UA string carries "unblink" too). The
// browser passes false when the operator enables --tls-mimic, extending that
// opt-in fingerprint parity to the JS environment.
func WithWebdriver(v bool) Option { return func(c *config) { c.webdriver = v } }

// WithMemoryLimit caps the Go heap that running page JS may grow before every
// live runtime is interrupted (see memGuard / ADR-0003). 0 disables the guard.
func WithMemoryLimit(bytes uint64) Option { return func(c *config) { c.memLimit = bytes } }

// WithExtensions loads WebExtensions into the engine. Their state (Phase 1: the
// declarativeNetRequest network rules) is engine-lifetime and shared read-only across
// every render and live session, so a page-JS subrequest matching a block rule is
// cancelled before it leaves the process.
func WithExtensions(bundles []*webext.Bundle) Option {
	return func(c *config) { c.extensions = bundles }
}

// New returns an Engine. If a pre-warm pool is configured, call Close to stop its
// background refiller.
func New(opts ...Option) *Engine {
	c := config{timeout: DefaultTimeout, concurrent: DefaultMaxConcurrent, webdriver: true}
	for _, opt := range opts {
		opt(&c)
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.concurrent <= 0 {
		c.concurrent = DefaultMaxConcurrent
	}
	e := &Engine{timeout: c.timeout, sem: make(chan struct{}, c.concurrent), assets: newAssetCache(c.assetTTL), webdriver: c.webdriver, memGuard: newMemGuard(c.memLimit), extHost: newExtensionHost(c.extensions)}
	if c.prewarm > 0 {
		e.pool = make(chan *eventloop.EventLoop, c.prewarm)
		e.stop = make(chan struct{})
		go e.refill()
	}
	// Start any extension background worker on its own eventloop, warm for the process
	// lifetime (its filter-list setup runs once, not per render).
	e.extHost.startBackground(e.memGuard)
	return e
}

// rejectRequire is the source loader installed on every runtime's require registry.
// goja_nodejs's DefaultSourceLoader is os.Open, so a zero-value registry lets page
// JS read arbitrary host files (e.g. require('/etc/passwd'), and .json paths are
// parsed and handed back to the script) and exfiltrate them via fetch. We refuse
// all module source loads instead. ESM `import` is unaffected: it is bundled by
// esbuild through the guarded network transport, not through require().
func rejectRequire(string) ([]byte, error) {
	return nil, errors.New("unblink: require() is disabled")
}

// newLoop creates a fresh, single-use event loop hardened for running untrusted
// page JavaScript: require() is neutered (see rejectRequire), and the built-in
// console is disabled because its default printer writes to os.Stdout, which is
// reserved for the MCP JSON-RPC transport. A safe no-op console is provided to the
// page by preludeJS so scripts that call console.* don't ReferenceError.
func newLoop() *eventloop.EventLoop {
	reg := require.NewRegistry(require.WithLoader(rejectRequire))
	return eventloop.NewEventLoop(eventloop.WithRegistry(reg), eventloop.EnableConsole(false))
}

// refill keeps the pool topped up with fresh event loops, blocking (no busy-wait)
// whenever the pool is full until a render frees a slot.
func (e *Engine) refill() {
	for {
		loop := newLoop()
		select {
		case e.pool <- loop:
		case <-e.stop:
			return
		}
	}
}

// takeLoop returns a fresh event loop: from the pool when one is ready, otherwise
// created inline (cold start, or a burst beyond the pool size). It never blocks on
// the pool.
func (e *Engine) takeLoop() *eventloop.EventLoop {
	if e.pool != nil {
		select {
		case loop := <-e.pool:
			return loop
		default:
		}
	}
	return newLoop()
}

// Close stops the pre-warm refiller and the memory-guard watchdog. It is
// idempotent and safe to call on an engine without a pool.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		if e.stop != nil {
			close(e.stop)
		}
		e.extHost.Close()
		e.memGuard.close()
	})
}

// Render executes the page's scripts against a minimal DOM, mutating doc in
// place. env supplies the network transport (external scripts, fetch/XHR,
// modules) and cookie jar (document.cookie); all fields may be nil/zero and degrade
// gracefully. It is best-effort: on timeout or error, whatever mutations completed
// before the cutoff are kept. base is the page URL.
//
// Like the persistent live Context, the render drives a Start()ed loop and polls for
// a quiet period (no in-flight network + no DOM mutations) rather than blocking on a
// natural drain: this early-exits on pages with persistent timers and honors an
// optional Env.Wait gate. The loop is always Terminate()d before returning so no
// background timer mutates doc while the caller reads it.
func (e *Engine) Render(ctx context.Context, doc *html.Node, base *url.URL, env Env) error {
	if doc == nil {
		return nil
	}
	scripts := collectScripts(doc)
	modules := collectModuleScripts(doc)
	if len(scripts) == 0 && len(modules) == 0 && e.extHost == nil {
		return nil // nothing to run; leave the tree untouched
	}
	// With an extension loaded, even a script-less page needs a render: its content
	// scripts must run and its cosmetic rules must be applied (a browser does the same).

	// Bound concurrency (each render holds a runtime's worth of memory).
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	budget := e.timeout
	if env.Timeout > 0 {
		budget = env.Timeout
		if budget > maxRenderBudget {
			budget = maxRenderBudget
		}
	}

	start := time.Now()
	loop := e.takeLoop()
	loop.Start()
	var vmRef atomic.Pointer[goja.Runtime]
	var b *bridge
	var stats settleStats
	// setupDone/execDone are written on the loop goroutine and read only after
	// loop.Terminate() joins it (same happens-before edge as b/stats below).
	var setupDone, execDone time.Time
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	scheduled := loop.RunOnLoop(func(vm *goja.Runtime) {
		// A Go panic here would crash the loop goroutine; recover and unblock the
		// caller. (JS exceptions/interrupts surface as errors, not panics.)
		defer func() {
			if r := recover(); r != nil {
				closeDone()
			}
		}()
		vmRef.Store(vm)
		e.memGuard.register(vm)
		// reqTimeout is the render budget so a wait_timeout override also gives the
		// page's own fetches longer to complete (else the awaited content never lands).
		b = newBridge(vm, loop, doc, base, env.Transport, env.Cookies, env.Storage, env.SessionStorage, ctx, budget, e.extHost)
		b.assets = e.assets
		b.webdriver = e.webdriver
		b.install()
		// Kick off the concurrent script-body prefetch before the prelude runs so
		// the fetches overlap prelude execution and each other; runScripts blocks
		// on each body in document order.
		b.startPrefetch(scripts)
		// Warm the modulepreload/preload chunk graph concurrently too, so the serial
		// runtime import() path (dynimport) hits the asset cache instead of fetching
		// each code-split chunk one at a time.
		b.startModulePreload(doc)
		// Timer clamp deadline (one-shot renders only): a wall-clock instant, read
		// by the prelude timer wrapper, past which a long one-shot timer is pulled
		// in so its content still materializes. Live sessions leave this unset.
		clamp := budget - timerClampMargin
		if clamp < 0 {
			clamp = 0
		}
		_ = vm.Set("__unblinkTimerDeadlineMs", time.Now().Add(clamp).UnixMilli())
		// Stubs simplest to express in JS (storage, observers, rAF), then the
		// web-platform API layer (encoding, fetch classes, viewport/matchMedia,
		// Intl, messaging). Failure here is non-fatal.
		_, _ = vm.RunProgram(preludeProgram)
		_, _ = vm.RunProgram(preludeAPIProgram)
		setupDone = time.Now()
		// Tell the extension background this tab navigated to the page, before any
		// request, so it builds a per-tab page store and filters with the right context.
		e.extHost.notifyNavigation(b.tabID, baseURLString(base))
		// Extension content scripts share the page world (ADR 0010). Gather their
		// hiding CSS once, then inject JS at each run_at around the page's own scripts.
		b.injectContentScriptCSS()
		b.injectContentScripts(webext.RunAtStart)
		b.runScripts(scripts)
		if len(modules) > 0 {
			b.runModules(modules, parseImportMap(doc))
		}
		b.injectContentScripts(webext.RunAtEnd)
		b.fireLifecycle()
		b.injectContentScripts(webext.RunAtIdle)
		execDone = time.Now()
		if env.Diag != nil {
			*env.Diag = b.collectDiagnostics()
		}
		settlePoll(vm, loop, b, budget, env.Wait, &stats, closeDone)
	})
	if !scheduled {
		loop.Terminate()
		return nil
	}

	// Hard watchdog past the settle budget: fires only if a synchronous script wedges
	// the loop before the settle poll can run.
	hard := time.NewTimer(budget + settleGrace)
	defer hard.Stop()

	var rerr error
	interrupted := false
	select {
	case <-done:
	case <-hard.C:
		interrupted = true
		if vm := vmRef.Load(); vm != nil {
			vm.Interrupt("unblink: render timeout")
		}
		<-done
	case <-ctx.Done():
		interrupted = true
		if vm := vmRef.Load(); vm != nil {
			vm.Interrupt("unblink: context cancelled")
		}
		<-done
		rerr = ctx.Err()
	}
	// Terminate before the caller reads doc so no background setInterval mutates the
	// tree concurrently with extraction. Terminate joins the loop goroutine, giving
	// the happens-before edge for the b/stats reads below.
	loop.Terminate()
	if vm := vmRef.Load(); vm != nil {
		e.memGuard.unregister(vm)
	}
	// Flatten shadow content (resolving <slot> distribution) into the light tree so
	// extraction sees the composed page. Runs after Terminate (no concurrent mutation)
	// and is a no-op unless the page attached a shadow root. The runtime is discarded, so
	// this may splice destructively into doc.
	if b != nil {
		b.ComposeShadowInto(doc)
		// Cosmetic filtering: detach extension-hidden ad markup from the frozen tree so
		// it never reaches the Markdown. Post-Terminate (like shadow composition) means
		// page JS never observes the removal — mirroring CSS hiding's lack of events.
		b.applyCosmeticFilters(doc)
		// The render's tab is gone; let the extension drop its per-tab page store.
		e.extHost.notifyTabRemoved(b.tabID)
	}
	if env.Diag != nil {
		// Timing: a wedged script can leave setupDone/execDone unset; attribute the
		// whole elapsed time to the last stage that was reached.
		end := time.Now()
		env.Diag.TotalDur = end.Sub(start)
		switch {
		case setupDone.IsZero():
			env.Diag.SetupDur = env.Diag.TotalDur
		case execDone.IsZero():
			env.Diag.SetupDur = setupDone.Sub(start)
			env.Diag.ExecDur = end.Sub(setupDone)
		default:
			env.Diag.SetupDur = setupDone.Sub(start)
			env.Diag.ExecDur = execDone.Sub(setupDone)
			env.Diag.SettleDur = end.Sub(execDone)
		}
		env.Diag.WaitRequested = !env.Wait.empty()
		env.Diag.WaitMet = stats.met
		env.Diag.DeadlineHit = stats.deadline || interrupted
		env.Diag.DOMBusy = stats.domBusy
		env.Diag.NetPending = int(stats.pending)
		env.Diag.TimersPending = stats.timersPending
		env.Diag.SettledIdle = stats.idleExit && !interrupted
		if b != nil {
			if interrupted {
				// The settle never closed on its own (a wedged script or cancellation),
				// so its close-tick stats are empty; read the live counter instead.
				env.Diag.NetPending = int(b.pending.Load())
			}
			if b.netCount != nil {
				env.Diag.NetRequests = int(b.netCount.total.Load())
				env.Diag.NetFailed = int(b.netCount.failed.Load())
				env.Diag.NetBytes = b.netCount.bytesDown.Load()
			}
			if b.pendingNav != nil {
				env.Diag.PendingNavigation = b.pendingNav.String()
			}
			// Post-Terminate (settle complete + loop joined): the request/console
			// logs now include everything the settle waited on.
			b.fillCaptureLogs(env.Diag)
		}
	}
	return rerr
}
