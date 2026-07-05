package js

import (
	"net/http"
	"net/url"
	"testing"
)

func TestParseCSPSource(t *testing.T) {
	cases := []struct {
		in   string
		kind cspSourceKind
		ok   bool
	}{
		{"'self'", srcKeyword, true},
		{"'unsafe-inline'", srcKeyword, true},
		{"'nonce-abc123'", srcNonce, true},
		{"'sha256-YWJj'", srcHash, true},
		{"https:", srcScheme, true},
		{"data:", srcScheme, true},
		{"https://cdn.example.com", srcHost, true},
		{"*.example.com", srcHost, true},
		{"*", srcHost, true},
		{"", srcKeyword, false}, // empty → rejected (a lone garbage token becomes a never-matching host)
	}
	for _, c := range cases {
		s, ok := parseCSPSource(c.in)
		if ok != c.ok {
			t.Errorf("parseCSPSource(%q) ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		if ok && s.kind != c.kind {
			t.Errorf("parseCSPSource(%q) kind=%v want %v", c.in, s.kind, c.kind)
		}
	}
}

func TestCSPHostMatches(t *testing.T) {
	u := func(s string) *url.URL { x, _ := url.Parse(s); return x }
	cases := []struct {
		src    string
		target string
		want   bool
	}{
		{"https://cdn.example.com", "https://cdn.example.com/a.js", true},
		{"https://cdn.example.com", "https://other.com/a.js", false},
		{"*.example.com", "https://cdn.example.com/a.js", true},
		{"*.example.com", "https://example.com/a.js", false}, // *. requires a subdomain
		{"*", "https://anything.com/a.js", true},
		{"*", "data:text/js,x", false},                                    // bare * excludes non-network schemes
		{"https://cdn.example.com", "http://cdn.example.com/a.js", false}, // scheme mismatch
		{"cdn.example.com", "https://cdn.example.com/a.js", true},         // scheme-less matches network scheme
		{"https://cdn.example.com:8443", "https://cdn.example.com/a.js", false},
		{"https://cdn.example.com/assets/", "https://cdn.example.com/assets/x.js", true},
		{"https://cdn.example.com/assets/", "https://cdn.example.com/other/x.js", false},
	}
	for _, c := range cases {
		s, ok := parseCSPSource(c.src)
		if !ok {
			t.Fatalf("parse %q failed", c.src)
		}
		if got := cspHostMatches(s, u(c.target)); got != c.want {
			t.Errorf("cspHostMatches(%q, %q)=%v want %v", c.src, c.target, got, c.want)
		}
	}
}

func TestCSPExternalScriptMatching(t *testing.T) {
	base, _ := url.Parse("https://page.com/x")
	other, _ := url.Parse("https://cdn.other.com/a.js")
	self, _ := url.Parse("https://page.com/a.js")

	p := parseCSPPolicy("script-src 'self'")
	if !p.allowsExternalScript(base, self, "", true) {
		t.Error("'self' should allow same-origin script")
	}
	if p.allowsExternalScript(base, other, "", true) {
		t.Error("'self' should block cross-origin script")
	}

	// strict-dynamic: host/self ignored; parser-inserted blocked, script-inserted allowed.
	sd := parseCSPPolicy("script-src 'nonce-n1' 'strict-dynamic'")
	if sd.allowsExternalScript(base, other, "", true) {
		t.Error("strict-dynamic should block a parser-inserted script without nonce")
	}
	if !sd.allowsExternalScript(base, other, "", false) {
		t.Error("strict-dynamic should allow a script-inserted script")
	}
	if !sd.allowsExternalScript(base, other, "n1", true) {
		t.Error("matching nonce should allow even under strict-dynamic")
	}
}

func TestCSPInlineMatching(t *testing.T) {
	base, _ := url.Parse("https://page.com/x")

	// 'unsafe-inline' allows inline...
	if !parseCSPPolicy("script-src 'unsafe-inline'").allowsInlineScript(base, "x=1", "") {
		t.Error("'unsafe-inline' should allow inline")
	}
	// ...but is ignored when a nonce source is present.
	p := parseCSPPolicy("script-src 'unsafe-inline' 'nonce-n1'")
	if p.allowsInlineScript(base, "x=1", "") {
		t.Error("'unsafe-inline' must be ignored when a nonce-source is present")
	}
	if !p.allowsInlineScript(base, "x=1", "n1") {
		t.Error("matching nonce should allow inline")
	}
}

func TestCSPBlocksEval(t *testing.T) {
	cases := map[string]bool{
		"script-src 'self'":               true,  // no unsafe-eval → blocked
		"script-src 'self' 'unsafe-eval'": false, // present → allowed
		"default-src 'self'":              true,  // falls back to default-src
		"img-src 'self'":                  false, // no script-src/default-src → not restricted
		"connect-src 'self'":              false,
	}
	for policy, want := range cases {
		if got := parseCSPPolicy(policy).blocksEval(); got != want {
			t.Errorf("blocksEval(%q)=%v want %v", policy, got, want)
		}
	}
}

func TestBuildCSP(t *testing.T) {
	h := http.Header{}
	h.Add("Content-Security-Policy", "script-src 'self'")
	h.Add("Content-Security-Policy", "connect-src 'self'")
	h.Set("Content-Security-Policy-Report-Only", "script-src 'none'")
	c := buildCSP(h, nil)
	if c == nil || len(c.enforced) != 2 || len(c.reportOnly) != 1 {
		t.Fatalf("buildCSP enforced=%d reportOnly=%d, want 2/1", len(c.enforced), len(c.reportOnly))
	}
	// No policy anywhere → nil.
	if buildCSP(http.Header{}, nil) != nil {
		t.Error("buildCSP with no policy should return nil")
	}
	// Comma-separated policies in one header → two policies.
	h2 := http.Header{}
	h2.Set("Content-Security-Policy", "script-src 'self', connect-src 'self'")
	if c2 := buildCSP(h2, nil); c2 == nil || len(c2.enforced) != 2 {
		t.Errorf("comma-separated policies: got %v", c2)
	}
}
