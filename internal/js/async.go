package js

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// xhrTimeoutReason is the rejection value of a request that ran past the
// per-request timeout the caller passed to __unblinkFetch (XMLHttpRequest.timeout).
// The prelude matches it to fire `timeout` rather than `error`.
const xhrTimeoutReason = "fetch: timeout"

// installAsync exposes __unblinkFetch(method, url, headers, body, mode,
// credentials[, timeoutMs]) -> Promise to JS. window.fetch and XMLHttpRequest are
// built on top of it in the prelude, which passes the request's mode/credentials
// so the Go layer can enforce CORS (see cors.go). The returned promise carries an
// abort() method that cancels the in-flight request (XMLHttpRequest.abort). It is
// only installed when a Transport is available; otherwise fetch/XHR keep their
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
		mode := "cors"
		if a := call.Argument(4); !goja.IsUndefined(a) && !goja.IsNull(a) && a.String() != "" {
			mode = a.String()
		}
		credentials := "same-origin"
		if a := call.Argument(5); !goja.IsUndefined(a) && !goja.IsNull(a) && a.String() != "" {
			credentials = a.String()
		}
		var timeout time.Duration
		if a := call.Argument(6); !goja.IsUndefined(a) && !goja.IsNull(a) {
			if ms := a.ToInteger(); ms > 0 {
				timeout = time.Duration(ms) * time.Millisecond
			}
		}
		return b.fetchPromise(method, rawURL, headers, body, mode, credentials, timeout)
	})
}

// fetchPromise returns a Promise that settles when the (off-loop) HTTP request
// completes. A JS-level setTimeout keepalive holds the event loop open while the
// request is in flight (its jobCount increment is synchronous on-loop, unlike the
// native SetTimeout). We resolve/reject BEFORE clearing the keepalive so any
// continuation that starts another fetch acquires its own keepalive first.
//
// A positive timeout (XMLHttpRequest.timeout) and the promise's abort() method
// both act on the request's context, so an expired or aborted request still
// settles through the one RunOnLoop below: neither adds a timer or any other
// async primitive, and the pending/keepalive bracket (ADR 0004) is unchanged.
func (b *bridge) fetchPromise(method, rawURL string, headers map[string]string, body []byte, mode, credentials string, timeout time.Duration) goja.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()

	abs, err := b.resolveURL(rawURL)
	if err != nil {
		_ = reject(vm.ToValue("fetch: " + err.Error()))
		return vm.ToValue(promise)
	}
	// CSP connect-src gates fetch/XHR on-loop (a pure URL check; recordError is only
	// loop-safe here, not in the off-loop goroutine below). A browser does not
	// dispatch a blocked connection; report-only records but proceeds. See csp.go.
	if !b.cspAllowConnect(abs) {
		_ = reject(vm.ToValue("fetch: blocked by Content-Security-Policy: connect-src"))
		return vm.ToValue(promise)
	}

	b.pending.Add(1) // off-loop request in flight; bracketed by the keepalive window
	keep := b.acquireKeepalive()
	// budgetCtx is bounded by the render budget; ctx additionally by the caller's
	// timeout, so "ctx expired while budgetCtx is live" identifies that timeout.
	budgetCtx, cancel := context.WithTimeout(xhrCtx(b.ctx), b.reqTimeout)
	ctx, cancelTimeout := budgetCtx, context.CancelFunc(func() {})
	if timeout > 0 {
		ctx, cancelTimeout = context.WithTimeout(budgetCtx, timeout)
	}
	go func() {
		defer cancel()
		defer cancelTimeout()
		// doFetch applies the CORS policy over untrusted page JS and issues the
		// request(s) through the transport (a cross-origin preflight, when needed,
		// reuses this same pending/keepalive bracket — no new async primitive, so
		// the ADR 0004 settle invariant holds).
		res, respType, ferr := b.doFetch(ctx, method, abs, headers, body, mode, credentials)
		timedOut := ferr != nil && timeout > 0 && ctx.Err() == context.DeadlineExceeded && budgetCtx.Err() == nil
		_ = b.loop.RunOnLoop(func(vm *goja.Runtime) {
			switch {
			case timedOut:
				_ = reject(vm.ToValue(xhrTimeoutReason))
			case ferr != nil:
				_ = reject(vm.ToValue(ferr.Error()))
			default:
				_ = resolve(b.responseObject(res, respType))
			}
			b.releaseKeepalive(keep)
			b.pending.Add(-1)
		})
		// If RunOnLoop returned false the loop was terminated; res.Body is already
		// fully read by the Transport, so there is nothing to clean up.
	}()
	pv := vm.ToValue(promise)
	// abort() cancels the request's context: the transport returns, and the
	// goroutine above rejects the promise and closes the bracket as usual.
	_ = pv.ToObject(vm).Set("abort", func(goja.FunctionCall) goja.Value {
		cancel()
		return goja.Undefined()
	})
	return pv
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

func (b *bridge) responseObject(r *Response, respType string) goja.Value {
	vm := b.vm
	o := vm.NewObject()
	_ = o.Set("status", r.Status)
	_ = o.Set("ok", r.Status >= 200 && r.Status < 300)
	_ = o.Set("statusText", r.StatusText)
	_ = o.Set("url", r.FinalURL)
	// type is the Fetch response type ("basic"/"cors"/"opaque"); the prelude copies
	// it onto Response.type so page code sees an opaque cross-origin read as opaque.
	_ = o.Set("type", respType)
	_ = o.Set("body", string(r.Body))
	// bodyBytes carries the raw bytes for Response.arrayBuffer()/blob(); the
	// string body above stays the fast path for text()/json().
	_ = o.Set("bodyBytes", vm.NewArrayBuffer(r.Body))
	// rawBytes hands out the pre-transcode octets, which XHR's x-user-defined
	// decode needs (the binary-as-text idiom). It is present only when the body
	// was transcoded, and wraps r.Raw without copying, on demand.
	if raw := r.Raw; len(raw) > 0 && !sameBytes(raw, r.Body) {
		_ = o.Set("rawBytes", func(goja.FunctionCall) goja.Value {
			return vm.ToValue(vm.NewArrayBuffer(raw))
		})
	}
	// Set-Cookie and Set-Cookie2 are forbidden response-header names (Fetch
	// Standard): page script never sees them, whatever the CORS mode, or it
	// could read HttpOnly cookies. This object is the only route response
	// headers take to page JS (fetch and XHR both build on it), so they are
	// dropped here. The cookie jar is fed by the HTTP client below the
	// Transport and the requests log is operator-facing; neither reads this.
	h := vm.NewObject()
	for k, v := range r.Headers {
		k = strings.ToLower(k)
		if k == "set-cookie" || k == "set-cookie2" {
			continue
		}
		_ = h.Set(k, v)
	}
	_ = o.Set("headers", h)
	return o
}

// sameBytes reports whether a and b are the same slice (same backing array and
// length), without comparing contents.
func sameBytes(a, b []byte) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
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
