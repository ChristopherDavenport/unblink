package js

import (
	"context"
	"net/url"
	"strings"

	"github.com/dop251/goja"
)

// installAsync exposes __unblinkFetch(method, url, headers, body) -> Promise to
// JS. window.fetch and XMLHttpRequest are built on top of it in the prelude. It
// is only installed when a Transport is available; otherwise fetch/XHR keep their
// rejecting stubs.
func (b *bridge) installAsync() {
	if b.transport == nil {
		return
	}
	_ = b.vm.Set("__unblinkFetch", func(call goja.FunctionCall) goja.Value {
		method := "GET"
		if a := call.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) && a.String() != "" {
			method = strings.ToUpper(a.String())
		}
		rawURL := call.Argument(1).String()
		headers := b.toStringMap(call.Argument(2))
		// Body: string, or an ArrayBuffer for binary-faithful uploads (the prelude
		// serializes Blob/FormData/typed-array bodies to an ArrayBuffer).
		var body []byte
		if a := call.Argument(3); !goja.IsUndefined(a) && !goja.IsNull(a) {
			if ab, ok := a.Export().(goja.ArrayBuffer); ok {
				body = ab.Bytes()
			} else if s := a.String(); s != "" {
				body = []byte(s)
			}
		}
		return b.fetchPromise(method, rawURL, headers, body)
	})
}

// fetchPromise returns a Promise that settles when the (off-loop) HTTP request
// completes. A JS-level setTimeout keepalive holds the event loop open while the
// request is in flight (its jobCount increment is synchronous on-loop, unlike the
// native SetTimeout). We resolve/reject BEFORE clearing the keepalive so any
// continuation that starts another fetch acquires its own keepalive first.
func (b *bridge) fetchPromise(method, rawURL string, headers map[string]string, body []byte) goja.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()

	abs, err := b.resolveURL(rawURL)
	if err != nil {
		_ = reject(vm.ToValue("fetch: " + err.Error()))
		return vm.ToValue(promise)
	}

	b.pending.Add(1) // off-loop request in flight; bracketed by the keepalive window
	keep := b.acquireKeepalive()
	go func() {
		ctx, cancel := context.WithTimeout(xhrCtx(b.ctx), b.reqTimeout)
		defer cancel()
		res, ferr := b.transport.Do(ctx, method, abs, headers, body)
		_ = b.loop.RunOnLoop(func(vm *goja.Runtime) {
			if ferr != nil {
				_ = reject(vm.ToValue(ferr.Error()))
			} else {
				_ = resolve(b.responseObject(res))
			}
			b.releaseKeepalive(keep)
			b.pending.Add(-1)
		})
		// If RunOnLoop returned false the loop was terminated; res.Body is already
		// fully read by the Transport, so there is nothing to clean up.
	}()
	return vm.ToValue(promise)
}

func (b *bridge) resolveURL(rawURL string) (string, error) {
	if base := b.docBaseNow(); base != nil {
		u, err := base.Parse(rawURL)
		if err != nil {
			return "", err
		}
		return u.String(), nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// acquireKeepalive schedules a far-future JS timeout to pin the event loop's job
// count > 0 while async work is outstanding. Using the JS-level setTimeout (not
// loop.SetTimeout) makes the increment synchronous on the loop goroutine.
func (b *bridge) acquireKeepalive() goja.Value {
	if b.jsSetTimeout == nil {
		return nil
	}
	id, err := b.jsSetTimeout(goja.Undefined(), b.noopVal, b.vm.ToValue(3_600_000))
	if err != nil {
		return nil
	}
	return id
}

func (b *bridge) releaseKeepalive(id goja.Value) {
	if b.jsClearTimeout == nil || id == nil || goja.IsUndefined(id) {
		return
	}
	_, _ = b.jsClearTimeout(goja.Undefined(), id)
}

func (b *bridge) responseObject(r *Response) goja.Value {
	vm := b.vm
	o := vm.NewObject()
	_ = o.Set("status", r.Status)
	_ = o.Set("ok", r.Status >= 200 && r.Status < 300)
	_ = o.Set("url", r.FinalURL)
	_ = o.Set("body", string(r.Body))
	// bodyBytes carries the raw bytes for Response.arrayBuffer()/blob(); the
	// string body above stays the fast path for text()/json().
	_ = o.Set("bodyBytes", vm.NewArrayBuffer(r.Body))
	h := vm.NewObject()
	for k, v := range r.Headers {
		_ = h.Set(strings.ToLower(k), v)
	}
	_ = o.Set("headers", h)
	return o
}

func (b *bridge) toStringMap(v goja.Value) map[string]string {
	if v == nil || goja.IsNull(v) || goja.IsUndefined(v) {
		return nil
	}
	o := v.ToObject(b.vm)
	if o == nil {
		return nil
	}
	keys := o.Keys()
	if len(keys) == 0 {
		return nil
	}
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		m[k] = o.Get(k).String()
	}
	return m
}
