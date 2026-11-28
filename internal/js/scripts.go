package js

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/dop251/goja"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var selScript = cascadia.MustCompile("script")

// collectScripts returns the classic scripts (inline and external src) in document
// order. ES modules and non-JS types are skipped, as are `nomodule` scripts: the
// engine executes type=module scripts, so on a differential-loading build
// (paired -es2015/-es5 bundles) running the nomodule half too would boot the app
// twice — a modern browser runs only the module set.
func collectScripts(doc *html.Node) []*html.Node {
	var out []*html.Node
	for _, s := range selScript.MatchAll(doc) {
		if hasAttr(s, "nomodule") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(getAttr(s, "type"))) {
		case "", "text/javascript", "application/javascript":
			out = append(out, s)
		default:
			// module, importmap, application/json, etc. — skip
		}
	}
	return out
}

// stripSourceMappingURL removes `//# sourceMappingURL=...` (and `//@ ...`) comment
// lines so goja's parser doesn't try to load a missing .map file from disk and fail
// the whole compile — which would silently disable a bundle (e.g. preact.umd.js).
func stripSourceMappingURL(src string) string {
	const marker = "sourceMappingURL="
	if !strings.Contains(src, marker) {
		return src
	}
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if (strings.HasPrefix(t, "//#") || strings.HasPrefix(t, "//@")) && strings.Contains(t, marker) {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// loadInsertedScript fetches and runs a <script src> that was dynamically inserted into
// the document (createElement('script') + appendChild — the webpack "JSONP" chunk
// mechanism), then fires its load/error event so the loader (e.g. __webpack_require__.e)
// resolves. It is called from connect, so only a directly-inserted node is considered; a
// script nested inside inserted markup (e.g. innerHTML) is not executed, matching browser
// semantics. Only external classic scripts load — inline inserted scripts never run.
func (b *bridge) loadInsertedScript(n *html.Node) {
	if b.transport == nil || n == nil || n.Type != html.ElementNode || n.DataAtom != atom.Script {
		return
	}
	if b.loadedScripts[n] {
		return
	}
	src := b.scriptSrc(n)
	if src == "" || !isClassicScriptType(n) || !b.isConnected(n) {
		return
	}
	b.loadedScripts[n] = true
	// CSP script-src gates the load. Script-inserted (createElement+appendChild), so
	// strict-dynamic — the common modern-CSP case — lets a trusted-inserted chunk
	// through. A block fires the error event instead of loading.
	if abs, err := b.resolveURL(src); err == nil && !b.cspAllowExternalScript(abs, b.nodeNonce(n), false) {
		b.fireScriptEvent(n, "error")
		return
	}
	b.loadExternalScript(n, src)
}

// scriptSrc returns a script element's effective src. The src property reflects the
// attribute, so the two normally agree; the property is still read first because a
// page (or a library patching HTMLScriptElement) can shadow it on the wrapper.
func (b *bridge) scriptSrc(n *html.Node) string {
	if obj := b.wrap(n).ToObject(b.vm); obj != nil {
		if v := obj.Get("src"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(getAttr(n, "src"))
}

// isClassicScriptType reports whether n's type marks it a classic script (the set
// runScripts runs). Module / JSON / importmap scripts are excluded.
func isClassicScriptType(n *html.Node) bool {
	switch strings.ToLower(strings.TrimSpace(getAttr(n, "type"))) {
	case "", "text/javascript", "application/javascript":
		return true
	default:
		return false
	}
}

// loadExternalScript fetches src off-loop, then (on-loop) runs it and fires load/error on
// the element. The pending/keepalive bracket keeps the settle open until the chunk — and
// any promise its load handler resolves — has run, mirroring fetchPromise. load/error are
// fired on-loop (not during the insert) so they don't re-enter mid-mutation.
func (b *bridge) loadExternalScript(n *html.Node, src string) {
	b.pending.Add(1)
	keep := b.acquireKeepalive()
	integrity := ""
	if b.sriEnabled {
		integrity = getAttr(n, "integrity")
	}
	go func() {
		var (
			body    []byte
			ok      bool
			name    = src
			sriFail string // set to the URL when SRI blocked the chunk (recorded on-loop)
		)
		if abs, err := b.resolveURL(src); err == nil {
			name = abs
			// SRI-pinned chunks bypass the asset cache (it holds only the transcoded
			// body, not the raw bytes SRI must hash).
			if cached, hit := b.assets.get(assetKey(abs)); hit && integrity == "" {
				body, ok = cached, true
			} else {
				ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
				res, ferr := b.scriptDo(ctx, abs)
				cancel()
				if ferr == nil && res != nil && res.Status < 400 {
					if integrity != "" {
						if v, enforced := verifySRI(integrity, sriBytes(res)); enforced && !v {
							sriFail = abs // block: leave ok=false so the error event fires below
						} else {
							body, ok = res.Body, true
							b.assets.put(assetKey(abs), res.Body)
						}
					} else {
						body, ok = res.Body, true
						b.assets.put(assetKey(abs), res.Body)
					}
				}
			}
		}
		_ = b.loop.RunOnLoop(func(vm *goja.Runtime) {
			if sriFail != "" {
				b.recordError(fmt.Errorf("subresource integrity mismatch, script not executed: %s", sriFail))
			}
			if ok {
				b.currentScript = n
				b.compileAndRun("chunk:"+name, string(body))
				b.currentScript = nil
				b.fireScriptEvent(n, "load")
			} else {
				b.fireScriptEvent(n, "error")
			}
			b.releaseKeepalive(keep)
			b.pending.Add(-1)
		})
	}()
}

// fireScriptEvent dispatches a non-bubbling load/error event at the script
// element. Its on<type> handler — webpack sets script.onload/onerror directly —
// runs as part of the dispatch (eventhandlers.go).
func (b *bridge) fireScriptEvent(n *html.Node, typ string) {
	b.dispatchOnNode(n, b.newEvent(typ, false, false, b.wrap(n)))
}

// scriptText returns the concatenated text content of a <script> node.
func scriptText(n *html.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return sb.String()
}

// runDocumentScripts runs the document's parser-inserted scripts in the order a
// browser does:
//
//  1. every classic script that is not deferred, in document order, as the
//     parser reaches it;
//  2. the document is then fully parsed: readyState becomes "interactive";
//  3. the scripts that wait for the end of parsing — external classic scripts
//     with `defer`, and module scripts, which are deferred by definition — in
//     document order, interleaved.
//
// DOMContentLoaded follows (fireLifecycle). `defer` on an inline classic script
// has no effect, as specified. `async` scripts run where the document names them
// (a browser runs them whenever they arrive, so any order is a valid one).
//
// The order is load-bearing for the common pattern of a deferred library in
// <head> configured by an inline script in <body>: Alpine is loaded with defer
// and starts as soon as it runs, and the page registers its components in a
// later inline script listening for alpine:init. Run in plain document order
// the library started first and every component was "not defined".
func (b *bridge) runDocumentScripts(scripts, modules []*html.Node) {
	b.scriptOrdinal = make(map[*html.Node]int, len(scripts))
	var immediate, deferred []*html.Node
	for i, s := range scripts {
		b.scriptOrdinal[s] = i
		if hasAttr(s, "defer") && !hasAttr(s, "async") && strings.TrimSpace(getAttr(s, "src")) != "" {
			deferred = append(deferred, s)
		} else {
			immediate = append(immediate, s)
		}
	}
	b.runScripts(immediate)
	b.setReadyState("interactive") // parsing is done; deferred scripts, modules and DOMContentLoaded follow

	if len(deferred) == 0 {
		if len(modules) > 0 {
			b.runModules(modules)
		}
		return
	}
	if len(modules) == 0 {
		b.runScripts(deferred)
		return
	}
	// Both kinds wait for the end of parsing and then run in document order:
	// merge the two lists by position and run each maximal run of one kind.
	pos := make(map[*html.Node]int)
	for i, s := range selScript.MatchAll(b.doc) {
		pos[s] = i
	}
	isModule := make(map[*html.Node]bool, len(modules))
	for _, m := range modules {
		isModule[m] = true
	}
	all := append(append([]*html.Node{}, deferred...), modules...)
	sort.SliceStable(all, func(i, j int) bool { return pos[all[i]] < pos[all[j]] })
	for i := 0; i < len(all); {
		j := i
		for j < len(all) && isModule[all[j]] == isModule[all[i]] {
			j++
		}
		if isModule[all[i]] {
			b.runModules(all[i:j])
		} else {
			b.runScripts(all[i:j])
		}
		i = j
	}
}

// runScripts compiles and runs each script in document order. External (src)
// script bodies come from the asset cache, the concurrent prefetch
// (startPrefetch), or a synchronous Transport fetch, in that order — execution
// itself always blocks in document order, as classic scripts must. A syntax or
// runtime error skips that script so one bad script doesn't blank the page.
func (b *bridge) runScripts(scripts []*html.Node) {
	for i, s := range scripts {
		var src string
		if n, ok := b.scriptOrdinal[s]; ok {
			i = n // the script's position among the document's classic scripts
		}
		name := fmt.Sprintf("script-%d.js", i) // inline fallback; externals use their URL
		if srcAttr := strings.TrimSpace(getAttr(s, "src")); srcAttr != "" {
			if b.transport == nil {
				continue // networking disabled: external scripts can't load
			}
			abs, err := b.resolveURL(srcAttr)
			if err != nil {
				continue
			}
			name = abs
			// CSP script-src gates the load before any fetch (a browser blocks it).
			// These are parser-inserted (present in the initial HTML).
			if !b.cspAllowExternalScript(abs, b.nodeNonce(s), true) {
				continue
			}
			integrity := ""
			if b.sriEnabled {
				integrity = getAttr(s, "integrity")
			}
			if integrity != "" {
				// SRI-pinned: bypass the asset cache/prefetch (they hold only the
				// charset-transcoded body, not the raw bytes SRI must hash) and block
				// on mismatch, like a browser refusing a tampered script.
				ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
				res, ferr := b.scriptDo(ctx, abs)
				cancel()
				if ferr != nil || res == nil || res.Status >= 400 {
					continue
				}
				if ok, enforced := verifySRI(integrity, sriBytes(res)); enforced && !ok {
					b.recordError(fmt.Errorf("subresource integrity mismatch, script not executed: %s", abs))
					continue
				}
				src = string(res.Body)
				b.assets.put(assetKey(abs), res.Body)
			} else if body, ok := b.assets.get(assetKey(abs)); ok {
				src = string(body)
			} else if body, ok, found := b.prefetched(abs); found {
				if !ok {
					continue // fetch failed; same skip as the synchronous path
				}
				src = string(body)
			} else {
				ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
				res, ferr := b.scriptDo(ctx, abs)
				cancel()
				if ferr != nil || res == nil || res.Status >= 400 {
					continue
				}
				src = string(res.Body)
				b.assets.put(assetKey(abs), res.Body)
			}
		} else {
			src = scriptText(s)
			// CSP script-src gates inline scripts (nonce / hash of the source text /
			// 'unsafe-inline'). Hash is over the original text, before compileAndRun's
			// stripSourceMappingURL.
			if !b.cspAllowInline(src, b.nodeNonce(s)) {
				continue
			}
		}
		if strings.TrimSpace(src) == "" {
			continue
		}
		b.currentScript = s
		b.compileAndRun(name, src)
		b.currentScript = nil
	}
}

// compileAndRun compiles src as a classic script and runs it, falling back to esbuild
// dynamic-import lowering (lowerDynamicImport) when goja can't parse it. Used by both
// the initial script pass (runScripts) and dynamically inserted <script src> chunks
// (loadExternalScript). A compile or runtime error is recorded, never fatal.
func (b *bridge) compileAndRun(name, src string) {
	prog, err := classicProgram(name, stripSourceMappingURL(src))
	if err != nil {
		b.recordError(err) // original goja error preserves today's diagnostic text
		return
	}
	if _, err := b.vm.RunProgram(prog); err != nil {
		b.reportException(err) // window.onerror / "error" listeners, then diagnostics
	}
}

// classicProgram resolves the Program for a classic script: goja compile with
// the esbuild dynamic-import-lowering fallback. The whole outcome — the lowered
// program or the original compile error — is cached across renders, so neither
// goja's parse nor esbuild's Transform re-runs for identical source. The key is
// content-only: name (the script URL, or a positional fallback for inline
// scripts) reaches goja for diagnostics but never splits the cache.
func classicProgram(name, src string) (*goja.Program, error) {
	key := progKey{kind: "classic", hash: sha256.Sum256([]byte(src))}
	if e, ok := progCacheGet(key); ok {
		return e.prog, e.err
	}
	// Escape shorthand-then-hyphen in regex classes (/[\s-_]/) that goja would
	// otherwise reject at compile time; a no-op for source that lacks the pattern.
	src = fixClassRangeHyphen(src)
	prog, err := goja.Compile(name, src, false)
	if err != nil && strings.Contains(src, "import") {
		// goja can't parse dynamic import(); lower it via esbuild and retry so an SPA
		// bundle runs instead of being skipped wholesale. Only on a compile error, only
		// when the source could contain an import token.
		if lowered, ok := lowerDynamicImport(src); ok {
			if p2, e2 := goja.Compile(name+".lowered", lowered, false); e2 == nil {
				prog, err = p2, nil
			}
		}
	}
	if err != nil && (strings.Contains(src, "async") || strings.Contains(src, "await")) {
		// Still not compiling: goja also lacks async generators and for-await.
		// Lower those too (and dynamic import with them) and try once more.
		if lowered, ok := lowerAsyncIteration(src); ok {
			if p2, e2 := goja.Compile(name+".lowered", lowered, false); e2 == nil {
				prog, err = p2, nil
			}
		}
	}
	progCachePut(key, progEntry{prog: prog, err: err, size: int64(len(src))})
	return prog, err
}
