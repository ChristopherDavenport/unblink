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

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"github.com/dop251/goja_nodejs/require"
	"golang.org/x/net/html"
)

// Defaults for the engine.
const (
	DefaultTimeout       = 2 * time.Second
	DefaultMaxConcurrent = 4
	// maxRenderBudget caps an Env.Timeout override so a caller's wait_timeout can't
	// pin a runtime (and its concurrency slot) open indefinitely.
	maxRenderBudget = 30 * time.Second
)

// Engine runs scripts over an *html.Node tree. It is safe for concurrent use;
// concurrency is bounded by a semaphore because each render holds a goja runtime.
type Engine struct {
	timeout   time.Duration
	sem       chan struct{}
	pool      chan *eventloop.EventLoop // pre-warmed, fresh, single-use loops; nil when disabled
	stop      chan struct{}
	closeOnce sync.Once
}

// Option configures an Engine.
type Option func(*config)

type config struct {
	timeout    time.Duration
	concurrent int
	prewarm    int
}

// WithTimeout sets the wall-clock budget for a single render.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithConcurrency caps how many renders may run at once.
func WithConcurrency(n int) Option { return func(c *config) { c.concurrent = n } }

// WithPrewarm keeps n fresh event loops ready in the background so runtime
// creation is off the render critical path. 0 disables the pool (loops are
// created per render). Loops are always single-use; the pool never reuses one.
func WithPrewarm(n int) Option { return func(c *config) { c.prewarm = n } }

// New returns an Engine. If a pre-warm pool is configured, call Close to stop its
// background refiller.
func New(opts ...Option) *Engine {
	c := config{timeout: DefaultTimeout, concurrent: DefaultMaxConcurrent}
	for _, opt := range opts {
		opt(&c)
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.concurrent <= 0 {
		c.concurrent = DefaultMaxConcurrent
	}
	e := &Engine{timeout: c.timeout, sem: make(chan struct{}, c.concurrent)}
	if c.prewarm > 0 {
		e.pool = make(chan *eventloop.EventLoop, c.prewarm)
		e.stop = make(chan struct{})
		go e.refill()
	}
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

// Close stops the pre-warm refiller. It is idempotent and safe to call on an
// engine without a pool.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		if e.stop != nil {
			close(e.stop)
		}
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
	if len(scripts) == 0 && len(modules) == 0 {
		return nil // nothing to run; leave the tree untouched
	}

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

	loop := e.takeLoop()
	loop.Start()
	var vmRef atomic.Pointer[goja.Runtime]
	var b *bridge
	var waitMet bool
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
		// reqTimeout is the render budget so a wait_timeout override also gives the
		// page's own fetches longer to complete (else the awaited content never lands).
		b = newBridge(vm, loop, doc, base, env.Transport, env.Cookies, ctx, budget)
		b.install()
		// Stubs simplest to express in JS (storage, observers, rAF, and the
		// network-aware fetch/XHR). Failure here is non-fatal.
		_, _ = vm.RunString(preludeJS)
		b.runScripts(scripts)
		if len(modules) > 0 {
			b.runModules(modules, parseImportMap(doc))
		}
		b.fireLifecycle()
		if env.Diag != nil {
			*env.Diag = b.collectDiagnostics()
		}
		settlePoll(loop, b, budget, env.Wait, &waitMet, closeDone)
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
	select {
	case <-done:
	case <-hard.C:
		if vm := vmRef.Load(); vm != nil {
			vm.Interrupt("unblink: render timeout")
		}
		<-done
	case <-ctx.Done():
		if vm := vmRef.Load(); vm != nil {
			vm.Interrupt("unblink: context cancelled")
		}
		<-done
		rerr = ctx.Err()
	}
	// Terminate before the caller reads doc so no background setInterval mutates the
	// tree concurrently with extraction. Terminate joins the loop goroutine, giving
	// the happens-before edge for the b/waitMet reads below.
	loop.Terminate()
	if env.Diag != nil {
		env.Diag.WaitRequested = !env.Wait.empty()
		env.Diag.WaitMet = waitMet
		if b != nil && b.pendingNav != nil {
			env.Diag.PendingNavigation = b.pendingNav.String()
		}
	}
	return rerr
}
