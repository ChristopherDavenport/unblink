package js

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// CORS enforcement over untrusted page JavaScript, following the Fetch spec's
// cross-origin rules. It runs in the Go layer (doFetch, called from fetchPromise)
// — ABOVE the transport, so every request (including a preflight and a blocked or
// opaque one) is still issued through b.transport and therefore still logged in
// the requests diagnostics. The layer decides only what the page's fetch()/
// XMLHttpRequest may READ; the operator/agent, a higher trust tier, keeps full
// visibility of the traffic via the requests tool. See ADR 0011.
//
// This is a security posture, not fidelity: unblink runs the page's own code, so
// it must not let that code exfiltrate a cross-origin response the same-origin
// policy would deny. Cross-origin <script>/module loads are NOT restricted here
// (a browser allows them); their integrity is governed by SRI (sri.go).

// corsOmitCredentialsCtxKey marks a request context whose credentials (cookies)
// must be withheld — a cross-origin request whose credentials mode is not
// "include". The browser's shared JS RoundTripper honors it (cookie-strip
// decorator) so a non-credentialed cross-origin request never carries the
// session's cookies.
type corsOmitCredentialsCtxKey struct{}

// WithOmitCredentials marks ctx so the shared JS RoundTripper strips the Cookie
// header before dispatch.
func WithOmitCredentials(ctx context.Context) context.Context {
	return context.WithValue(ctx, corsOmitCredentialsCtxKey{}, true)
}

// OmitCredentials reports whether ctx was marked by WithOmitCredentials.
func OmitCredentials(ctx context.Context) bool {
	v, _ := ctx.Value(corsOmitCredentialsCtxKey{}).(bool)
	return v
}

// isHTTPScheme reports whether CORS applies to a target of this scheme. Only
// http/https are policed; chrome-extension://, data:, blob: etc. are left to
// their own handlers (extension resources, inline data).
func isHTTPScheme(scheme string) bool {
	s := strings.ToLower(scheme)
	return s == "http" || s == "https"
}

// schemeDefaultPort is the default port for a scheme, "" if unknown.
func schemeDefaultPort(scheme string) string {
	switch strings.ToLower(scheme) {
	case "https", "wss":
		return "443"
	case "http", "ws":
		return "80"
	}
	return ""
}

// port returns u's explicit port or the scheme default.
func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return schemeDefaultPort(u.Scheme)
}

// sameOrigin reports whether a and b share scheme, host, and (default-normalized)
// port — the Fetch "same origin" definition.
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		port(a) == port(b)
}

// originString renders u as an ASCII origin "scheme://host[:port]" for the Origin
// request header, including the port only when non-default.
func originString(u *url.URL) string {
	if u == nil {
		return "null"
	}
	host := u.Hostname()
	if p := u.Port(); p != "" && p != schemeDefaultPort(u.Scheme) {
		host = host + ":" + p
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

// isCORSSafelistedContentType reports whether a Content-Type value keeps a request
// "simple" (no preflight).
func isCORSSafelistedContentType(v string) bool {
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "application/x-www-form-urlencoded", "multipart/form-data", "text/plain":
		return true
	}
	return false
}

// isCORSSafelistedHeader reports whether a request header may appear on a simple
// cross-origin request without triggering a preflight.
func isCORSSafelistedHeader(name, value string) bool {
	switch strings.ToLower(name) {
	case "accept", "accept-language", "content-language":
		return true
	case "content-type":
		return isCORSSafelistedContentType(value)
	}
	return false
}

// isSimpleRequest reports whether a cross-origin request needs no CORS preflight:
// a safelisted method carrying only safelisted headers.
func isSimpleRequest(method string, headers map[string]string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "POST":
	default:
		return false
	}
	for k, v := range headers {
		if !isCORSSafelistedHeader(k, v) {
			return false
		}
	}
	return true
}

// parseCommaList splits an HTTP list header into trimmed, non-empty tokens.
func parseCommaList(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// containsToken reports whether list holds tok (case-insensitively).
func containsToken(list []string, tok string) bool {
	for _, t := range list {
		if strings.EqualFold(t, tok) {
			return true
		}
	}
	return false
}

// validateActualCORS reports whether a cross-origin response's CORS headers let
// the page read it, for the given request origin and credentials mode.
func validateActualCORS(res *Response, origin string, credentialed bool) bool {
	if res == nil {
		return false
	}
	allow := strings.TrimSpace(res.Headers["access-control-allow-origin"])
	if allow == "" {
		return false
	}
	if credentialed {
		// A credentialed request forbids the wildcard and requires the credentials flag.
		if allow == "*" {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(res.Headers["access-control-allow-credentials"]), "true") {
			return false
		}
		return strings.EqualFold(allow, origin)
	}
	if allow == "*" {
		return true
	}
	return strings.EqualFold(allow, origin)
}

// methodAllowed reports whether the actual request method is authorized by a
// preflight's Access-Control-Allow-Methods (a CORS-safelisted method always is).
func methodAllowed(allowMethods, method string, credentialed bool) bool {
	list := parseCommaList(allowMethods)
	if !credentialed && containsToken(list, "*") {
		return true
	}
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "POST":
		return true
	}
	return containsToken(list, method)
}

// validatePreflight reports whether an OPTIONS preflight response authorizes the
// actual cross-origin request — its origin/credentials, method, and any
// non-safelisted headers.
func validatePreflight(res *Response, origin, method string, reqHeaders map[string]string, credentialed bool) bool {
	if res == nil || res.Status >= 400 {
		return false
	}
	if !validateActualCORS(res, origin, credentialed) {
		return false
	}
	if !methodAllowed(res.Headers["access-control-allow-methods"], method, credentialed) {
		return false
	}
	allowHdrs := parseCommaList(res.Headers["access-control-allow-headers"])
	wildcardHdr := !credentialed && containsToken(allowHdrs, "*")
	for k, v := range reqHeaders {
		if isCORSSafelistedHeader(k, v) || strings.EqualFold(k, "origin") {
			continue
		}
		if wildcardHdr {
			continue
		}
		if !containsToken(allowHdrs, k) {
			return false
		}
	}
	return true
}

// preflightRequestHeaders is the sorted, comma-separated, lowercased list of the
// request's non-safelisted header names, for Access-Control-Request-Headers.
func preflightRequestHeaders(headers map[string]string) string {
	var names []string
	for k, v := range headers {
		if isCORSSafelistedHeader(k, v) {
			continue
		}
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// opaqueResponse strips a no-cors response to what the page may observe: an opaque
// response is unreadable (status 0, empty body/url, no headers).
func opaqueResponse() *Response {
	return &Response{Status: 0, Headers: map[string]string{}}
}

// doFetch issues a fetch/XHR request under the CORS policy and returns the
// response plus its Fetch response type ("basic"/"cors"/"opaque"). A non-nil
// error means the page's promise must reject (a browser network error). Every
// request is sent through b.transport (so it lands in the requests log) except
// where the spec forbids sending (credentials mode "same-origin", cross-origin).
func (b *bridge) doFetch(ctx context.Context, method, abs string, headers map[string]string, body []byte, mode, credentials string) (*Response, string, error) {
	target, err := url.Parse(abs)
	if err != nil {
		return nil, "", err
	}
	// Same-origin (or enforcement disabled, or a non-HTTP scheme) → today's path.
	if b.allowCrossOrigin || b.base == nil || !isHTTPScheme(target.Scheme) || sameOrigin(target, b.base) {
		res, ferr := b.transport.Do(ctx, method, abs, headers, body)
		if ferr != nil {
			return nil, "", ferr
		}
		// A same-origin request that REDIRECTED cross-origin (res.FinalURL differs in
		// origin) must not be read as same-origin — otherwise a same-origin endpoint
		// that 302s to an attacker origin would launder a cross-origin read past SOP.
		// Re-validate the final response's CORS (final-hop; per-hop remains a
		// documented approximation). The cross-origin hop already dropped the session
		// cookies, matching the credentialed=false validation. Only when enforcement
		// is on and the initial target was a same-origin HTTP(S) URL.
		if !b.allowCrossOrigin && b.base != nil && isHTTPScheme(target.Scheme) && res != nil && res.FinalURL != "" {
			if fin, perr := url.Parse(res.FinalURL); perr == nil && isHTTPScheme(fin.Scheme) && !sameOrigin(fin, b.base) {
				if !validateActualCORS(res, originString(b.base), false) {
					return nil, "", fmt.Errorf("fetch: blocked by CORS: request redirected cross-origin to %s without a matching Access-Control-Allow-Origin", res.FinalURL)
				}
				return res, "cors", nil
			}
		}
		return res, "basic", nil
	}

	origin := originString(b.base)
	credentialed := strings.EqualFold(credentials, "include")
	reqCtx := ctx
	if !credentialed {
		// A non-credentialed cross-origin request must not carry session cookies.
		reqCtx = WithOmitCredentials(ctx)
	}
	// Copy headers so adding Origin doesn't mutate the caller's map.
	h := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		h[k] = v
	}
	h["Origin"] = origin

	switch strings.ToLower(mode) {
	case "same-origin":
		// The page forbade cross-origin; a browser rejects without sending.
		return nil, "", fmt.Errorf("fetch: blocked by CORS: cross-origin request in 'same-origin' mode: %s", abs)
	case "no-cors":
		switch strings.ToUpper(method) {
		case "GET", "HEAD", "POST":
		default:
			return nil, "", fmt.Errorf("fetch: 'no-cors' mode does not allow method %q", method)
		}
		res, ferr := b.transport.Do(reqCtx, method, abs, h, body)
		if ferr != nil {
			return nil, "", ferr
		}
		// Sent (and logged), but the page may not read it.
		_ = res
		return opaqueResponse(), "opaque", nil
	default: // "cors"
		if !isSimpleRequest(method, headers) {
			pfHeaders := map[string]string{
				"Origin":                        origin,
				"Access-Control-Request-Method": strings.ToUpper(method),
			}
			if rh := preflightRequestHeaders(headers); rh != "" {
				pfHeaders["Access-Control-Request-Headers"] = rh
			}
			pf, pferr := b.transport.Do(reqCtx, "OPTIONS", abs, pfHeaders, nil)
			if pferr != nil {
				return nil, "", pferr
			}
			if !validatePreflight(pf, origin, method, headers, credentialed) {
				return nil, "", fmt.Errorf("fetch: blocked by CORS: preflight did not allow the request: %s", abs)
			}
		}
		res, ferr := b.transport.Do(reqCtx, method, abs, h, body)
		if ferr != nil {
			return nil, "", ferr
		}
		if !validateActualCORS(res, origin, credentialed) {
			return nil, "", fmt.Errorf("fetch: blocked by CORS: no matching Access-Control-Allow-Origin: %s", abs)
		}
		return res, "cors", nil
	}
}
