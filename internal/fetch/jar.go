package fetch

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// trackingJar wraps an http.CookieJar to remember which cookies were set with the
// HttpOnly attribute. The stdlib net/http/cookiejar discards HttpOnly on read — its
// Cookies method returns only Name=Value — so without this a server-set HttpOnly
// session cookie would be readable by page JavaScript through document.cookie,
// contrary to real-browser behavior. All storage and public-suffix scoping stays in
// the wrapped jar; this decorator only records HttpOnly cookie names by scope so the
// document.cookie bridge can hide them.
type trackingJar struct {
	http.CookieJar
	mu       sync.Mutex
	httpOnly map[string]map[string]bool // scope domain -> set of HttpOnly cookie names
}

// wrapTrackingJar wraps inner unless it is nil or already a *trackingJar (clients
// share one jar via Client.Jar(), so re-wrapping must be idempotent).
func wrapTrackingJar(inner http.CookieJar) http.CookieJar {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(*trackingJar); ok {
		return inner
	}
	return &trackingJar{CookieJar: inner, httpOnly: make(map[string]map[string]bool)}
}

// cookieScope is the domain a cookie applies to: its explicit Domain attribute
// (leading dot stripped) or, when host-only, the request host.
func cookieScope(u *url.URL, ck *http.Cookie) string {
	d := strings.TrimPrefix(strings.ToLower(ck.Domain), ".")
	if d == "" {
		d = strings.ToLower(u.Hostname())
	}
	return d
}

func (j *trackingJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	for _, ck := range cookies {
		scope := cookieScope(u, ck)
		names := j.httpOnly[scope]
		if ck.HttpOnly {
			if names == nil {
				names = make(map[string]bool)
				j.httpOnly[scope] = names
			}
			names[ck.Name] = true
		} else if names != nil {
			delete(names, ck.Name) // re-set without HttpOnly makes it visible again
		}
	}
	j.mu.Unlock()
	j.CookieJar.SetCookies(u, cookies)
}

// IsHTTPOnly reports whether a cookie named name, scoped to host, was set HttpOnly
// (and so must be hidden from document.cookie). It matches host-only and domain
// cookies the same way the jar does on read.
func (j *trackingJar) IsHTTPOnly(host, name string) bool {
	host = strings.ToLower(host)
	j.mu.Lock()
	defer j.mu.Unlock()
	for scope, names := range j.httpOnly {
		if names[name] && (host == scope || strings.HasSuffix(host, "."+scope)) {
			return true
		}
	}
	return false
}
