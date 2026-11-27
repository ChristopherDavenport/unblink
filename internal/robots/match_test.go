package robots_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/robots"
)

// Match reports the deciding rule; MatchNormalized compares after percent-
// encoding normalization (RFC 9309 section 2.2.2), so an equivalent spelling of
// a path cannot slip past a rule.
func TestMatchAndNormalize(t *testing.T) {
	p := robots.Parse([]byte("User-agent: *\nDisallow: /private\nAllow: /private/public\n" +
		"Disallow: /café\nDisallow: /a%2fb\nDisallow: /x%7Ey$\n"))
	g := p.StarGroup()

	rule, allowed := g.Match("/private/doc")
	if allowed || rule == nil || rule.Pattern != "/private" || rule.Allow {
		t.Errorf("Match(/private/doc) = %+v, %v", rule, allowed)
	}
	rule, allowed = g.Match("/private/public/doc")
	if !allowed || rule == nil || rule.Pattern != "/private/public" {
		t.Errorf("Match(/private/public/doc) = %+v, %v", rule, allowed)
	}
	if rule, allowed := g.Match("/open"); !allowed || rule != nil {
		t.Errorf("Match(/open) = %+v, %v; want no rule, allowed", rule, allowed)
	}
	var none *robots.Group
	if rule, allowed := none.MatchNormalized("/anything"); !allowed || rule != nil {
		t.Errorf("nil group = %+v, %v", rule, allowed)
	}

	for _, tc := range []struct {
		path       string
		plain      bool // Allowed (path compared as written)
		normalized bool // MatchNormalized
	}{
		{"/private/doc", false, false},
		{"/%70rivate/doc", true, false},        // %70 = 'p': an encoded unreserved char
		{"/private/%70ublic/doc", false, true}, // the Allow carve-out, re-spelled
		{"/caf%C3%A9", true, false},            // the pattern is written in raw UTF-8
		{"/caf%c3%a9", true, false},            // hex case is not significant
		{"/a%2Fb", true, false},                // reserved escape: only hex case differs
		{"/a/b", true, true},                   // ...but %2F never equals a real slash
		{"/x~y", true, false},                  // the pattern escaped an unreserved char
		{"/x~yz", true, true},                  // '$' still anchors
	} {
		if got := g.Allowed(tc.path); got != tc.plain {
			t.Errorf("Allowed(%q) = %v, want %v", tc.path, got, tc.plain)
		}
		if _, got := g.MatchNormalized(tc.path); got != tc.normalized {
			t.Errorf("MatchNormalized(%q) allowed = %v, want %v", tc.path, got, tc.normalized)
		}
	}

	for in, want := range map[string]string{
		"/a b":         "/a%20b",
		"/%7euser":     "/~user",
		"/%3fq=%2a":    "/%3Fq=%2A",
		"/100%":        "/100%25",
		"/bad%zzend":   "/bad%25zzend",
		"/%%341":       "/%2541",
		"/*.gif$":      "/*.gif$",
		"/café":        "/caf%C3%A9",
		"/p?x=1&y=%41": "/p?x=1&y=A",
	} {
		if got := robots.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
