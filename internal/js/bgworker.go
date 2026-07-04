package js

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/christopherdavenport/unblink/internal/webext"
	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"golang.org/x/net/html"
)

// bgWorker runs an extension's background scripts on a dedicated, engine-lifetime
// eventloop — the analog of a persistent background page / service worker. It is a
// separate goja runtime from every page render (a runtime is single-goroutine), so its
// perpetual timers live in its own audit and can never hold a page render open; the
// only coupling is explicit messages, routed through the broker and bracketed on the
// page's pending counter so a content-script round-trip still keeps the page from
// settling until the reply lands (ADR 0004).
//
// It reuses the page bridge with a minimal empty document. A real service worker has no
// DOM; giving the background one is a pragmatic simplification (like same-world content
// scripts, ADR 0010) that lets it reuse the whole prelude + chrome API + fetch stack.
type bgWorker struct {
	bundle *webext.Bundle
	host   *ExtensionHost

	loop     *eventloop.EventLoop
	vm       atomic.Pointer[goja.Runtime]
	bridge   *bridge
	ctx      context.Context
	cancel   context.CancelFunc
	memGuard *memGuard

	started     atomic.Bool
	closed      atomic.Bool
	webReqCount atomic.Int32 // number of registered webRequest.onBeforeRequest listeners (read off-loop)
}

// bgReqTimeout bounds a background fetch (extension resource reads are local; this is a
// generous ceiling for any future external fetch).
const bgReqTimeout = 30 * time.Second

// bgStartTimeout bounds how long engine construction waits for the background's
// synchronous setup. A wedged background degrades to "not ready" rather than hanging.
const bgStartTimeout = 5 * time.Second

// start builds the background runtime and runs its scripts (which register the message /
// webRequest listeners). Called once at engine construction. It blocks until the
// background's synchronous top-level setup completes, so the listeners are in place
// before the first page request consults them (an off-loop webReqCount check, unlike
// message delivery, does not queue behind the script-run job).
func (w *bgWorker) start(memGuard *memGuard) {
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	w.memGuard = memGuard
	w.loop = newLoop()
	w.loop.Start()
	w.ctx, w.cancel = context.WithCancel(context.Background())
	doc, _ := html.Parse(strings.NewReader("<html><head></head><body></body></html>"))
	base, _ := url.Parse(w.bundle.BaseURL)
	ready := make(chan struct{})
	w.loop.RunOnLoop(func(vm *goja.Runtime) {
		defer close(ready)
		defer func() { _ = recover() }() // a panic here must not crash the loop goroutine
		w.vm.Store(vm)
		if memGuard != nil {
			memGuard.register(vm)
		}
		b := newBridge(vm, w.loop, doc, base, &bundleTransport{bundle: w.bundle}, nil, nil, nil, w.ctx, bgReqTimeout, w.host)
		b.bgMode = true
		b.install()
		_, _ = vm.RunProgram(preludeProgram)
		_, _ = vm.RunProgram(preludeAPIProgram)
		w.bridge = b
		w.runBackgroundScripts(b)
	})
	select {
	case <-ready:
	case <-time.After(bgStartTimeout):
	}
}

// runBackgroundScripts executes the manifest's background scripts. Classic scripts
// (MV2 background.scripts, or a non-module service_worker) run directly; a module
// service worker is bundled through the esbuild module path.
func (w *bgWorker) runBackgroundScripts(b *bridge) {
	bg := w.bundle.Manifest.Background
	if bg.ServiceWorker != "" && bg.Module {
		node := &html.Node{Type: html.ElementNode, Data: "script", Attr: []html.Attribute{
			{Key: "type", Val: "module"},
			{Key: "src", Val: bg.ServiceWorker},
		}}
		b.runModules([]*html.Node{node}, nil)
	}
	files := append([]string{}, bg.Scripts...)
	if bg.ServiceWorker != "" && !bg.Module {
		files = append(files, bg.ServiceWorker)
	}
	for _, f := range files {
		data, err := w.bundle.ReadResource(f)
		if err != nil {
			b.recordError(fmt.Errorf("background script %q: %w", f, err))
			continue
		}
		b.compileAndRun("background:"+w.bundle.ID+"/"+f, string(data))
	}
}

// deliver dispatches one message to the background's onMessage listeners on the
// background loop and routes the reply back through respond. respond is always called
// exactly once (nil when there is no listener / no response), so the page-side pending
// bracket is always released.
func (w *bgWorker) deliver(msg, sender any, respond func(any)) {
	if w == nil || w.closed.Load() {
		respond(nil)
		return
	}
	scheduled := w.loop.RunOnLoop(func(vm *goja.Runtime) {
		b := w.bridge
		if b == nil || len(b.msgListeners) == 0 {
			respond(nil)
			return
		}
		done := false
		respondOnce := func(v any) {
			if !done {
				done = true
				respond(v)
			}
		}
		sendResponse := func(call goja.FunctionCall) goja.Value {
			respondOnce(exportSafe(call.Argument(0)))
			return goja.Undefined()
		}
		jsMsg, jsSender, jsResp := vm.ToValue(msg), vm.ToValue(sender), vm.ToValue(sendResponse)
		async := false
		for _, fn := range b.msgListeners {
			ret, err := fn(goja.Undefined(), jsMsg, jsSender, jsResp)
			if err != nil {
				b.recordError(err)
				continue
			}
			if ret != nil && ret.ToBoolean() {
				async = true // listener will call sendResponse later
			}
		}
		if !done && !async {
			respondOnce(nil)
		}
	})
	if !scheduled {
		respond(nil)
	}
}

// close tears down the background runtime.
func (w *bgWorker) close() {
	if w == nil || !w.closed.CompareAndSwap(false, true) {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	if w.memGuard != nil {
		if vm := w.vm.Load(); vm != nil {
			w.memGuard.unregister(vm)
		}
	}
	if w.loop != nil {
		w.loop.Terminate()
	}
}

// bundleTransport serves an extension's own files (chrome-extension://<id>/path or an
// extension-relative path) to the background's fetch/module loader. External network
// from the background is out of scope for now (returns an error).
type bundleTransport struct {
	bundle *webext.Bundle
}

func (t *bundleTransport) Do(_ context.Context, _ string, rawURL string, _ map[string]string, _ []byte) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "" && u.Scheme != "chrome-extension" {
		return nil, fmt.Errorf("bundle transport: external fetch %q not supported", rawURL)
	}
	data, err := t.bundle.ReadResource(u.Path)
	if err != nil {
		return &Response{Status: 404, FinalURL: rawURL}, nil
	}
	return &Response{Status: 200, Headers: map[string]string{}, Body: data, FinalURL: rawURL}, nil
}

// exportSafe converts a goja value to a plain Go value that can cross runtimes.
func exportSafe(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}
