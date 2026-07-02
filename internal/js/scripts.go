package js

import (
	"context"
	"crypto/sha256"
	"fmt"
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
	b.loadExternalScript(n, src)
}

// scriptSrc returns a script element's effective src. `el.src = url` sets a property on
// the JS wrapper (it does not reflect to the html attribute), while setAttribute('src',
// url) writes the attribute — so check both, preferring the JS property.
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
	go func() {
		var (
			body []byte
			ok   bool
			name = src
		)
		if abs, err := b.resolveURL(src); err == nil {
			name = abs
			if cached, hit := b.assets.get(assetKey(abs)); hit {
				body, ok = cached, true
			} else {
				ctx, cancel := context.WithTimeout(b.ctx, b.reqTimeout)
				res, ferr := b.transport.Do(ctx, "GET", abs, nil, nil)
				cancel()
				if ferr == nil && res != nil && res.Status < 400 {
					body, ok = res.Body, true
					b.assets.put(assetKey(abs), res.Body)
				}
			}
		}
		_ = b.loop.RunOnLoop(func(vm *goja.Runtime) {
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

// fireScriptEvent dispatches a non-bubbling load/error event at the script element and
// also invokes its on<type> property handler — webpack sets script.onload/onerror
// directly, and the generic dispatch only runs addEventListener listeners.
func (b *bridge) fireScriptEvent(n *html.Node, typ string) {
	wrapper := b.wrap(n)
	ev := b.newEvent(typ, false, false, wrapper)
	b.dispatchOnNode(n, ev)
	if obj := wrapper.ToObject(b.vm); obj != nil {
		if fn, ok := goja.AssertFunction(obj.Get("on" + typ)); ok {
			b.callSafe(fn, wrapper, ev.js)
		}
	}
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

// runScripts compiles and runs each script in document order. External (src)
// scripts are fetched synchronously via the Transport — classic scripts block —
// then run; inline scripts use their text. A syntax or runtime error skips that
// script so one bad script doesn't blank the page.
func (b *bridge) runScripts(scripts []*html.Node) {
	for i, s := range scripts {
		var src string
		if srcAttr := strings.TrimSpace(getAttr(s, "src")); srcAttr != "" {
			if b.transport == nil {
				continue // networking disabled: external scripts can't load
			}
			abs, err := b.resolveURL(srcAttr)
			if err != nil {
				continue
			}
			if body, ok := b.assets.get(assetKey(abs)); ok {
				src = string(body)
			} else {
				ctx, cancel := context.WithTimeout(b.ctx, b.reqTimeout)
				res, ferr := b.transport.Do(ctx, "GET", abs, nil, nil)
				cancel()
				if ferr != nil || res == nil || res.Status >= 400 {
					continue
				}
				src = string(res.Body)
				b.assets.put(assetKey(abs), res.Body)
			}
		} else {
			src = scriptText(s)
		}
		if strings.TrimSpace(src) == "" {
			continue
		}
		b.currentScript = s
		b.compileAndRun(fmt.Sprintf("script-%d.js", i), src)
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
		b.recordError(err)
	}
}

// classicProgram resolves the Program for a classic script: goja compile with
// the esbuild dynamic-import-lowering fallback. The whole outcome — the lowered
// program or the original compile error — is cached across renders, so neither
// goja's parse nor esbuild's Transform re-runs for identical source.
func classicProgram(name, src string) (*goja.Program, error) {
	key := progKey{name: "classic\x00" + name, hash: sha256.Sum256([]byte(src))}
	if e, ok := progCacheGet(key); ok {
		return e.prog, e.err
	}
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
	progCachePut(key, progEntry{prog: prog, err: err, size: int64(len(src))})
	return prog, err
}
