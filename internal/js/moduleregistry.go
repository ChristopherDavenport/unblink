package js

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/dop251/goja"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

// The module registry gives module loading — <script type=module> entries, their
// static imports, and dynamic import() alike — browser-correct module-map semantics:
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
	// url is the module's absolute URL — the registry key, its import referrer and
	// its import.meta.url. An inline <script type=module> has no URL of its own: its
	// instance is unregistered (it can be neither imported nor deduped), url is "",
	// and the document base stands in as the referrer (moduleResolve).
	url  string
	cjs  string   // esbuild CommonJS transform of the source
	deps []string // resolved absolute URLs of static require() dependencies

	instErr  error         // fetch/transform failure for this module
	selfDone chan struct{} // closed when this module's own fetch+transform finishes (not its deps)

	// Evaluation state — touched only on the loop goroutine.
	evaluated  bool
	evaluating bool
	evalErr    error
	exports    goja.Value   // module.exports; the namespace importers receive
	module     *goja.Object // the CommonJS `module` object while/after evaluating
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
	inst.transform(b, src)
	return inst
}

// instantiateFrom registers url from source bytes the caller already holds (an
// integrity-verified module entry), so the registry runs exactly those bytes
// instead of fetching the URL again. A URL already registered is left alone.
func (r *moduleRegistry) instantiateFrom(b *bridge, url string, src []byte) {
	r.mu.Lock()
	if _, ok := r.insts[url]; ok {
		r.mu.Unlock()
		return
	}
	inst := &moduleInstance{url: url, selfDone: make(chan struct{})}
	r.insts[url] = inst
	r.mu.Unlock()
	defer close(inst.selfDone)
	inst.transform(b, src)
}

// transform turns the module's source into its CommonJS unit and resolves its
// static dependencies against inst.url, setting cjs+deps or instErr. Safe off-loop
// for a URL-keyed module (an absolute referrer never reads the document base); an
// inline module (url "") must call it on the loop goroutine.
func (inst *moduleInstance) transform(b *bridge, src []byte) {
	cjs, err := b.moduleCJS(inst.url, src)
	if err != nil {
		inst.instErr = fmt.Errorf("transform %s: %w", inst.label(), err)
		return
	}
	inst.cjs = cjs
	for _, spec := range staticRequires(cjs) {
		// Possibly off-loop and sized by the page: stop once the render is cancelled.
		if err := b.ctx.Err(); err != nil {
			inst.instErr = err
			return
		}
		if abs, err := b.moduleResolve(inst.url, spec); err == nil {
			inst.deps = append(inst.deps, abs)
		}
	}
}

// label names the module in diagnostics.
func (inst *moduleInstance) label() string {
	if inst.url == "" {
		return "inline module"
	}
	return inst.url
}

// instantiateInline builds the instance for an inline <script type=module> and
// instantiates the static graph it imports. The instance is not registered: an
// inline module has no URL, so nothing can import it and two identical inline
// scripts each run. Called on the loop goroutine (its specifiers resolve against
// the document base); it blocks while the dependency graphs load off-loop.
func (r *moduleRegistry) instantiateInline(b *bridge, src string) *moduleInstance {
	inst := &moduleInstance{}
	inst.transform(b, []byte(src))
	if inst.instErr != nil {
		return inst
	}
	var wg sync.WaitGroup
	for _, d := range inst.deps {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			r.instantiateGraph(b, d)
		}(d)
	}
	wg.Wait()
	return inst
}

// evaluate runs the registered module at url — see evaluateInst.
func (r *moduleRegistry) evaluate(b *bridge, url string) (goja.Value, error) {
	inst := r.lookup(url)
	if inst == nil {
		return nil, fmt.Errorf("module %s was not instantiated", url)
	}
	return r.evaluateInst(b, inst)
}

// evaluateInst runs a module body once, on the loop goroutine, in post-order. A
// module currently being evaluated (an import cycle) returns its in-progress
// namespace: the live module.exports, which esbuild's CommonJS output assigns
// before the module's own requires run, so the cyclic importer holds the real
// (getter-backed, still-filling) namespace rather than a stale placeholder.
func (r *moduleRegistry) evaluateInst(b *bridge, inst *moduleInstance) (goja.Value, error) {
	if inst.instErr != nil {
		return nil, inst.instErr
	}
	if inst.evaluated {
		return inst.exports, inst.evalErr
	}
	if inst.evaluating {
		return inst.module.Get("exports"), nil // cycle: hand back the partial namespace
	}
	inst.evaluating = true
	url := inst.url

	vm := b.vm
	moduleObj := vm.NewObject()
	exportsObj := vm.NewObject()
	_ = moduleObj.Set("exports", exportsObj)
	inst.module = moduleObj
	inst.exports = exportsObj

	requireFn := func(call goja.FunctionCall) goja.Value {
		spec := call.Argument(0).String()
		durl, err := b.moduleResolve(url, spec)
		if err != nil {
			panic(vm.NewTypeError("cannot resolve %q from %s: %v", spec, inst.label(), err))
		}
		ns, err := r.evaluate(b, durl)
		if err != nil {
			// A dependency that threw rethrows the same error value to every
			// importer (a browser's module record keeps its evaluation error); a
			// load failure has no JS value, so it surfaces as a TypeError.
			var ex *goja.Exception
			if errors.As(err, &ex) {
				panic(ex.Value())
			}
			panic(vm.NewTypeError("%v", err))
		}
		return ns
	}

	// A module-local __unblinkImport shadows the global loader so a nested dynamic
	// import() resolves against THIS module's URL (relative specifiers and import-map
	// scopes), not the document's. An inline module's "" is the document itself.
	importFn := func(call goja.FunctionCall) goja.Value {
		return b.importPromise(url, call.Argument(0).String())
	}

	// import.meta (bound to the wrapper's importMetaParam): url is the module's own
	// URL, or the document base for an inline module; resolve() applies the import
	// map with this module as the referrer, like a static import written here.
	meta := vm.NewObject()
	metaURL := url
	if metaURL == "" {
		if base := b.docBaseNow(); base != nil {
			metaURL = base.String()
		}
	}
	_ = meta.Set("url", metaURL)
	_ = meta.Set("resolve", func(call goja.FunctionCall) goja.Value {
		spec := call.Argument(0).String()
		abs, err := b.moduleResolve(url, spec)
		if err != nil {
			panic(vm.NewTypeError("import.meta.resolve(%q): %v", spec, err))
		}
		return vm.ToValue(abs)
	})

	fail := func(err error) (goja.Value, error) {
		inst.evaluated, inst.evalErr = true, err
		return nil, err
	}
	prog, err := compileCached("module.js", "(function(require, module, exports, "+dynImportGlobal+", "+importMetaParam+"){\n"+inst.cjs+"\n})")
	if err != nil {
		return fail(err)
	}
	fnVal, err := vm.RunProgram(prog)
	if err != nil {
		return fail(err)
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return fail(errors.New("module wrapper is not callable"))
	}
	if _, err := fn(goja.Undefined(), vm.ToValue(requireFn), moduleObj, exportsObj, vm.ToValue(importFn), meta); err != nil {
		return fail(err)
	}
	inst.exports = moduleObj.Get("exports") // esbuild reassigns module.exports
	inst.evaluated = true
	return inst.exports, nil
}

// fetchModuleSource returns a module's raw source, reusing the asset cache and any
// in-flight prefetch warm, then a guarded transport fetch. Every module — a static
// import, a <script type=module src>, a dynamic import() — is loaded here.
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
	// A data: module carries its own bytes, so nothing upstream has asked CSP
	// about it yet (a network module's entry was gated where it was declared, and
	// its dependencies inherit that decision). script-src must name `data:` — or
	// propagate trust with 'strict-dynamic' — for it to load, as in a browser.
	// The policy and the document URL are fixed before any script runs, so this
	// read is safe off the loop; the refusal is returned, not recorded here
	// (recordError is loop-only), and surfaces as the import's failure.
	if isDataURL(url) && b.csp != nil {
		if u, perr := neturl.Parse(url); perr == nil && !b.csp.allowsExternalScript(b.base, u, "", false) {
			return nil, errors.New("blocked by Content-Security-Policy: script-src does not allow a data: module")
		}
	}
	ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
	defer cancel()
	res, err := b.scriptDo(ctx, url)
	if err != nil {
		return nil, err
	}
	if res.Status >= 400 {
		return nil, fmt.Errorf("module %s: status %d", url, res.Status)
	}
	b.assets.put(assetKey(url), res.Body)
	return res.Body, nil
}

// importMetaParam is the module-wrapper parameter that carries import.meta, so each
// module sees its own import.meta.url while the transform output stays a pure
// function of the source (and so cacheable). CommonJS has no import.meta, so
// esbuild lowers every use to a module-level `const import_meta = {};` — an empty
// object, leaving import.meta.url undefined. importMetaDeclRE rebinds that one
// declaration to the parameter. (An esbuild Define of "import.meta" does the same
// job but rebuilds its whole globals table on every Transform — a third of the
// transform's allocations.) TestModuleSyntaxForms pins the generated shape.
const importMetaParam = "__unblinkImportMeta"

var importMetaDeclRE = regexp.MustCompile(`(?m)^((?:const|var|let) import_meta\d* = )\{\};$`)

// dynImportPrefixRE matches the head of esbuild's lowered dynamic import
// (Promise.resolve().then(() => __toESM(require(SPEC)))) so it can be redirected to
// the async loader, distinct from a static default import's bare
// __toESM(require(SPEC)). Only the prefix is rewritten — to `__unblinkImport((` —
// so the doubled open paren balances esbuild's trailing `))` whatever SPEC contains
// (a non-literal SPEC with nested calls defeats any attempt to match its end).
var dynImportPrefixRE = regexp.MustCompile(`Promise\.resolve\(\)\.then\(\(\)\s*=>\s*__toESM\(require\(`)

// moduleCJS returns the CommonJS unit for one module's source, via the asset cache:
// the transform is a pure function of the source bytes (plus the JSON-vs-JS choice),
// so repeat renders of a page skip esbuild entirely. A URL ending in .json is a
// JSON module.
func (b *bridge) moduleCJS(url string, src []byte) (string, error) {
	isJSON := strings.HasSuffix(strings.ToLower(url), ".json")
	sum := sha256.Sum256(src)
	kind := "js"
	if isJSON {
		kind = "json"
	}
	key := "cjs\x00" + kind + "\x00" + hex.EncodeToString(sum[:])
	if body, ok := b.assets.get(key); ok {
		return string(body), nil
	}
	var (
		cjs string
		err error
	)
	if isJSON {
		cjs, err = jsonModuleCJS(src)
	} else {
		cjs, err = transformModuleCJS(string(src))
	}
	if err != nil {
		return "", err
	}
	b.assets.put(key, []byte(cjs))
	return cjs, nil
}

// jsonModuleCJS wraps a JSON document as a CommonJS module whose exports are the
// parsed value (so `import data from "./x.json"` yields it as the default). The
// text is parsed at run time by JSON.parse from a string literal, never spliced in
// as source, so a hostile "JSON" body cannot smuggle code into the module wrapper.
func jsonModuleCJS(src []byte) (string, error) {
	if !json.Valid(src) {
		return "", errors.New("invalid JSON module")
	}
	lit, err := json.Marshal(string(src))
	if err != nil {
		return "", err
	}
	// json.Marshal escapes U+2028/U+2029 and '<', so the literal is valid JS source.
	return "module.exports = JSON.parse(" + string(lit) + ");", nil
}

// transformModuleCJS turns one ES module's source into a self-contained CommonJS
// unit goja can run: static imports become require() calls (resolved against the
// module registry), exports become module.exports, import.meta is bound to the
// wrapper's importMetaParam, and a dynamic import() becomes an __unblinkImport()
// call (the async registry loader) rather than a synchronous require — so a lazy
// chunk is fetched when the page calls import(), never at load. The regex fix for
// shorthand-hyphen regex classes is applied later, at compile time (compileCached).
//
// This is esbuild.Transform, which has no filesystem access; do not replace it with
// esbuild.Build (see runModules).
//
// The unit runs inside a wrapper whose parameters are named require, module and
// exports, and the transform's own output uses those names for the module
// plumbing. The module's *source* must not see them: in a browser a free
// `require`, `module` or `exports` in an ES module is the page's global of that
// name, usually undefined. Left shadowed, `typeof require` reads "function" and
// `typeof module` "object" in every module — a bundler runtime then takes its
// CommonJS branch (rolldown's __require returned the wrapper's function, so
// Discourse read `require.entries` off it and got undefined where the page's AMD
// loader keeps its registry), and a UMD prologue assigns to module.exports and
// clobbers the module's real exports. esbuild's Define rewrites exactly the free
// source references (not its own generated ones, and not a local binding of the
// same name) to the global. Define rebuilds esbuild's globals table per call, so
// it is only passed for names the source mentions (freeWordIn) — most modules
// mention none.
func transformModuleCJS(src string) (string, error) {
	var define map[string]string
	for _, name := range cjsWrapperNames {
		if freeWordIn(src, name) {
			if define == nil {
				define = make(map[string]string, len(cjsWrapperNames))
			}
			define[name] = "globalThis." + name
		}
	}
	// A module script is an ES module whatever its text looks like. esbuild decides
	// per file, and one with no import/export syntax it reads as CommonJS: top-level
	// `this` becomes `exports` (it is undefined in a module) and `module`/`exports`
	// bind to the wrapper's, which Define cannot then reach. An empty export list
	// settles it without changing what the module exports or its line numbers.
	res := esbuild.Transform(src+"\nexport {};", esbuild.TransformOptions{
		Loader:    esbuild.LoaderJS,
		Format:    esbuild.FormatCommonJS,
		Target:    esbuild.ES2017,
		Supported: map[string]bool{"dynamic-import": false},
		Define:    define,
		LogLevel:  esbuild.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return "", fmt.Errorf("esbuild: %s", res.Errors[0].Text)
	}
	code := dynImportPrefixRE.ReplaceAllLiteralString(string(res.Code), "Promise.resolve().then(() => "+dynImportGlobal+"((")
	code = importMetaDeclRE.ReplaceAllString(code, "${1}"+importMetaParam+";")
	return code, nil
}

// cjsWrapperNames are the module wrapper's parameters a module's source must see
// as page globals (see transformModuleCJS).
var cjsWrapperNames = [...]string{"require", "module", "exports"}

// freeWordIn reports whether src may contain word as a free identifier: an
// occurrence on word boundaries that is not a property access (`.word`) nor an
// object-literal key (`word:`). It is a pre-filter for an exact rewrite, so a
// false positive (a string, a comment) only costs the Define; it never has a
// false negative for a real free reference.
func freeWordIn(src, word string) bool {
	isIdent := func(c byte) bool {
		return c == '_' || c == '$' || c >= 0x80 || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
	}
	for from := 0; ; {
		i := strings.Index(src[from:], word)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(word)
		from = end
		if i > 0 && (isIdent(src[i-1]) || src[i-1] == '.') {
			// `a.b` is a property access — unless the dot is the tail of a spread
			// or rest (`...require`), which names the identifier itself.
			if src[i-1] != '.' || i < 3 || src[i-3:i] != "..." {
				continue
			}
		}
		if end < len(src) && isIdent(src[end]) {
			continue
		}
		j := end
		for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
			j++
		}
		if j < len(src) && src[j] == ':' {
			// `word:` is an object key or a label — but `cond ? word : x` is a use.
			k := i - 1
			for k >= 0 && (src[k] == ' ' || src[k] == '\t' || src[k] == '\n' || src[k] == '\r') {
				k--
			}
			if k < 0 || src[k] != '?' {
				continue
			}
		}
		return true
	}
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
