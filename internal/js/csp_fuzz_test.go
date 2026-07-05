package js

import (
	"net/url"
	"testing"
)

// FuzzBuildCSP feeds arbitrary, attacker-controlled CSP header strings at the
// parser and every matcher. The header is untrusted page input, so parsing and
// matching must never panic on any bytes.
func FuzzBuildCSP(f *testing.F) {
	f.Add("script-src 'self' 'unsafe-inline'")
	f.Add("default-src 'none'; connect-src https://*.example.com:8443/x/")
	f.Add("script-src 'nonce-abc' 'strict-dynamic' 'sha256-!!!'")
	f.Add("")
	f.Add("script-src ;;; ,,, '''")
	f.Add("'; :// : ://// *.*.* '")

	base, _ := url.Parse("https://page.com/here")
	target, _ := url.Parse("https://cdn.other.com/a.js")

	f.Fuzz(func(t *testing.T, header string) {
		p := parseCSPPolicy(header)
		if p == nil {
			return
		}
		// Exercise every matcher; none may panic.
		_ = p.allowsInlineScript(base, "x=1", "n")
		_ = p.allowsExternalScript(base, target, "n", true)
		_ = p.allowsExternalScript(base, target, "n", false)
		_ = p.allowsConnect(base, target)
		_ = p.blocksEval()
	})
}
