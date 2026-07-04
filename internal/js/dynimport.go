package js

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/dop251/goja"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

// dynImportGlobal is the runtime loader that lowered dynamic import() calls target.
// lowerDynamicImport rewrites esbuild's `__toESM(require(SPEC))` (its dynamic-import
// lowering) to `__unblinkImport((SPEC))`; this loader resolves SPEC, fetches+bundles
// the chunk graph OFF the loop goroutine (so sibling import()s overlap instead of
// serializing), then runs it ON the loop and resolves a Promise with its namespace —
// a plain {__esModule:true, default, ...named} object, so `(await import(spec)).default`
// yields the chunk's real default export. The disabled global `require` is deliberately
// left alone, so the require()-is-off security control (TestRequireDisabled) still holds.
const dynImportGlobal = "__unblinkImport"

// dynImportResultGlobal is the transient global the chunk IIFE assigns its namespace to;
// a Format:IIFE bundle can't otherwise hand a value back to the caller. It is read and
// cleared within the same on-loop RunOnLoop job that ran the chunk, and those jobs are
// serialized, so concurrently-loading chunks never race on it.
const dynImportResultGlobal = "__unblinkChunkResult"

// dynImportConcurrency bounds how many dynamic-import chunks fetch+bundle off-loop at
// once (see bridge.dynSem). 16 matches the module-warming worker pool: wide enough to
// collapse a code-split graph's serial round trips, bounded enough that a page firing
// hundreds of import()s can't spawn an unbounded number of concurrent esbuild builds.
const dynImportConcurrency = 16

// installDynamicImport binds the __unblinkImport loader as an async function: each call
// returns a Promise that settles once the (off-loop) chunk fetch+bundle and the (on-loop)
// run complete. Unlike a plain fetch it also bundles the chunk's static graph via esbuild;
// that CPU work runs off-loop too. Any failure (no transport, resolve/fetch/bundle/run
// error) rejects the Promise, so the lowered `.then` rejects and the import degrades
// gracefully to the "reject, don't blank the page" path.
func (b *bridge) installDynamicImport() {
	_ = b.vm.Set(dynImportGlobal, func(call goja.FunctionCall) goja.Value {
		return b.importPromise(call.Argument(0).String())
	})
}

// importPromise resolves spec on-loop, then fetches+bundles the chunk off-loop (bracketed
// by the pending/keepalive window, exactly like fetchPromise, so the settle poll waits for
// it — ADR 0004) and runs it on-loop. It returns a Promise for the chunk's namespace.
func (b *bridge) importPromise(spec string) goja.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()

	if b.transport == nil {
		_ = reject(vm.ToValue("import(): JS networking is disabled"))
		return vm.ToValue(promise)
	}
	abs, err := b.moduleResolve("", spec)
	if err != nil {
		_ = reject(vm.ToValue(fmt.Sprintf("import(%q): %v", spec, err)))
		return vm.ToValue(promise)
	}
	// `import * as __m` never errors on a missing default export (unlike `export {default}`);
	// Object.assign onto a fresh {__esModule:true} yields a plain, extensible namespace whose
	// .default / named exports resolve directly (the lowered call site no longer wraps this in
	// esbuild's __toESM). globalThis escapes the function scope that Format:IIFE wraps around
	// the bundle.
	entry := "import * as __m from " + strconv.Quote(abs) + ";\n" +
		"globalThis." + dynImportResultGlobal + " = Object.assign({ __esModule: true }, __m);\n"
	bkey := bundleKey(b.docBaseNow(), entry, nil)

	b.pending.Add(1) // off-loop fetch+bundle in flight; bracketed by the keepalive window
	keep := b.acquireKeepalive()
	go func() {
		bundled, berr := b.bundleDynamicChunk(entry, bkey)
		_ = b.loop.RunOnLoop(func(vm *goja.Runtime) {
			defer func() {
				b.releaseKeepalive(keep)
				b.pending.Add(-1)
			}()
			if berr != nil {
				_ = reject(vm.ToValue(fmt.Sprintf("import(%q): %v", spec, berr)))
				return
			}
			ns, rerr := b.runDynamicChunk(bundled)
			if rerr != nil {
				_ = reject(vm.ToValue(fmt.Sprintf("import(%q): %v", spec, rerr)))
				return
			}
			_ = resolve(ns)
		})
		// If RunOnLoop returned false the loop was terminated; nothing to clean up
		// (the response bodies were already fully read by the transport).
	}()
	return vm.ToValue(promise)
}

// bundleDynamicChunk fetches and bundles the ESM chunk graph at entry (through the same
// SSRF-guarded transport + modulePlugin as runModules) and returns the IIFE bundle. Runs
// OFF the loop goroutine, so it must touch only concurrency-safe bridge state: the mutex-
// guarded asset cache, the atomic/mutex transport, and the immutable-during-exec prefetch
// map. It never reads mutable DOM/base state (moduleResolve's absolute-URL fast path keeps
// esbuild's resolver off docBaseNow). Bounded by b.dynSem.
func (b *bridge) bundleDynamicChunk(entry, bkey string) ([]byte, error) {
	// Same TTL'd bundle cache as runModules: repeat renders of a code-split SPA skip
	// re-fetching and re-bundling every lazy chunk.
	if bundled, hit := b.assets.get(bkey); hit {
		return bundled, nil
	}
	b.dynSem <- struct{}{}
	defer func() { <-b.dynSem }()
	// Re-check after acquiring the slot: a concurrent import of the same chunk may have
	// filled the cache while we waited.
	if bundled, hit := b.assets.get(bkey); hit {
		return bundled, nil
	}
	res := esbuild.Build(esbuild.BuildOptions{
		Stdin: &esbuild.StdinOptions{
			Contents:   entry,
			Loader:     esbuild.LoaderJS,
			Sourcefile: "dynimport.js",
			ResolveDir: "/",
		},
		Bundle:    true,
		Write:     false,
		Format:    esbuild.FormatIIFE,
		Target:    esbuild.ESNext,
		Supported: map[string]bool{"dynamic-import": false},
		LogLevel:  esbuild.LogLevelSilent,
		Plugins:   []esbuild.Plugin{b.modulePlugin(nil)},
	})
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("bundle: %s", res.Errors[0].Text)
	}
	if len(res.OutputFiles) == 0 {
		return nil, errors.New("bundle produced no output")
	}
	bundled := res.OutputFiles[0].Contents
	b.assets.put(bkey, bundled)
	return bundled, nil
}

// runDynamicChunk compiles and runs a bundled chunk on the loop goroutine, returning the
// namespace the IIFE published via dynImportResultGlobal. The set-then-read of that global
// is confined to this one RunOnLoop job (jobs are serialized), so concurrent chunk loads
// never race on it. RunProgram is not re-entrant here (the job runs at top of stack).
func (b *bridge) runDynamicChunk(bundled []byte) (goja.Value, error) {
	prog, err := compileCached("dynimport.js", string(bundled))
	if err != nil {
		return nil, err
	}
	if _, err := b.vm.RunProgram(prog); err != nil {
		return nil, err
	}
	ns := b.vm.Get(dynImportResultGlobal)
	_ = b.vm.Set(dynImportResultGlobal, goja.Undefined())
	if ns == nil || goja.IsUndefined(ns) || goja.IsNull(ns) {
		return nil, errors.New("chunk produced no namespace")
	}
	return ns, nil
}
