package js

import "github.com/dop251/goja"

// Per-tab navigation events. A real extension (uBlock Origin) tracks each tab's
// committed document via chrome.webNavigation / chrome.tabs events, and uses that
// per-tab page context to decide first/third-party and per-site network rules. unblink
// gives every page render a unique synthetic tab id and fires a navigation sequence into
// the background before the page's requests, so the extension builds the right page
// store (without it, uBO filters against an empty document and over-blocks unrelated
// third-party requests).

// newHostEvent returns a background event object whose listeners the host fires
// (webNavigation/tabs). Registered only in bgMode.
func (b *bridge) newHostEvent(name string) *goja.Object {
	o := b.vm.NewObject()
	_ = o.Set("addListener", func(call goja.FunctionCall) goja.Value {
		if fn, ok := goja.AssertFunction(call.Argument(0)); ok {
			if b.hostEvents == nil {
				b.hostEvents = map[string][]goja.Callable{}
			}
			b.hostEvents[name] = append(b.hostEvents[name], fn)
		}
		return goja.Undefined()
	})
	_ = o.Set("removeListener", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("hasListener", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(len(b.hostEvents[name]) > 0) })
	return o
}

// fireHostEvent invokes the background's listeners for name with args. Runs on the
// background loop.
func (b *bridge) fireHostEvent(name string, args ...goja.Value) {
	for _, fn := range b.hostEvents[name] {
		if _, err := fn(goja.Undefined(), args...); err != nil {
			b.recordError(err)
		}
	}
}

// nextTabID allocates a unique synthetic tab id for a page render/session.
func (h *ExtensionHost) nextTabID() int {
	if h == nil {
		return 0
	}
	return int(h.tabSeq.Add(1))
}

// notifyNavigation tells the background that a tab committed a navigation to rawURL, so
// the extension can build its per-tab page store. Fired on the background loop; because
// it is scheduled at render start (before the page's scripts run) it is processed ahead
// of the request-verdict round-trips that follow.
func (h *ExtensionHost) notifyNavigation(tabID int, rawURL string) {
	if h == nil {
		return
	}
	w := h.bg
	if w == nil || tabID == 0 || rawURL == "" {
		return
	}
	w.loop.RunOnLoop(func(vm *goja.Runtime) {
		b := w.bridge
		if b == nil {
			return
		}
		nav := vm.NewObject()
		_ = nav.Set("tabId", tabID)
		_ = nav.Set("frameId", 0)
		_ = nav.Set("parentFrameId", -1)
		_ = nav.Set("url", rawURL)
		_ = nav.Set("timeStamp", 0)
		b.fireHostEvent("webNavigation.onBeforeNavigate", nav)
		b.fireHostEvent("webNavigation.onCommitted", nav)
		change := vm.NewObject()
		_ = change.Set("status", "complete")
		_ = change.Set("url", rawURL)
		b.fireHostEvent("tabs.onUpdated", vm.ToValue(tabID), change, b.tabObject(vm, tabID, rawURL))
		b.fireHostEvent("webNavigation.onDOMContentLoaded", nav)
		b.fireHostEvent("webNavigation.onCompleted", nav)
	})
}

// notifyTabRemoved tells the background a tab closed, so the extension can drop its page
// store for it.
func (h *ExtensionHost) notifyTabRemoved(tabID int) {
	if h == nil {
		return
	}
	w := h.bg
	if w == nil || tabID == 0 {
		return
	}
	w.loop.RunOnLoop(func(vm *goja.Runtime) {
		b := w.bridge
		if b == nil {
			return
		}
		info := vm.NewObject()
		_ = info.Set("windowId", 1)
		_ = info.Set("isWindowClosing", false)
		b.fireHostEvent("tabs.onRemoved", vm.ToValue(tabID), info)
	})
}

// tabObject builds a chrome.tabs Tab for a synthetic tab.
func (b *bridge) tabObject(vm *goja.Runtime, tabID int, rawURL string) *goja.Object {
	t := vm.NewObject()
	_ = t.Set("id", tabID)
	_ = t.Set("url", rawURL)
	_ = t.Set("active", true)
	_ = t.Set("status", "complete")
	_ = t.Set("windowId", 1)
	_ = t.Set("incognito", false)
	return t
}
