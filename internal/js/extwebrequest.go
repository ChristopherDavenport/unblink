package js

import (
	"net/url"
	"strings"
	"time"

	"github.com/christopherdavenport/unblink/internal/webext"
	"github.com/dop251/goja"
)

// MV2 webRequest support. Manifest-V2 uBlock Origin does its own network filtering in
// the background: it registers chrome.webRequest.onBeforeRequest as a *blocking*
// listener and returns {cancel:true} (or {redirectUrl}) for requests its engine matches.
// unblink doesn't reimplement that matcher — it delivers each subrequest to the
// background's listeners and honors their verdict.
//
// The listeners live in the background runtime, but a request is decided on the page's
// fetch goroutine (off every event loop). So the verdict is fetched via a
// timeout-guarded round-trip onto the background loop: no event loop blocks, and a wedged
// background degrades to "allow" rather than hanging the render.

// webReqTimeout bounds how long a page subrequest waits for the background's verdict.
const webReqTimeout = 2 * time.Second

// webReqListener is one registered onBeforeRequest listener with its URL filter.
type webReqListener struct {
	fn       goja.Callable
	patterns []webext.MatchPattern // filter.urls; empty means all URLs
}

func (l webReqListener) matches(u *url.URL) bool {
	if len(l.patterns) == 0 {
		return true
	}
	for _, p := range l.patterns {
		if p.Matches(u) {
			return true
		}
	}
	return false
}

// newWebRequestEvent builds a chrome.webRequest.onBeforeRequest event. In the background
// it registers real blocking listeners; elsewhere it is an inert stub.
func (b *bridge) newWebRequestEvent() *goja.Object {
	o := b.vm.NewObject()
	if !b.bgMode {
		return b.newEventStub()
	}
	_ = o.Set("addListener", func(call goja.FunctionCall) goja.Value {
		fn, ok := goja.AssertFunction(call.Argument(0))
		if !ok {
			return goja.Undefined()
		}
		var patterns []webext.MatchPattern
		if filter, ok := call.Argument(1).(*goja.Object); ok {
			for _, u := range toStringSlice(filter.Get("urls")) {
				if mp, err := webext.ParseMatchPattern(u); err == nil {
					patterns = append(patterns, mp)
				}
			}
		}
		b.webReqListeners = append(b.webReqListeners, webReqListener{fn: fn, patterns: patterns})
		if b.extHost != nil && b.extHost.bg != nil {
			b.extHost.bg.webReqCount.Add(1)
		}
		return goja.Undefined()
	})
	_ = o.Set("removeListener", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.Set("hasListener", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(len(b.webReqListeners) > 0) })
	return o
}

// webRequestVerdict asks the background's onBeforeRequest listeners whether to cancel or
// redirect req. Returns an empty decision when there are no listeners, the background is
// absent, or the round-trip times out.
func (h *ExtensionHost) webRequestVerdict(req webext.Request) webext.Decision {
	w := h.bg
	if w == nil || w.webReqCount.Load() == 0 {
		return webext.Decision{}
	}
	result := make(chan webext.Decision, 1)
	scheduled := w.loop.RunOnLoop(func(vm *goja.Runtime) {
		result <- w.bridge.runWebRequest(vm, req)
	})
	if !scheduled {
		return webext.Decision{}
	}
	select {
	case d := <-result:
		return d
	case <-time.After(webReqTimeout):
		return webext.Decision{}
	}
}

// runWebRequest invokes the matching onBeforeRequest listeners and returns the first
// blocking/redirecting verdict. Runs on the background loop.
func (b *bridge) runWebRequest(vm *goja.Runtime, req webext.Request) webext.Decision {
	for _, l := range b.webReqListeners {
		if !l.matches(req.URL) {
			continue
		}
		details := vm.NewObject()
		_ = details.Set("url", req.URL.String())
		_ = details.Set("method", strings.ToUpper(req.Method))
		_ = details.Set("type", string(req.Type))
		_ = details.Set("tabId", 1)
		_ = details.Set("frameId", 0)
		_ = details.Set("parentFrameId", -1)
		_ = details.Set("requestId", "0")
		ret, err := l.fn(goja.Undefined(), details)
		if err != nil {
			b.recordError(err)
			continue
		}
		if d := webReqDecision(ret); d.Block || d.RedirectTo != "" {
			return d
		}
	}
	return webext.Decision{}
}

// webReqDecision reads a blocking onBeforeRequest return value ({cancel:true} or
// {redirectUrl:"..."}).
func webReqDecision(ret goja.Value) webext.Decision {
	o, ok := ret.(*goja.Object)
	if !ok {
		return webext.Decision{}
	}
	if v := o.Get("cancel"); v != nil && v.ToBoolean() {
		return webext.Decision{Block: true}
	}
	if v := o.Get("redirectUrl"); v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		return webext.Decision{RedirectTo: v.String()}
	}
	return webext.Decision{}
}
