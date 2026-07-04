package webext_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// matchRule compiles a single-rule matcher from a ruleset JSON snippet.
func matcher(t *testing.T, rulesJSON string) *webext.RuleMatcher {
	t.Helper()
	rules, err := webext.ParseRules([]byte(rulesJSON))
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	m, err := webext.NewRuleMatcher(rules)
	if err != nil {
		t.Fatalf("NewRuleMatcher: %v", err)
	}
	return m
}

func req(t *testing.T, rawURL string, typ webext.ResourceType, initiator string) webext.Request {
	t.Helper()
	return webext.Request{URL: mustURL(t, rawURL), Method: "GET", Type: typ, Initiator: initiator}
}

func TestURLFilterMatching(t *testing.T) {
	tests := []struct {
		filter string
		url    string
		want   bool
	}{
		// domain anchor + separator
		{"||ads.example.com^", "https://ads.example.com/x.js", true},
		{"||ads.example.com^", "https://sub.ads.example.com/x", true},
		{"||ads.example.com^", "https://ads.example.com", true}, // ^ matches URL end
		{"||ads.example.com^", "https://notads.example.com/x", false},
		{"||ads.example.com^", "https://example.com/ads.example.com", false},
		// plain substring
		{"/tracker.js", "https://cdn.foo.com/a/tracker.js", true},
		{"/tracker.js", "https://cdn.foo.com/a/main.js", false},
		// start / end anchors
		{"|https://evil", "https://evil.test/x", true},
		{"|https://evil", "http://evil.test/x", false},
		{".gif|", "https://x.com/a.gif", true},
		{".gif|", "https://x.com/a.gif?q=1", false},
		// wildcard
		{"a*b.com", "https://aXYZb.com/", true},
		// case-insensitive by default
		{"||ADS.example.com^", "https://ads.example.com/x", true},
	}
	for _, tt := range tests {
		m := matcher(t, `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":`+quote(tt.filter)+`}}]`)
		got := m.Match(req(t, tt.url, webext.TypeScript, "")).Block
		if got != tt.want {
			t.Errorf("urlFilter %q vs %q: Block=%v, want %v", tt.filter, tt.url, got, tt.want)
		}
	}
}

func TestMatcherAllowOverridesBlock(t *testing.T) {
	m := matcher(t, `[
		{"id":1,"priority":1,"action":{"type":"block"},"condition":{"urlFilter":"||ads.example.com^"}},
		{"id":2,"priority":2,"action":{"type":"allow"},"condition":{"urlFilter":"||ads.example.com/ok"}}
	]`)
	if d := m.Match(req(t, "https://ads.example.com/x.js", webext.TypeScript, "")); !d.Block {
		t.Error("expected block for /x.js")
	}
	if d := m.Match(req(t, "https://ads.example.com/ok/x.js", webext.TypeScript, "")); d.Block || !d.Allow {
		t.Errorf("expected allow to override block for /ok, got %+v", d)
	}
}

func TestMatcherResourceTypes(t *testing.T) {
	m := matcher(t, `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"||t.example^","resourceTypes":["script"]}}]`)
	if !m.Match(req(t, "https://t.example/a", webext.TypeScript, "")).Block {
		t.Error("script should be blocked")
	}
	if m.Match(req(t, "https://t.example/a", webext.TypeXHR, "")).Block {
		t.Error("xhr should not be blocked (type-scoped to script)")
	}
}

func TestMatcherInitiatorAndDomainType(t *testing.T) {
	m := matcher(t, `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"||track.example^","domainType":"thirdParty"}}]`)
	// third-party page → blocked
	if !m.Match(req(t, "https://track.example/b", webext.TypeXHR, "news.test")).Block {
		t.Error("third-party request should be blocked")
	}
	// same registrable domain → first party → not blocked
	if m.Match(req(t, "https://track.example/b", webext.TypeXHR, "track.example")).Block {
		t.Error("first-party request should not be blocked")
	}

	m2 := matcher(t, `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"/beacon","initiatorDomains":["news.test"]}}]`)
	if !m2.Match(req(t, "https://cdn.x/beacon", webext.TypeXHR, "news.test")).Block {
		t.Error("initiatorDomains match should block")
	}
	if m2.Match(req(t, "https://cdn.x/beacon", webext.TypeXHR, "other.test")).Block {
		t.Error("non-listed initiator should not block")
	}
}

func TestMatcherNoMatch(t *testing.T) {
	m := matcher(t, `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"||ads.example.com^"}}]`)
	if d := m.Match(req(t, "https://safe.example.com/x", webext.TypeScript, "")); d.Block || d.Allow || d.RedirectTo != "" {
		t.Errorf("expected empty decision, got %+v", d)
	}
}

// quote JSON-encodes a string for embedding in a ruleset literal.
func quote(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b = append(b, '\\', byte(r))
		default:
			b = append(b, string(r)...)
		}
	}
	b = append(b, '"')
	return string(b)
}
