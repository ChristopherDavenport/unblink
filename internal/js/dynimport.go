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
// lowering) to `__toESM(__unblinkImportSync(SPEC))`; this loader fetches, bundles, and
// runs the chunk at SPEC and returns its namespace as a plain {__esModule:true, default,
// ...named} object, so the surrounding __toESM() is a passthrough and
// `(await import(spec)).default` yields the chunk's real default export. The disabled
// global `require` is deliberately left alone, so the require()-is-off security control
// (TestRequireDisabled) still holds.
const dynImportGlobal = "__unblinkImportSync"

// dynImportResultGlobal is the transient global the chunk IIFE assigns its namespace to;
// a Format:IIFE bundle can't otherwise hand a value back to the caller. It is read and
// cleared immediately after the synchronous run, so sequential imports never race.
const dynImportResultGlobal = "__unblinkChunkResult"

// installDynamicImport binds the __unblinkImportSync loader. It runs synchronously on the
// loop goroutine — exactly like runModules' esbuild.Build — blocking until the chunk
// graph is fetched and bundled, so no keepalive/pending bracket is needed (there is no
// off-loop continuation: the whole load completes within the calling microtask). Any
// failure (no transport, fetch/bundle/parse error) throws, so the lowered `.then` rejects
// and the import degrades gracefully to the Phase A "reject, don't blank the page" path.
func (b *bridge) installDynamicImport() {
	_ = b.vm.Set(dynImportGlobal, func(call goja.FunctionCall) goja.Value {
		spec := call.Argument(0).String()
		ns, err := b.loadDynamicChunk(spec)
		if err != nil {
			panic(b.vm.NewGoError(fmt.Errorf("import(%q): %w", spec, err)))
		}
		return ns
	})
}

// loadDynamicChunk resolves, fetches, bundles, and runs the ESM chunk at spec (through
// the same SSRF-guarded transport + modulePlugin as runModules) and returns its module
// namespace.
func (b *bridge) loadDynamicChunk(spec string) (goja.Value, error) {
	if b.transport == nil {
		return nil, errors.New("JS networking is disabled")
	}
	abs, err := b.moduleResolve("", spec)
	if err != nil {
		return nil, err
	}
	// `import * as __m` never errors on a missing default export (unlike `export {default}`);
	// Object.assign onto a fresh {__esModule:true} yields a plain, extensible object whose
	// __esModule flag makes the call site's __toESM() a passthrough (so .default / named
	// exports resolve). globalThis escapes the function scope that Format:IIFE wraps the
	// bundle in.
	entry := "import * as __m from " + strconv.Quote(abs) + ";\n" +
		"globalThis." + dynImportResultGlobal + " = Object.assign({ __esModule: true }, __m);\n"
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
	prog, err := compileCached("dynimport.js", string(res.OutputFiles[0].Contents))
	if err != nil {
		return nil, err
	}
	// Re-entrant RunProgram is safe: goja pushes/pops the caller's frame when the call
	// stack is non-empty (we are inside the lowered import's .then microtask).
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
