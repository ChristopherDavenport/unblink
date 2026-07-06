package js

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/dop251/goja"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

// The module registry gives dynamic import() browser-correct module-map semantics:
// every module URL is fetched, transformed, and evaluated exactly ONCE per render,
// and its live namespace is shared across every importer. The prior design bundled
// each import()'s whole static graph independently (esbuild IIFE), so a dependency
// shared by two import()s — e.g. React imported by both an Astro island's component
// chunk and its renderer chunk — was duplicated. React keeps process-global mutable
// state (the hooks dispatcher); with two copies, ReactDOM sets copy A's dispatcher
// while the component reads copy B's (null), throwing "Cannot read property
// 'useMemoCache' of null" and rendering the island empty. Sharing one instance fixes
// that, and any other cross-chunk singleton / instanceof identity.
//
// Loading is two-phase to stay cycle-safe and to keep goja's single-threaded
// evaluation on the loop goroutine:
//   - instantiate (off-loop): fetch each module and esbuild-transform it to a
//     self-contained CommonJS unit, discovering its static deps; traverse the whole
//     reachable graph, deduping by URL. Fetch+transform of a module never waits on
//     its deps, so an import cycle can't deadlock here.
//   - evaluate (on-loop): run each module body once in post-order, resolving its
//     static requires from already-evaluated instances; a cycle yields the partial
//     (in-progress) exports, matching CommonJS.

type moduleInstance struct {
	url  string
	cjs  string   // esbuild CommonJS transform of the source
	deps []string // resolved absolute URLs of static require() dependencies

	instErr  error         // fetch/transform failure for this module
	selfDone chan struct{} // closed when this module's own fetch+transform finishes (not its deps)

	// Evaluation state — touched only on the loop goroutine.
	evaluated  bool
	evaluating bool
	evalErr    error
	exports    goja.Value // module.exports; the namespace importers receive
}

type moduleRegistry struct {
	mu    sync.Mutex
	insts map[string]*moduleInstance
}

func newModuleRegistry() *moduleRegistry {
	return &moduleRegistry{insts: make(map[string]*moduleInstance)}
}

// lookup returns the instance for url, guarding the map against a concurrent
// off-loop instantiateSelf write (evaluate reads it from the loop goroutine while
// another import()'s instantiate phase may still be populating the map).
func (r *moduleRegistry) lookup(url string) *moduleInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.insts[url]
}

// instantiateGraph fetches and transforms every module reachable from root,
// deduping by URL against the shared registry. Concurrent, cycle-safe: a `seen`
// set bounds this traversal and instantiateSelf dedups across traversals/imports.
// After it returns, every reachable module has its cjs+deps set (or instErr).
func (r *moduleRegistry) instantiateGraph(b *bridge, root string) {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		seen  = map[string]bool{root: true}
		visit func(string)
	)
	visit = func(url string) {
		defer wg.Done()
		inst := r.instantiateSelf(b, url)
		if inst.instErr != nil {
			return
		}
		mu.Lock()
		for _, d := range inst.deps {
			if !seen[d] {
				seen[d] = true
				wg.Add(1)
				go visit(d)
			}
		}
		mu.Unlock()
	}
	wg.Add(1)
	go visit(root)
	wg.Wait()
}

// instantiateSelf fetches and transforms a single module (not its deps), deduped
// by URL: the first caller does the work, concurrent callers wait on selfDone.
// Fetching a module never waits on its dependencies, so cycles can't deadlock.
func (r *moduleRegistry) instantiateSelf(b *bridge, url string) *moduleInstance {
	r.mu.Lock()
	if inst, ok := r.insts[url]; ok {
		r.mu.Unlock()
		<-inst.selfDone
		return inst
	}
	inst := &moduleInstance{url: url, selfDone: make(chan struct{})}
	r.insts[url] = inst
	r.mu.Unlock()
	defer close(inst.selfDone)

	// Bound concurrent fetch+transform like the old dynamic-import path.
	b.dynSem <- struct{}{}
	defer func() { <-b.dynSem }()

	src, err := b.fetchModuleSource(url)
	if err != nil {
		inst.instErr = err
		return inst
	}
	cjs, err := transformModuleCJS(string(src))
	if err != nil {
		inst.instErr = fmt.Errorf("transform %s: %w", url, err)
		return inst
	}
	inst.cjs = cjs
	for _, spec := range staticRequires(cjs) {
		if abs, err := b.moduleResolve(url, spec); err == nil {
			inst.deps = append(inst.deps, abs)
		}
	}
	return inst
}

// evaluate runs a module body once, on the loop goroutine, in post-order. A module
// currently being evaluated (a cycle) returns its in-progress exports.
func (r *moduleRegistry) evaluate(b *bridge, url string) (goja.Value, error) {
	inst := r.lookup(url)
	if inst == nil {
		return nil, fmt.Errorf("module %s was not instantiated", url)
	}
	if inst.instErr != nil {
		return nil, inst.instErr
	}
	if inst.evaluated {
		return inst.exports, inst.evalErr
	}
	if inst.evaluating {
		return inst.exports, nil // cycle: hand back the partial exports
	}
	inst.evaluating = true

	vm := b.vm
	moduleObj := vm.NewObject()
	exportsObj := vm.NewObject()
	_ = moduleObj.Set("exports", exportsObj)
	inst.exports = exportsObj // pre-register so a cyclic require sees this object

	requireFn := func(call goja.FunctionCall) goja.Value {
		spec := call.Argument(0).String()
		durl, err := b.moduleResolve(url, spec)
		if err != nil {
			panic(vm.NewTypeError("cannot resolve %q from %s: %v", spec, url, err))
		}
		ns, err := r.evaluate(b, durl)
		if err != nil {
			panic(vm.NewTypeError("%v", err))
		}
		return ns
	}

	prog, err := compileCached("module.js", "(function(require, module, exports){\n"+inst.cjs+"\n})")
	if err != nil {
		inst.evaluated, inst.evalErr = true, err
		return nil, err
	}
	fnVal, err := vm.RunProgram(prog)
	if err != nil {
		inst.evaluated, inst.evalErr = true, err
		return nil, err
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		inst.evaluated, inst.evalErr = true, errors.New("module wrapper is not callable")
		return nil, inst.evalErr
	}
	if _, err := fn(goja.Undefined(), vm.ToValue(requireFn), moduleObj, exportsObj); err != nil {
		inst.evaluated, inst.evalErr = true, err
		return nil, err
	}
	inst.exports = moduleObj.Get("exports") // esbuild reassigns module.exports
	inst.evaluated = true
	return inst.exports, nil
}

// fetchModuleSource returns a module's raw source, reusing the asset cache and any
// in-flight prefetch warm, then a guarded transport fetch — mirroring modulePlugin's
// OnLoad so the two loaders share cached bodies.
func (b *bridge) fetchModuleSource(url string) ([]byte, error) {
	if body, ok := b.assets.get(assetKey(url)); ok {
		return body, nil
	}
	if body, ok, found := b.prefetched(url); found && ok {
		return body, nil
	}
	if b.transport == nil {
		return nil, errors.New("import(): JS networking is disabled")
	}
	ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
	defer cancel()
	res, err := b.transport.Do(ctx, "GET", url, nil, nil)
	if err != nil {
		return nil, err
	}
	if res.Status >= 400 {
		return nil, fmt.Errorf("module %s: status %d", url, res.Status)
	}
	b.assets.put(assetKey(url), res.Body)
	return res.Body, nil
}

// dynDynImportRE matches esbuild's lowered dynamic import
// (Promise.resolve().then(() => __toESM(require(SPEC)))) so it can be rewritten to
// the async loader, distinct from a static default import's __toESM(require(SPEC)).
var dynDynImportRE = regexp.MustCompile(`Promise\.resolve\(\)\.then\(\(\)\s*=>\s*__toESM\(require\((.*?)\)\)\)`)

// transformModuleCJS turns one ES module's source into a self-contained CommonJS
// unit goja can run: static imports become require() calls (resolved against the
// module registry), exports become module.exports, and a dynamic import() becomes
// an __unblinkImport() call (the async registry loader) rather than a synchronous
// require. The regex fix for shorthand-hyphen regex classes is applied later, at
// compile time (compileCached).
func transformModuleCJS(src string) (string, error) {
	res := esbuild.Transform(src, esbuild.TransformOptions{
		Loader:    esbuild.LoaderJS,
		Format:    esbuild.FormatCommonJS,
		Target:    esbuild.ES2017,
		Supported: map[string]bool{"dynamic-import": false},
		LogLevel:  esbuild.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return "", fmt.Errorf("esbuild: %s", res.Errors[0].Text)
	}
	code := dynDynImportRE.ReplaceAllString(string(res.Code), dynImportGlobal+"($1)")
	return code, nil
}

// requireRE extracts the string-literal specifiers of static require() calls from
// esbuild's CommonJS output (its only require() calls are the module's static
// imports). Dynamic imports have already been rewritten to __unblinkImport, so they
// are not matched. A false positive from a require("...") inside a string literal is
// harmless: it is instantiated eagerly and only errors if actually evaluated.
var requireRE = regexp.MustCompile(`require\(\s*["']([^"']+)["']\s*\)`)

func staticRequires(cjs string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, m := range requireRE.FindAllStringSubmatch(cjs, -1) {
		if spec := m[1]; !seen[spec] {
			seen[spec] = true
			out = append(out, spec)
		}
	}
	return out
}
