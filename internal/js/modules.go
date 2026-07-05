package js

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	esbuild "github.com/evanw/esbuild/pkg/api"
	"golang.org/x/net/html"
)

const moduleNamespace = "unblink-url"

var (
	selModuleScript = cascadia.MustCompile("script[type=module]")
	selImportMap    = cascadia.MustCompile("script[type=importmap]")
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

// parseImportMap reads the first <script type="importmap"> and returns its
// "imports" mapping (bare specifier -> URL). Scopes are not supported.
func parseImportMap(doc *html.Node) map[string]string {
	n := selImportMap.MatchFirst(doc)
	if n == nil {
		return nil
	}
	var im struct {
		Imports map[string]string `json:"imports"`
	}
	if err := json.Unmarshal([]byte(scriptText(n)), &im); err != nil {
		return nil
	}
	return im.Imports
}

// runModules bundles and runs each <script type="module"> via esbuild — the
// module graph is fetched through the guarded transport, bundled to a classic
// IIFE, and run on the existing goja path. goja has no native ESM, so bundling is
// the pure-Go route. A build failure (incl. a blocked/failed module fetch) skips
// that module script; it is never fatal. Requires a transport (network).
func (b *bridge) runModules(modules []*html.Node, importMap map[string]string) {
	if b.transport == nil {
		return
	}
	plugin := b.modulePlugin(importMap)
	for _, m := range modules {
		var entry string
		if src := strings.TrimSpace(getAttr(m, "src")); src != "" {
			abs, err := b.resolveURL(src)
			if err != nil {
				continue
			}
			// SRI is enforced only on the top-level module tag (browser parity —
			// transitive static imports are not integrity-checked).
			if b.sriEnabled {
				if integrity := getAttr(m, "integrity"); integrity != "" && !b.verifyModuleIntegrity(abs, integrity) {
					continue
				}
			}
			entry = "import " + strconv.Quote(abs) + ";"
		} else {
			entry = scriptText(m)
		}
		if strings.TrimSpace(entry) == "" {
			continue
		}

		// The bundle output is a pure function of (base, entry, import map) —
		// its module fetches are asset requests — so it shares the asset cache's
		// TTL/flag, skipping the whole esbuild build on repeat renders.
		bkey := bundleKey(b.docBaseNow(), entry, importMap)
		bundled, hit := b.assets.get(bkey)
		if !hit {
			res := esbuild.Build(esbuild.BuildOptions{
				Stdin: &esbuild.StdinOptions{
					Contents:   entry,
					Loader:     esbuild.LoaderJS,
					Sourcefile: "entry.js",
					ResolveDir: "/",
				},
				Bundle:   true,
				Write:    false,
				Format:   esbuild.FormatIIFE,
				Target:   esbuild.ES2017,
				LogLevel: esbuild.LogLevelSilent,
				Plugins:  []esbuild.Plugin{plugin},
			})
			if len(res.Errors) > 0 || len(res.OutputFiles) == 0 {
				if len(res.Errors) > 0 {
					b.recordError(fmt.Errorf("esbuild: %s", res.Errors[0].Text))
				}
				continue
			}
			bundled = res.OutputFiles[0].Contents
			b.assets.put(bkey, bundled)
		}
		prog, err := compileCached("module.js", string(bundled))
		if err != nil {
			b.recordError(err)
			continue
		}
		if _, err := b.vm.RunProgram(prog); err != nil {
			b.recordError(err)
		}
	}
}

// verifyModuleIntegrity fetches the entry module and checks its integrity against
// the raw bytes, priming the asset cache with the fetched body so the esbuild
// loader (modulePlugin OnLoad) reuses it instead of re-fetching. It returns false
// (and records a diagnostic) only on a real hash mismatch, so the caller skips the
// module — a browser refusing a tampered module. A fetch failure returns true and
// lets the normal loader path surface the error, rather than blocking on SRI.
func (b *bridge) verifyModuleIntegrity(abs, integrity string) bool {
	ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
	res, err := b.transport.Do(ctx, "GET", abs, nil, nil)
	cancel()
	if err != nil || res == nil || res.Status >= 400 {
		return true
	}
	if ok, enforced := verifySRI(integrity, sriBytes(res)); enforced && !ok {
		b.recordError(fmt.Errorf("subresource integrity mismatch, module not executed: %s", abs))
		return false
	}
	b.assets.put(assetKey(abs), res.Body)
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
	res := esbuild.Transform(src, esbuild.TransformOptions{
		Loader:    esbuild.LoaderJS,
		Target:    esbuild.ESNext,
		Supported: map[string]bool{"dynamic-import": false},
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

// modulePlugin resolves every specifier to an absolute URL in a custom namespace
// and loads its bytes through the guarded transport, so esbuild never touches a
// filesystem and all module fetches obey the SSRF guard + budget.
func (b *bridge) modulePlugin(importMap map[string]string) esbuild.Plugin {
	resolve := func(a esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
		spec := a.Path
		if isBareSpecifier(spec) {
			mapped, ok := importMap[spec]
			if !ok {
				return esbuild.OnResolveResult{}, fmt.Errorf("unresolved bare specifier %q", spec)
			}
			spec = mapped
		}
		abs, err := b.moduleResolve(a.Importer, spec)
		if err != nil {
			return esbuild.OnResolveResult{}, err
		}
		return esbuild.OnResolveResult{Path: abs, Namespace: moduleNamespace}, nil
	}

	return esbuild.Plugin{
		Name: "unblink-net",
		Setup: func(pb esbuild.PluginBuild) {
			// Entry-point imports (file namespace) and imports within already
			// downloaded modules (moduleNamespace) both go through the same resolver.
			pb.OnResolve(esbuild.OnResolveOptions{Filter: ".*"}, resolve)
			pb.OnResolve(esbuild.OnResolveOptions{Filter: ".*", Namespace: moduleNamespace}, resolve)

			pb.OnLoad(esbuild.OnLoadOptions{Filter: ".*", Namespace: moduleNamespace}, func(a esbuild.OnLoadArgs) (esbuild.OnLoadResult, error) {
				loader := esbuild.LoaderJS
				if strings.HasSuffix(strings.ToLower(a.Path), ".json") {
					loader = esbuild.LoaderJSON
				}
				if body, ok := b.assets.get(assetKey(a.Path)); ok {
					contents := string(body)
					return esbuild.OnLoadResult{Contents: &contents, Loader: loader}, nil
				}
				// A modulepreload/prefetch warm may be in flight for this chunk; join
				// it (overlapping this build's wait with the other warmers) rather than
				// issuing a duplicate fetch. On a warm that failed, fall through to a
				// fresh fetch — a missing module is more consequential than a skipped
				// classic script, so we retry rather than skip.
				if body, ok, found := b.prefetched(a.Path); found && ok {
					contents := string(body)
					return esbuild.OnLoadResult{Contents: &contents, Loader: loader}, nil
				}
				ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
				defer cancel()
				res, err := b.transport.Do(ctx, "GET", a.Path, nil, nil)
				if err != nil {
					return esbuild.OnLoadResult{}, err
				}
				if res.Status >= 400 {
					return esbuild.OnLoadResult{}, fmt.Errorf("module %s: status %d", a.Path, res.Status)
				}
				contents := string(res.Body)
				b.assets.put(assetKey(a.Path), res.Body)
				return esbuild.OnLoadResult{Contents: &contents, Loader: loader}, nil
			})
		},
	}
}

// moduleResolve joins a specifier against the importing module's URL (or the page
// base URL for the entry point).
func (b *bridge) moduleResolve(importer, spec string) (string, error) {
	// An absolute specifier (http/https, or chrome-extension:// for a background
	// module) resolves to itself — no base needed. Besides being correct, this keeps
	// off-loop dynamic-import bundles (bundleDynamicChunk, whose entry imports an
	// absolute URL) from reading docBaseNow off the loop goroutine, which would race
	// with on-loop navigation updates.
	if u, err := url.Parse(spec); err == nil && u.IsAbs() {
		return u.String(), nil
	}
	// Relative imports resolve against the importer when it is an absolute URL (so a
	// module's own path, not the document base, anchors its siblings); otherwise the
	// document/extension base.
	var base *url.URL
	if iu, err := url.Parse(importer); err == nil && iu.IsAbs() {
		base = iu
	} else {
		base = b.docBaseNow()
	}
	u, err := url.Parse(spec)
	if err != nil {
		return "", err
	}
	if base == nil {
		if u.IsAbs() {
			return u.String(), nil
		}
		return "", fmt.Errorf("cannot resolve %q without a base url", spec)
	}
	return base.ResolveReference(u).String(), nil
}

// isBareSpecifier reports whether spec is a bare module specifier (needs an import
// map), i.e. not relative, root-relative, or an absolute http(s) URL.
func isBareSpecifier(spec string) bool {
	switch {
	case spec == "":
		return false
	case strings.HasPrefix(spec, "./"), strings.HasPrefix(spec, "../"), strings.HasPrefix(spec, "/"):
		return false
	case strings.Contains(spec, "://"):
		// Any absolute-URL specifier (http/https, and chrome-extension:// for an
		// extension background module) is not bare.
		return false
	default:
		return true
	}
}
