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
)

// collectModuleScripts returns <script type="module"> elements (inline + src) in
// document order.
func collectModuleScripts(doc *html.Node) []*html.Node {
	return selModuleScript.MatchAll(doc)
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
			entry = "import " + strconv.Quote(abs) + ";"
		} else {
			entry = scriptText(m)
		}
		if strings.TrimSpace(entry) == "" {
			continue
		}

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
		prog, err := compileCached("module.js", string(res.OutputFiles[0].Contents))
		if err != nil {
			b.recordError(err)
			continue
		}
		if _, err := b.vm.RunProgram(prog); err != nil {
			b.recordError(err)
		}
	}
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
// Promise.resolve().then(() => __toESM(require(SPEC))). We then redirect that exact,
// stable esbuild-generated substring to the __unblinkImportSync loader (installDynamicImport)
// so the chunk actually fetches+bundles+runs and (await import(spec)).default resolves
// to the chunk's real default. The rename is arity-preserving (require -> loader), leaves
// the disabled global require untouched (the require()-off security control holds), and
// only ever matches esbuild's dynamic-import lowering — a bare CommonJS require() call is
// never printed as `__toESM(require(`, nor is a `require(` inside a string literal. If the
// loader is absent/errors (e.g. no transport), it throws and the import rejects gracefully
// (Phase A behavior). TestLowerDynamicImport* pins the generated shape so a goja/esbuild
// bump that changes it fails loudly.
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
	code := strings.ReplaceAll(string(res.Code), "__toESM(require(", "__toESM("+dynImportGlobal+"(")
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
				ctx, cancel := context.WithTimeout(b.ctx, b.reqTimeout)
				defer cancel()
				res, err := b.transport.Do(ctx, "GET", a.Path, nil, nil)
				if err != nil {
					return esbuild.OnLoadResult{}, err
				}
				if res.Status >= 400 {
					return esbuild.OnLoadResult{}, fmt.Errorf("module %s: status %d", a.Path, res.Status)
				}
				contents := string(res.Body)
				loader := esbuild.LoaderJS
				if strings.HasSuffix(strings.ToLower(a.Path), ".json") {
					loader = esbuild.LoaderJSON
				}
				return esbuild.OnLoadResult{Contents: &contents, Loader: loader}, nil
			})
		},
	}
}

// moduleResolve joins a specifier against the importing module's URL (or the page
// base URL for the entry point).
func (b *bridge) moduleResolve(importer, spec string) (string, error) {
	var base *url.URL
	if strings.HasPrefix(importer, "http://") || strings.HasPrefix(importer, "https://") {
		base, _ = url.Parse(importer)
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
	case strings.HasPrefix(spec, "http://"), strings.HasPrefix(spec, "https://"):
		return false
	default:
		return true
	}
}
