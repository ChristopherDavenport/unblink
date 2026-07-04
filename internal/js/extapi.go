package js

import (
	"encoding/json"

	"github.com/christopherdavenport/unblink/internal/webext"
	"github.com/dop251/goja"
)

// installExtensionAPI installs the chrome / browser namespace that content scripts
// (and, later, the background worker) call. Everything is a Go closure because each
// method touches host state — storage, the cosmetic buffer, the loaded manifest. With
// unblink's same-world content-script model there is one shared chrome object bound to
// the "active" extension (ADR 0010); a no-op or accept-and-ignore stub is provided for
// the UI/eventing surface unblink has no equivalent for, so an extension's init code
// runs instead of throwing. Guarded by b.extHost != nil in install().
func (b *bridge) installExtensionAPI(win *goja.Object) {
	vm := b.vm
	active := b.extHost.activeBundleFor(b.base)
	if active == nil {
		return
	}
	b.extActiveID = active.ID
	chrome := vm.NewObject()

	// --- chrome.runtime ---
	runtime := vm.NewObject()
	_ = runtime.Set("id", active.ID)
	getURL := func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(active.BaseURL + trimLeadingSlash(call.Argument(0).String()))
	}
	_ = runtime.Set("getURL", getURL)
	_ = runtime.Set("getManifest", func(goja.FunctionCall) goja.Value {
		// Return the full manifest so an extension can read any field (browser_action,
		// icons, options_ui, …), with __MSG__ references resolved as a browser does.
		if len(active.RawManifest) > 0 {
			var mo map[string]any
			if json.Unmarshal([]byte(active.Locales.Substitute(string(active.RawManifest))), &mo) == nil {
				return vm.ToValue(mo)
			}
		}
		m := vm.NewObject()
		if man := active.Manifest; man != nil {
			_ = m.Set("manifest_version", man.ManifestVersion)
			_ = m.Set("name", active.Locales.Substitute(man.Name))
			_ = m.Set("version", man.Version)
		}
		return m
	})
	_ = runtime.Set("getPlatformInfo", func(call goja.FunctionCall) goja.Value {
		info := vm.NewObject()
		_ = info.Set("os", "linux")
		_ = info.Set("arch", "x86-64")
		return b.apiReturn(call, info)
	})
	// runtime messaging. In the background context, onMessage registers real listeners
	// and sendMessage (background → other contexts) is not delivered yet. In a page/
	// content-script context, sendMessage routes to the background through the broker,
	// bracketed on the page's pending counter (ADR 0004); onMessage is a stub (pages
	// rarely receive unsolicited messages in this model).
	if b.bgMode {
		_ = runtime.Set("onMessage", b.newOnMessageListener())
		_ = runtime.Set("sendMessage", func(call goja.FunctionCall) goja.Value {
			return b.apiReturn(call, goja.Undefined())
		})
	} else {
		_ = runtime.Set("onMessage", b.newEventStub())
		_ = runtime.Set("sendMessage", func(call goja.FunctionCall) goja.Value {
			msg, cb := sendMessageArgs(call)
			return b.sendMessageToBackground(msg, cb)
		})
	}
	_ = runtime.Set("connect", func(goja.FunctionCall) goja.Value { return b.newPortStub() })
	_ = runtime.Set("onConnect", b.newEventStub())
	_ = runtime.Set("onInstalled", b.newEventStub())
	_ = runtime.Set("onStartup", b.newEventStub())
	_ = runtime.Set("lastError", goja.Undefined())
	_ = chrome.Set("runtime", runtime)

	// --- chrome.i18n ---
	i18n := vm.NewObject()
	_ = i18n.Set("getMessage", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(active.Locales.GetSub(call.Argument(0).String(), toStringSlice(call.Argument(1))))
	})
	_ = i18n.Set("getUILanguage", func(goja.FunctionCall) goja.Value { return vm.ToValue("en-US") })
	_ = i18n.Set("getAcceptLanguages", func(call goja.FunctionCall) goja.Value {
		return b.apiReturn(call, vm.ToValue([]string{"en-US", "en"}))
	})
	_ = chrome.Set("i18n", i18n)

	// --- chrome.extension (legacy aliases) ---
	ext := vm.NewObject()
	_ = ext.Set("getURL", getURL)
	_ = ext.Set("inIncognitoContext", false)
	_ = chrome.Set("extension", ext)

	// --- chrome.storage ---
	storage := vm.NewObject()
	for _, area := range []string{"local", "session", "sync", "managed"} {
		_ = storage.Set(area, b.newStorageArea(active.ID, area))
	}
	_ = storage.Set("onChanged", b.newStorageOnChanged(active.ID))
	_ = chrome.Set("storage", storage)

	// --- chrome.scripting (insertCSS feeds the cosmetic pass) ---
	scripting := vm.NewObject()
	_ = scripting.Set("insertCSS", func(call goja.FunctionCall) goja.Value {
		if o, ok := call.Argument(0).(*goja.Object); ok {
			b.captureInsertCSS(o, active)
		}
		return b.apiReturn(call, goja.Undefined())
	})
	_ = scripting.Set("removeCSS", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, goja.Undefined()) })
	_ = scripting.Set("executeScript", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.ToValue([]any{})) })
	_ = scripting.Set("registerContentScripts", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, goja.Undefined()) })
	_ = chrome.Set("scripting", scripting)

	// --- chrome.tabs (single synthetic tab) ---
	tabs := vm.NewObject()
	tabObj := func() *goja.Object {
		o := vm.NewObject()
		_ = o.Set("id", 1)
		_ = o.Set("active", true)
		if b.base != nil {
			_ = o.Set("url", b.base.String())
		}
		return o
	}
	_ = tabs.Set("query", func(call goja.FunctionCall) goja.Value {
		return b.apiReturn(call, vm.ToValue([]any{tabObj()}))
	})
	_ = tabs.Set("get", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, tabObj()) })
	_ = tabs.Set("sendMessage", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, goja.Undefined()) })
	_ = tabs.Set("insertCSS", func(call goja.FunctionCall) goja.Value {
		for _, a := range call.Arguments {
			if o, ok := a.(*goja.Object); ok {
				b.captureInsertCSS(o, active)
			}
		}
		return b.apiReturn(call, goja.Undefined())
	})
	_ = tabs.Set("onUpdated", b.hostOrStubEvent("tabs.onUpdated"))
	_ = tabs.Set("onRemoved", b.hostOrStubEvent("tabs.onRemoved"))
	_ = tabs.Set("onActivated", b.hostOrStubEvent("tabs.onActivated"))
	_ = tabs.Set("onCreated", b.hostOrStubEvent("tabs.onCreated"))
	_ = chrome.Set("tabs", tabs)

	// --- chrome.webNavigation: real events in the background so the extension can
	//     build a per-tab page store from the navigations unblink fires ---
	webNav := vm.NewObject()
	for _, ev := range []string{"onBeforeNavigate", "onCommitted", "onDOMContentLoaded", "onCompleted", "onCreatedNavigationTarget", "onErrorOccurred", "onReferenceFragmentUpdated", "onHistoryStateUpdated"} {
		_ = webNav.Set(ev, b.hostOrStubEvent("webNavigation."+ev))
	}
	_ = webNav.Set("getFrame", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, goja.Null()) })
	_ = webNav.Set("getAllFrames", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.ToValue([]any{})) })
	_ = chrome.Set("webNavigation", webNav)

	// --- chrome.action / browserAction (accept-and-ignore) ---
	action := b.newActionStub()
	_ = chrome.Set("action", action)
	_ = chrome.Set("browserAction", action)

	// --- chrome.permissions (declared permissions are granted) ---
	perms := vm.NewObject()
	_ = perms.Set("contains", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.ToValue(true)) })
	_ = perms.Set("request", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.ToValue(true)) })
	_ = perms.Set("getAll", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.NewObject()) })
	_ = perms.Set("onAdded", b.newEventStub())
	_ = perms.Set("onRemoved", b.newEventStub())
	_ = chrome.Set("permissions", perms)

	// --- chrome.declarativeNetRequest (dynamic rules are Phase 4; static rules are
	//     enforced host-side already) ---
	dnr := vm.NewObject()
	applyRules := func(update func(add []webext.DNRRule, removeIDs []int) error) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			if o, ok := call.Argument(0).(*goja.Object); ok {
				_ = update(parseDNRRulesArg(o.Get("addRules")), toIntSlice(o.Get("removeRuleIds")))
			}
			return b.apiReturn(call, goja.Undefined())
		}
	}
	_ = dnr.Set("updateDynamicRules", applyRules(active.Net.UpdateDynamic))
	_ = dnr.Set("updateSessionRules", applyRules(active.Net.UpdateSession))
	_ = dnr.Set("getDynamicRules", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.ToValue([]any{})) })
	_ = dnr.Set("getSessionRules", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.ToValue([]any{})) })
	_ = dnr.Set("updateEnabledRulesets", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, goja.Undefined()) })
	_ = dnr.Set("isRegexSupported", func(call goja.FunctionCall) goja.Value {
		o := vm.NewObject()
		_ = o.Set("isSupported", true)
		return b.apiReturn(call, o)
	})
	_ = chrome.Set("declarativeNetRequest", dnr)

	// --- chrome.webRequest (MV2): onBeforeRequest is a real blocking listener in the
	//     background; the other stages are inert stubs ---
	webRequest := vm.NewObject()
	_ = webRequest.Set("onBeforeRequest", b.newWebRequestEvent())
	for _, ev := range []string{"onBeforeSendHeaders", "onSendHeaders", "onHeadersReceived", "onResponseStarted", "onCompleted", "onErrorOccurred", "onBeforeRedirect", "onAuthRequired"} {
		_ = webRequest.Set(ev, b.newEventStub())
	}
	_ = chrome.Set("webRequest", webRequest)

	// Any namespace not implemented above (alarms, contextMenus, notifications,
	// webNavigation, windows, idle, …) falls through to a permissive inert stub so an
	// extension's init doesn't throw on a missing API. See extapi_permissive.go.
	chromeObj := b.wrapChrome(chrome)
	_ = vm.Set("chrome", chromeObj)
	_ = vm.Set("browser", chromeObj) // Firefox alias
}

// apiReturn supports both the MV2 callback and MV3 promise forms: it invokes a
// trailing function argument (if any) with result and returns a resolved promise of
// result. Resolving synchronously keeps the settle audit intact — the .then job runs
// on goja's tracked microtask queue, introducing no new async primitive (ADR 0004).
func (b *bridge) apiReturn(call goja.FunctionCall, result goja.Value) goja.Value {
	if n := len(call.Arguments); n > 0 {
		if fn, ok := goja.AssertFunction(call.Argument(n - 1)); ok {
			_, _ = fn(goja.Undefined(), result)
		}
	}
	promise, resolve, _ := b.vm.NewPromise()
	_ = resolve(result)
	return b.vm.ToValue(promise)
}

// hostOrStubEvent returns a real host-fired event in the background context, and an
// inert stub elsewhere (a page can't observe tab/navigation events).
func (b *bridge) hostOrStubEvent(name string) *goja.Object {
	if b.bgMode {
		return b.newHostEvent(name)
	}
	return b.newEventStub()
}

// newEventStub returns an addListener/removeListener/hasListener event object whose
// listeners never fire (no background/eventing surface until later phases).
func (b *bridge) newEventStub() *goja.Object {
	o := b.vm.NewObject()
	_ = o.Set("addListener", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("removeListener", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("hasListener", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
	return o
}

// newPortStub returns an inert runtime.Port (no background worker to connect to yet).
func (b *bridge) newPortStub() *goja.Object {
	o := b.vm.NewObject()
	_ = o.Set("name", "")
	_ = o.Set("postMessage", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("disconnect", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("onMessage", b.newEventStub())
	_ = o.Set("onDisconnect", b.newEventStub())
	return o
}

// newActionStub returns an accept-and-ignore browser action (no toolbar UI).
func (b *bridge) newActionStub() *goja.Object {
	o := b.vm.NewObject()
	for _, m := range []string{"setIcon", "setBadgeText", "setBadgeBackgroundColor", "setTitle", "setPopup", "enable", "disable", "setBadgeTextColor"} {
		_ = o.Set(m, func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, goja.Undefined()) })
	}
	_ = o.Set("onClicked", b.newEventStub())
	return o
}

// newStorageArea builds a chrome.storage area (get/set/remove/clear/getBytesInUse) over
// the host's shared in-memory store, scoped to this extension + area.
func (b *bridge) newStorageArea(extID, area string) *goja.Object {
	vm := b.vm
	st := b.extHost.storage
	o := vm.NewObject()
	_ = o.Set("get", func(call goja.FunctionCall) goja.Value {
		keys, all, defaults := storageKeys(call.Argument(0))
		res := st.get(extID, area, keys, all)
		for k, v := range defaults { // fill declared defaults for missing keys
			if _, ok := res[k]; !ok {
				res[k] = v
			}
		}
		return b.apiReturn(call, vm.ToValue(res))
	})
	_ = o.Set("set", func(call goja.FunctionCall) goja.Value {
		if o, ok := call.Argument(0).(*goja.Object); ok {
			kv := map[string]any{}
			for _, k := range o.Keys() {
				kv[k] = o.Get(k).Export()
			}
			st.set(extID, area, kv)
		}
		return b.apiReturn(call, goja.Undefined())
	})
	_ = o.Set("remove", func(call goja.FunctionCall) goja.Value {
		st.remove(extID, area, toStringSlice(call.Argument(0)))
		return b.apiReturn(call, goja.Undefined())
	})
	_ = o.Set("clear", func(call goja.FunctionCall) goja.Value {
		st.clear(extID, area)
		return b.apiReturn(call, goja.Undefined())
	})
	_ = o.Set("getBytesInUse", func(call goja.FunctionCall) goja.Value { return b.apiReturn(call, vm.ToValue(0)) })
	_ = o.Set("onChanged", b.newEventStub())
	return o
}

// newStorageOnChanged is the top-level chrome.storage.onChanged: it registers a real
// listener that the store fans changes out to, delivered on this bridge's loop.
func (b *bridge) newStorageOnChanged(extID string) *goja.Object {
	o := b.vm.NewObject()
	_ = o.Set("addListener", func(call goja.FunctionCall) goja.Value {
		fn, ok := goja.AssertFunction(call.Argument(0))
		if !ok {
			return goja.Undefined()
		}
		b.extHost.storage.subscribe(extID, func(changes map[string]any, area string) bool {
			return b.loop.RunOnLoop(func(vm *goja.Runtime) {
				_, _ = fn(goja.Undefined(), vm.ToValue(changes), vm.ToValue(area))
			})
		})
		return goja.Undefined()
	})
	_ = o.Set("removeListener", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("hasListener", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
	return o
}

// captureInsertCSS pulls CSS text from a scripting/tabs insertCSS details object
// (inline `css`/`code`, or `files`/`file` read from the extension) into the cosmetic
// buffer, so injected element-hiding stylesheets remove nodes at extraction time.
func (b *bridge) captureInsertCSS(details *goja.Object, active *webext.Bundle) {
	if v := details.Get("css"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		b.addCosmeticCSS(active.Locales.Substitute(v.String()))
	}
	if v := details.Get("code"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		b.addCosmeticCSS(active.Locales.Substitute(v.String()))
	}
	for _, key := range []string{"files", "file"} {
		for _, f := range toStringSlice(details.Get(key)) {
			if data, err := active.ReadResource(f); err == nil {
				b.addCosmeticCSS(active.Locales.Substitute(string(data)))
			}
		}
	}
}

// storageKeys interprets a chrome.storage.get argument: null/undefined → all keys; a
// string or array → those keys; an object → its keys, with the object's values as
// defaults for missing entries.
func storageKeys(v goja.Value) (keys []string, all bool, defaults map[string]any) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, true, nil
	}
	if o, ok := v.(*goja.Object); ok {
		if _, isArr := o.Export().([]any); !isArr {
			defaults = map[string]any{}
			for _, k := range o.Keys() {
				keys = append(keys, k)
				defaults[k] = o.Get(k).Export()
			}
			return keys, false, defaults
		}
	}
	return toStringSlice(v), false, nil
}

// toStringSlice normalizes a goja value that may be a string, an array of strings, or
// absent into a []string.
func toStringSlice(v goja.Value) []string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch e := v.Export().(type) {
	case string:
		return []string{e}
	case []any:
		out := make([]string, 0, len(e))
		for _, x := range e {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return e
	}
	return nil
}

func trimLeadingSlash(s string) string {
	for len(s) > 0 && s[0] == '/' {
		s = s[1:]
	}
	return s
}

// parseDNRRulesArg converts a JS array of declarativeNetRequest rule objects into
// []webext.DNRRule by round-tripping through JSON — reusing the same parser the static
// rulesets use, so dynamic and static rules behave identically.
func parseDNRRulesArg(v goja.Value) []webext.DNRRule {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	data, err := json.Marshal(v.Export())
	if err != nil {
		return nil
	}
	rules, err := webext.ParseRules(data)
	if err != nil {
		return nil
	}
	return rules
}

// toIntSlice normalizes a goja value (a number or array of numbers) into []int.
func toIntSlice(v goja.Value) []int {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch e := v.Export().(type) {
	case int64:
		return []int{int(e)}
	case float64:
		return []int{int(e)}
	case []any:
		out := make([]int, 0, len(e))
		for _, x := range e {
			switch n := x.(type) {
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	}
	return nil
}

// newOnMessageListener is the background context's chrome.runtime.onMessage: it appends
// real listeners the broker dispatches to.
func (b *bridge) newOnMessageListener() *goja.Object {
	o := b.vm.NewObject()
	_ = o.Set("addListener", func(call goja.FunctionCall) goja.Value {
		if fn, ok := goja.AssertFunction(call.Argument(0)); ok {
			b.msgListeners = append(b.msgListeners, fn)
		}
		return goja.Undefined()
	})
	_ = o.Set("removeListener", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("hasListener", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(len(b.msgListeners) > 0) })
	return o
}

// sendMessageArgs extracts (message, callback) from a chrome.runtime.sendMessage call.
// The common forms are sendMessage(message) and sendMessage(message, callback); the
// extensionId-prefixed overloads are not distinguished here (Phase 3).
func sendMessageArgs(call goja.FunctionCall) (goja.Value, goja.Callable) {
	n := len(call.Arguments)
	if n == 0 {
		return goja.Undefined(), nil
	}
	if n >= 2 {
		if fn, ok := goja.AssertFunction(call.Argument(n - 1)); ok {
			return call.Argument(0), fn
		}
	}
	return call.Argument(0), nil
}

// sendMessageToBackground routes a page/content-script message to the background worker
// and resolves with its reply. The round-trip is bracketed on b.pending (with a
// keepalive) so the render will not settle until the reply lands — the ADR-0004
// invariant that keeps a content-script message from being snapshotted away.
func (b *bridge) sendMessageToBackground(msg goja.Value, cb goja.Callable) goja.Value {
	vm := b.vm
	promise, resolve, _ := vm.NewPromise()
	if b.extHost == nil || b.extHost.broker == nil {
		_ = resolve(goja.Undefined())
		return vm.ToValue(promise)
	}
	msgGo := exportSafe(msg)
	sender := map[string]any{"id": b.extActiveID}
	if b.base != nil {
		sender["url"] = b.base.String()
		sender["origin"] = b.base.Scheme + "://" + b.base.Host
	}

	b.pending.Add(1) // hold the settle open across the round-trip
	keep := b.acquireKeepalive()
	respond := func(resp any) {
		_ = b.loop.RunOnLoop(func(vm *goja.Runtime) {
			rv := vm.ToValue(resp)
			if cb != nil {
				_, _ = cb(goja.Undefined(), rv)
			}
			_ = resolve(rv)
			b.releaseKeepalive(keep)
			b.pending.Add(-1)
		})
	}
	b.extHost.broker.sendToBackground(msgGo, sender, respond)
	return vm.ToValue(promise)
}
