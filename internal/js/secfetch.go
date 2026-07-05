package js

import (
	"context"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// Sec-Fetch metadata (Fetch Metadata Request Headers). unblink attaches truthful
// Sec-Fetch-Site/Mode/Dest to every page-JS subrequest, matching what a real
// browser sends, so origins that gate on them (resource-isolation policies, some
// anti-bot) see a coherent request context. These are FORBIDDEN header names:
// untrusted page JS must not set or override them, so the decorator strips any
// incoming Sec-Fetch-* and sets canonical values. The primary document's
// Sec-Fetch (document/navigate/none/?1) is set in internal/fetch. See ADR 0014.

// secFetchTransport injects Sec-Fetch-* on outgoing subrequests, computing the
// values from the request's resource type (reqTypeFrom) and the page origin. It
// sits just inside countingTransport (so header injection doesn't perturb the
// request log) and is otherwise a pass-through.
type secFetchTransport struct {
	inner Transport
	base  *url.URL // the document origin, for Sec-Fetch-Site
}

func (t *secFetchTransport) Do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (*Response, error) {
	return t.inner.Do(ctx, method, rawURL, secFetchHeaders(reqTypeFrom(ctx), t.base, rawURL, headers), body)
}

// ResetBudget forwards to the inner transport's budget reset, if any (the
// per-dispatch budget lives further down the chain).
func (t *secFetchTransport) ResetBudget() {
	if r, ok := t.inner.(interface{ ResetBudget() }); ok {
		r.ResetBudget()
	}
}

// secFetchHeaders returns a copy of headers with any page-supplied Sec-Fetch-*
// stripped (forbidden headers) and the canonical Dest/Mode/Site set. Sec-Fetch-User
// is never added to a subrequest (it applies to user-activated navigations only).
//
// Mode is an approximation: a classic <script> is no-cors and fetch/XHR is cors,
// but ES-module fetches (also tagged TypeScript) are really cors and a no-cors
// beacon (TypeXHR) is really no-cors. The common cases are correct; the rest is a
// documented fidelity gap (ADR 0014).
func secFetchHeaders(rt webext.ResourceType, base *url.URL, rawURL string, headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers)+3)
	for k, v := range headers {
		if strings.HasPrefix(strings.ToLower(k), "sec-fetch-") {
			continue // strip any page-JS attempt to spoof a forbidden header
		}
		out[k] = v
	}
	dest, mode := "empty", "cors"
	if rt == webext.TypeScript {
		dest, mode = "script", "no-cors"
	}
	out["Sec-Fetch-Dest"] = dest
	out["Sec-Fetch-Mode"] = mode
	out["Sec-Fetch-Site"] = secFetchSite(base, rawURL)
	return out
}

// secFetchSite computes the Sec-Fetch-Site relationship between the page origin
// and the request target: same-origin (scheme+host+port), same-site (registrable
// domain + scheme), or cross-site. "none" is reserved for top-level navigations.
func secFetchSite(base *url.URL, rawURL string) string {
	if base == nil {
		return "same-origin"
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return "cross-site"
	}
	if sameOrigin(target, base) {
		return "same-origin"
	}
	bd := registrableDomain(base.Hostname())
	if bd != "" && strings.EqualFold(target.Scheme, base.Scheme) && registrableDomain(target.Hostname()) == bd {
		return "same-site"
	}
	return "cross-site"
}

// registrableDomain returns the eTLD+1 for host, or "" when it can't be determined
// (bare IPs, unlisted TLDs) so those never match as same-site.
func registrableDomain(host string) string {
	d, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return ""
	}
	return d
}
