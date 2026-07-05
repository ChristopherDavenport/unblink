package js

import (
	"net/http"
	"net/url"
	"strings"
)

// Cross-origin isolation fidelity. unblink exposes a truthful window.isSecureContext
// and window.crossOriginIsolated so page code that branches on them (WebCrypto,
// high-resolution timers, feature detection) takes its real path. It does NOT
// enforce COEP subresource embedding (require-corp) — this is a fidelity signal,
// not a capability gate, and SharedArrayBuffer stays deliberately absent. COOP's
// opener severance is already moot (window.open/opener are null). See ADR 0014.

// installIsolationGlobals injects the resolved secure-context / cross-origin-isolated
// state so the prelude can expose them on window (== self == globalThis). Values
// are constants for the render, computed from the document URL and its response
// headers; the prelude reads __unblinkSecureContext / __unblinkCrossOriginIsolated.
func (b *bridge) installIsolationGlobals() {
	_ = b.vm.Set("__unblinkSecureContext", isSecureContext(b.base))
	_ = b.vm.Set("__unblinkCrossOriginIsolated", crossOriginIsolated(b.respHeaders, b.base))
}

// isSecureContext reports whether the document is a secure context, per the parts
// of the browser definition unblink can model: an https/wss origin, or a loopback
// host (localhost / *.localhost / 127.0.0.1 / ::1).
func isSecureContext(base *url.URL) bool {
	if base == nil {
		return false
	}
	switch strings.ToLower(base.Scheme) {
	case "https", "wss":
		return true
	}
	host := strings.ToLower(base.Hostname())
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "127.0.0.1" || host == "::1"
}

// crossOriginIsolated reports whether the document is cross-origin isolated: a
// secure context that opted in via Cross-Origin-Opener-Policy: same-origin AND
// Cross-Origin-Embedder-Policy: require-corp (or credentialless).
func crossOriginIsolated(headers http.Header, base *url.URL) bool {
	if !isSecureContext(base) || headers == nil {
		return false
	}
	if headerDirective(headers.Get("Cross-Origin-Opener-Policy")) != "same-origin" {
		return false
	}
	coep := headerDirective(headers.Get("Cross-Origin-Embedder-Policy"))
	return coep == "require-corp" || coep == "credentialless"
}

// headerDirective lowercases a header value and returns the token before any ";"
// parameters (e.g. `same-origin; report-to="x"` → `same-origin`).
func headerDirective(v string) string {
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}
