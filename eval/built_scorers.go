//go:build eval

package eval

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Scorers for the built-from-source framework fixtures (built_cases.go). They
// assert what the toy single-file fixtures cannot: that a real toolchain's
// output ran without a script error, that the engine fetched what a browser
// would and nothing more, and that nothing left the fixture's own origin.

// requestsWire mirrors the requests tool's structured output.
type requestsWire struct {
	Count     int               `json:"count"`
	Truncated bool              `json:"truncated,omitempty"`
	Requests  []page.NetRequest `json:"requests"`
}

// jsErrorsAt returns the js_errors a read or interact step reported.
func jsErrorsAt(tr *Transcript, at int) ([]string, string, bool) {
	r, why, ok := resultAt(tr, at)
	if !ok {
		return nil, why, false
	}
	if r.sr.Result.IsError {
		return nil, "step returned a tool error: " + r.text(), false
	}
	if r.sr.Tool == "interact" {
		var ir browser.InteractResult
		if err := into(r.sr.Result, &ir); err != nil {
			return nil, "decode interact: " + err.Error(), false
		}
		return ir.JSErrors, "", true
	}
	var rr browser.ReadResult
	if err := into(r.sr.Result, &rr); err != nil {
		return nil, "decode read: " + err.Error(), false
	}
	return rr.JSErrors, "", true
}

// NoJSErrors asserts a rendered read (or an interact) recorded no uncaught
// script error. A framework that logs a hydration mismatch, a missing API or a
// failed chunk load does it here; content can look right while the app is dead.
func NoJSErrors(at int) Scorer {
	return Scorer{Name: "no-js-errors", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		errs, why, ok := jsErrorsAt(tr, at)
		if !ok {
			return 0, why
		}
		if len(errs) > 0 {
			return 0, fmt.Sprintf("%d js error(s): %s", len(errs), clip(strings.Join(errs, " | "), 300))
		}
		return 1, "no js errors"
	}}
}

// JSErrorsOnly asserts every recorded script error contains one of allowed, and
// that at least one was recorded. It is for a case that provokes a failure on
// purpose (a 404 chunk) and must see that failure and no other.
func JSErrorsOnly(at int, allowed ...string) Scorer {
	return Scorer{Name: "js-errors-only", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		errs, why, ok := jsErrorsAt(tr, at)
		if !ok {
			return 0, why
		}
		for _, e := range errs {
			found := false
			for _, a := range allowed {
				if strings.Contains(e, a) {
					found = true
					break
				}
			}
			if !found {
				return 0, "unexpected js error: " + clip(e, 300)
			}
		}
		return 1, fmt.Sprintf("%d js error(s), all expected", len(errs))
	}}
}

// RenderedNotDiscarded asserts the read returned the JavaScript-rendered DOM of
// a page that settled: the collapse guard (ADR 0023) did not swap in the server
// HTML and the render did not run into its wall-clock budget. Without it a
// crashed or spinning app can pass on its prerendered text.
func RenderedNotDiscarded(at int) Scorer {
	return Scorer{Name: "rendered-not-discarded", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var rr browser.ReadResult
		if err := into(r.sr.Result, &rr); err != nil {
			return 0, "decode read: " + err.Error()
		}
		if rr.RenderDiscarded {
			return 0, "render_discarded=true"
		}
		if rr.RenderBudgetHit {
			return 0, "render_budget_hit=true: the page never settled"
		}
		return 1, "rendered, settled, not discarded"
	}}
}

// requestsAt decodes a requests step.
func requestsAt(tr *Transcript, at int) (*requestsWire, string, bool) {
	r, why, ok := resultAt(tr, at)
	if !ok {
		return nil, why, false
	}
	if r.sr.Result.IsError {
		return nil, "requests returned a tool error: " + r.text(), false
	}
	var out requestsWire
	if err := into(r.sr.Result, &out); err != nil {
		return nil, "decode requests: " + err.Error(), false
	}
	return &out, "", true
}

// Requested asserts what the page's JavaScript fetched, from the requests tool:
// every `want` substring appears in some successful request URL, and no `never`
// substring appears in any request URL at all. `never` is how a case proves a
// lazily imported chunk the page did not use was not fetched.
func Requested(at int, want, never []string) Scorer {
	return Scorer{Name: "requested", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		out, why, ok := requestsAt(tr, at)
		if !ok {
			return 0, why
		}
		matched := 0
		var missing, forbidden []string
		for _, w := range want {
			found := false
			for _, rq := range out.Requests {
				if strings.Contains(rq.URL, w) && !rq.Failed && rq.Status < 400 {
					found = true
					break
				}
			}
			if found {
				matched++
			} else {
				missing = append(missing, w)
			}
		}
		for _, n := range never {
			hit := false
			for _, rq := range out.Requests {
				if strings.Contains(rq.URL, n) {
					hit = true
					break
				}
			}
			if hit {
				forbidden = append(forbidden, n)
			} else {
				matched++
			}
		}
		detail := fmt.Sprintf("%d request(s)", len(out.Requests))
		if len(missing) > 0 {
			detail += "; not fetched: " + strings.Join(missing, ", ")
		}
		if len(forbidden) > 0 {
			detail += "; fetched but must not be: " + strings.Join(forbidden, ", ")
		}
		return frac(matched, len(want)+len(never)), detail
	}}
}

// RequestsStayOnFixture asserts the eval is offline: every request the page made
// went to the case's own httptest origin. A build that reaches for a CDN, a
// telemetry endpoint or a font host would make the gate depend on the network.
func RequestsStayOnFixture(at int) Scorer {
	return Scorer{Name: "requests-offline", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		out, why, ok := requestsAt(tr, at)
		if !ok {
			return 0, why
		}
		base, err := url.Parse(tr.Base)
		if err != nil {
			return 0, "bad base url: " + err.Error()
		}
		if out.Truncated {
			return 0, "request log truncated; cannot prove every request stayed local"
		}
		for _, rq := range out.Requests {
			u, err := url.Parse(rq.URL)
			if err != nil {
				return 0, "unparseable request url " + rq.URL
			}
			if u.Scheme == "data" || u.Scheme == "blob" || u.Scheme == "about" {
				continue
			}
			if u.Host != base.Host {
				return 0, "request left the fixture origin: " + clip(rq.URL, 200)
			}
		}
		return 1, fmt.Sprintf("%d request(s), all to the fixture origin", len(out.Requests))
	}}
}

// NoFailedRequests asserts no request the page made failed or answered 4xx/5xx,
// except URLs containing one of the allowed substrings.
func NoFailedRequests(at int, allowed ...string) Scorer {
	return Scorer{Name: "no-failed-requests", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		out, why, ok := requestsAt(tr, at)
		if !ok {
			return 0, why
		}
	next:
		for _, rq := range out.Requests {
			if !rq.Failed && rq.Status < 400 {
				continue
			}
			for _, a := range allowed {
				if strings.Contains(rq.URL, a) {
					continue next
				}
			}
			return 0, fmt.Sprintf("failed request %s (status %d %s)", clip(rq.URL, 160), rq.Status, rq.Err)
		}
		return 1, fmt.Sprintf("%d request(s), none failed", len(out.Requests))
	}}
}

// FailedRequest asserts the request log shows a request whose URL contains sub
// answered with status: a failure the page hit must be visible to the agent.
func FailedRequest(at int, sub string, status int) Scorer {
	return Scorer{Name: "failed-request", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		out, why, ok := requestsAt(tr, at)
		if !ok {
			return 0, why
		}
		for _, rq := range out.Requests {
			if strings.Contains(rq.URL, sub) {
				if rq.Status == status {
					return 1, fmt.Sprintf("%s -> %d", sub, status)
				}
				return 0, fmt.Sprintf("%s -> %d, want %d", sub, rq.Status, status)
			}
		}
		return 0, "no request for " + sub
	}}
}

// clip shortens s for a one-line scorer detail.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
