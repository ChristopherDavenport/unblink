package js

import (
	"fmt"

	"github.com/dop251/goja"
)

// dynImportGlobal is the runtime loader that lowered dynamic import() calls target.
// lowerDynamicImport rewrites esbuild's `__toESM(require(SPEC))` (its dynamic-import
// lowering) to `__unblinkImport((SPEC))`; this loader resolves SPEC and loads its
// module graph through the per-render registry (moduleregistry.go), which shares one
// instance of each module URL across importers, then resolves a Promise with the
// module namespace so `(await import(spec)).default` yields its real default export.
// The disabled global `require` is deliberately left alone, so the require()-is-off
// security control (TestRequireDisabled) still holds.
const dynImportGlobal = "__unblinkImport"

// dynImportConcurrency bounds how many modules fetch+transform off-loop at once (see
// bridge.dynSem). 16 matches the module-warming worker pool: wide enough to collapse a
// code-split graph's serial round trips, bounded enough that a page firing hundreds of
// import()s can't spawn an unbounded number of concurrent esbuild transforms.
const dynImportConcurrency = 16

// installDynamicImport binds the __unblinkImport loader as an async function: each call
// returns a Promise that settles once the (off-loop) module-graph instantiate and the
// (on-loop) evaluate complete. Any failure (no transport, resolve/fetch/transform/eval
// error) rejects the Promise, so the lowered `.then` rejects and the import degrades
// gracefully to the "reject, don't blank the page" path.
func (b *bridge) installDynamicImport() {
	_ = b.vm.Set(dynImportGlobal, func(call goja.FunctionCall) goja.Value {
		return b.importPromise(call.Argument(0).String())
	})
}

// importPromise returns a Promise for spec's module namespace, loaded through the
// per-render module registry so a dependency shared by several import()s resolves to
// ONE shared instance (module-map semantics — see moduleregistry.go). The off-loop
// instantiate phase (fetch + transform the whole static graph) is bracketed by the
// pending/keepalive window exactly like fetchPromise, so the settle poll waits for it
// (ADR 0004); the on-loop evaluate phase runs inside importModule.
func (b *bridge) importPromise(spec string) goja.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()

	if b.transport == nil {
		_ = reject(vm.ToValue("import(): JS networking is disabled"))
		return vm.ToValue(promise)
	}
	// Resolve on-loop: a relative specifier reads docBaseNow, which must not be
	// touched from the off-loop goroutine (it races on-loop navigation updates).
	abs, err := b.moduleResolve("", spec)
	if err != nil {
		_ = reject(vm.ToValue(fmt.Sprintf("import(%q): %v", spec, err)))
		return vm.ToValue(promise)
	}

	b.pending.Add(1) // off-loop instantiate in flight; bracketed by the keepalive window
	keep := b.acquireKeepalive()
	go func() {
		b.reg.instantiateGraph(b, abs) // fetch + transform the whole static graph off-loop
		_ = b.loop.RunOnLoop(func(vm *goja.Runtime) {
			defer func() {
				b.releaseKeepalive(keep)
				b.pending.Add(-1)
			}()
			ns, err := b.reg.evaluate(b, abs) // run bodies in post-order, on-loop
			if err != nil {
				_ = reject(vm.ToValue(fmt.Sprintf("import(%q): %v", spec, err)))
				return
			}
			_ = resolve(ns)
		})
		// RunOnLoop==false means the loop was terminated; nothing to clean up.
	}()
	return vm.ToValue(promise)
}
