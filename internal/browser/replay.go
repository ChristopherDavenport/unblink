package browser

import "net/http"

// WithJSRoundTripper replaces the shared connection pool that every page-JS
// subrequest client (one-shot renders and live sessions) sends through.
//
// It exists for offline replay harnesses (the `corpus` package, ADR 0022) that
// serve recorded responses instead of touching the network; pair it with
// WithFetchOptions(fetch.WithSharedTransport(rt)) to cover page fetches too.
// It is test-only in intent and not wired to any flag: rt REPLACES the pool that
// carries the SSRF dial guard, so a caller passing a real network transport
// takes on that guard itself. CORS cookie suppression (js.WithOmitCredentials)
// still applies in front of rt.
func WithJSRoundTripper(rt http.RoundTripper) Option {
	return func(o *options) { o.jsRoundTripper = rt }
}
