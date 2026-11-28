package js

import (
	"context"
	"fmt"
	"strings"

	"github.com/andybalholm/cascadia"
	esbuild "github.com/evanw/esbuild/pkg/api"
	urlpkg "github.com/nationallibraryofnorway/whatwg-url/url"
	"golang.org/x/net/html"
)

var (
	selModuleScript = cascadia.MustCompile("script[type=module]")
	selAnyScript    = cascadia.MustCompile("script")
	selPreload      = cascadia.MustCompile("link[rel~=modulepreload], link[rel~=preload]")
)

// collectModuleScripts returns <script type="module"> elements (inline + src) in
// document order.
func collectModuleScripts(doc *html.Node) []*html.Node {
	return selModuleScript.MatchAll(doc)
}

// collectPreloadHrefs returns the absolute URLs of the page's <link rel=modulepreload>
// (and script/module/fetch <link rel=preload>) hints — the code-split chunks a
// framework declares up front. These are exactly the chunks the runtime import()
// path would otherwise fetch serially; warming them concurrently (startModulePreload)
// turns that path into cache hits. Deduped; unresolvable hrefs are skipped.
func (b *bridge) collectPreloadHrefs(doc *html.Node) []string {
	var urls []string
	seen := make(map[string]bool)
	for _, l := range selPreload.MatchAll(doc) {
		rel := strings.ToLower(getAttr(l, "rel"))
		if !relHasToken(rel, "modulepreload") {
			// A plain rel=preload covers fonts/images/styles too; only script-like
			// destinations are runnable modules worth warming.
			switch strings.ToLower(strings.TrimSpace(getAttr(l, "as"))) {
			case "script", "module", "fetch":
			default:
				continue
			}
		}
		href := strings.TrimSpace(getAttr(l, "href"))
		if href == "" {
			continue
		}
		abs, err := b.resolveURL(href)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		urls = append(urls, abs)
	}
	return urls
}

// startModulePreload warms the page's modulepreload/preload chunk graph concurrently
// into the asset cache before the (serial) runtime import() path runs. Engine
// plumbing like startPrefetch — see warmConcurrent for the settle-safety rationale.
func (b *bridge) startModulePreload(doc *html.Node) {
	if b.transport == nil {
		return
	}
	b.warmConcurrent(b.collectPreloadHrefs(doc))
}

// relHasToken reports whether the space-separated rel attribute contains tok.
func relHasToken(rel, tok string) bool {
	for _, f := range strings.Fields(rel) {
		if f == tok {
			return true
		}
	}
	return false
}

// runModules loads and runs each <script type="module"> through the per-render
// module registry (moduleregistry.go, ADR 0018): every module is fetched through
// the guarded transport, esbuild-*transformed* (never bundled) to a CommonJS unit,
// and evaluated once on the existing goja path, its instance shared with every
// other importer — static or dynamic — in this render. goja has no native ESM, so
// the per-module transform is the pure-Go route.
//
// esbuild.Build is deliberately NOT used for page modules. Its bundler expands a
// non-literal relative import (import("./"+x), import(`./${x}.json`)) as a glob
// against the real filesystem, bypassing any plugin resolver — untrusted page JS
// could read files under the server's working directory (TestModuleGlobImport*).
// esbuild.Transform has no filesystem access by construction.
//
// Runs on the loop goroutine and blocks it for the instantiate phase (the fetches
// themselves run concurrently off-loop), exactly as the bundle build used to, so
// module scripts still execute in document order before DOMContentLoaded and add
// no async primitive (ADR 0004). A module whose graph fails to load or evaluate is
// recorded and skipped; later module scripts still run. Requires a transport.
func (b *bridge) runModules(modules []*html.Node) {
	if b.transport == nil {
		return
	}
	b.acquireImportMap(b.doc) // module loading begins: the import map is now fixed
	for _, m := range modules {
		if src := strings.TrimSpace(getAttr(m, "src")); src != "" {
			abs, err := b.resolveURL(src)
			if err != nil {
				continue
			}
			// CSP script-src gates the top-level module tag (parser-inserted).
			// Transitive imports inherit this decision (documented approximation).
			if !b.cspAllowExternalScript(abs, b.nodeNonce(m), true) {
				continue
			}
			// SRI is enforced only on the top-level module tag (browser parity —
			// transitive static imports are not integrity-checked).
			if b.sriEnabled {
				if integrity := getAttr(m, "integrity"); integrity != "" && !b.verifyModuleIntegrity(abs, integrity) {
					continue
				}
			}
			b.reg.instantiateGraph(b, abs)
			if _, err := b.reg.evaluate(b, abs); err != nil {
				b.recordError(fmt.Errorf("module %s: %w", abs, err))
			}
			continue
		}
		entry := scriptText(m)
		// CSP script-src gates inline modules (nonce / hash / 'unsafe-inline').
		if !b.cspAllowInline(entry, b.nodeNonce(m)) {
			continue
		}
		if strings.TrimSpace(entry) == "" {
			continue
		}
		inst := b.reg.instantiateInline(b, entry)
		if _, err := b.reg.evaluateInst(b, inst); err != nil {
			b.recordError(fmt.Errorf("inline module: %w", err))
		}
	}
}

// verifyModuleIntegrity fetches the entry module, checks its integrity against the
// raw bytes, and registers the module from those same bytes (instantiateFrom), so
// what was verified is what runs — the loader never fetches the entry a second
// time, which would let an origin serve the pinned bytes once and others after. It
// returns false (and records a diagnostic) only on a real hash mismatch, so the
// caller skips the module — a browser refusing a tampered module. A fetch failure
// returns true and lets the normal loader path surface the error, rather than
// blocking on SRI. A URL already in the module map keeps its instance (a browser's
// module map is keyed by URL alone).
func (b *bridge) verifyModuleIntegrity(abs, integrity string) bool {
	if b.reg.lookup(abs) != nil {
		return true
	}
	ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
	res, err := b.scriptDo(ctx, abs)
	cancel()
	if err != nil || res == nil || res.Status >= 400 {
		return true
	}
	if ok, enforced := verifySRI(integrity, sriBytes(res)); enforced && !ok {
		b.recordError(fmt.Errorf("subresource integrity mismatch, module not executed: %s", abs))
		return false
	}
	b.assets.put(assetKey(abs), res.Body)
	b.reg.instantiateFrom(b, abs, res.Body)
	return true
}

// lowerDynamicImport re-emits src with dynamic import() lowered to a require()-based
// form goja can parse. It is taken ONLY as a fallback when goja.Compile fails on a
// classic script — goja has no ImportCall node, so a literal import() in a classic
// bundle (Vite/Rollup-style code splitting) is a hard parse error that would otherwise
// skip the whole bundle and leave an SPA unmounted.
//
// Target ESNext + Supported{dynamic-import:false} makes esbuild lower ONLY dynamic
// import and leave private fields / logical assignment / optional chaining native, so
// nothing is routed through goja's buggy WeakMap (the reason blanket downleveling was
// rejected — see crypto_test.go). esbuild lowers both import('x') and import(expr) to
// Promise.resolve().then(() => __toESM(require(SPEC))). We rewrite the exact, stable
// `__toESM(require(` substring to `__unblinkImport((` (installDynamicImport): the doubled
// open paren balances esbuild's trailing `))` so SPEC stays a parenthesized expression,
// while dropping the __toESM wrapper — the loader now returns a Promise for the chunk
// namespace, which the surrounding `.then` chains on, so sibling import()s fetch
// concurrently instead of serializing. The disabled global require is left untouched
// (the require()-off security control holds), and the substring only ever matches
// esbuild's dynamic-import lowering — a bare CommonJS require() call is never printed as
// `__toESM(require(`, nor is a `require(` inside a string literal. If the loader is
// absent/errors (e.g. no transport), the Promise rejects and the import degrades
// gracefully. TestLowerDynamicImport* pins the generated shape so a goja/esbuild bump
// that changes it fails loudly.
//
// The default Format (Preserve/passthrough) is MANDATORY: Format IIFE would enable
// tree-shaking (dropping side-effecting bundle code) and rewrite a CommonJS bundle's
// top-level `this` to `exports`, breaking `this === window`. Do not pin a Format.
func lowerDynamicImport(src string) (string, bool) {
	return lowerClassic(src, map[string]bool{"dynamic-import": false})
}

// lowerAsyncIteration is lowerDynamicImport plus the two async-iteration forms
// goja cannot parse: async generator functions (`async function*`, `async *m()`)
// and `for await`. It is the second fallback, taken only when the script still
// does not compile: a bundle that merely *contains* an async generator (a
// streaming API client nobody calls on this page) is otherwise a hard parse
// error that skips the whole file — the vendor bundle on webpack.js.org, a
// chunk on bbc.co.uk and arstechnica.com. esbuild lowers both to its
// __asyncGenerator/__forAwait helpers, which are plain Promise and Symbol code:
// nothing is routed through WeakMap, so the reason for not downlevelling
// wholesale (see lowerDynamicImport) does not apply.
func lowerAsyncIteration(src string) (string, bool) {
	return lowerClassic(src, map[string]bool{"dynamic-import": false, "async-generator": false, "for-await": false})
}

func lowerClassic(src string, supported map[string]bool) (string, bool) {
	res := esbuild.Transform(src, esbuild.TransformOptions{
		Loader:    esbuild.LoaderJS,
		Target:    esbuild.ESNext,
		Supported: supported,
		LogLevel:  esbuild.LogLevelSilent,
	})
	if len(res.Errors) > 0 || len(res.Code) == 0 {
		return "", false
	}
	// esbuild prints the lowering as `__toESM(require(ARG))`. Replace the
	// `__toESM(require(` prefix with `__unblinkImport((` — the doubled open paren
	// balances the existing `))` (so ARG stays a parenthesized expression) while
	// dropping esbuild's __toESM wrapper, which is incompatible with the loader now
	// returning a Promise (the surrounding `.then` chains on it instead).
	code := strings.ReplaceAll(string(res.Code), "__toESM(require(", dynImportGlobal+"((")
	return code, true
}

// moduleResolve resolves a module specifier written in the module at importer (an
// absolute URL), or in the document itself when importer is not one (an inline
// module, whose instance URL is "", or a classic script): the import map's matching scopes, then
// its top-level imports, then the specifier as a URL relative to the referrer.
//
// An absolute importer never reads docBaseNow, so the off-loop registry instantiate
// (always called with a module URL) cannot race on-loop navigation updates.
func (b *bridge) moduleResolve(importer, spec string) (string, error) {
	base, err := urlpkg.Parse(importer)
	if err != nil {
		base = whatwgURL(b.docBaseNow())
	}
	return b.importMap.resolve(b.ctx, spec, base)
}
