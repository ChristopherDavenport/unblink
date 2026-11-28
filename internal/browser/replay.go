package browser

import (
	"net/http"
	"time"
)

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

// WithPageClock pins the clock every page rendered by this Browser sees: its
// Date and performance time origin start at t and advance in real time (see
// js.Env.ClockStart). Like WithJSRoundTripper it exists for offline replay
// harnesses — a recorded page computes "3 hours ago" against the moment it was
// recorded — and is not wired to any flag.
func WithPageClock(t time.Time) Option {
	return func(o *options) { o.pageClock = t }
}
