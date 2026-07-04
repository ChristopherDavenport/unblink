package webext_test

import (
	"net/url"
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func TestParseMatchPatternInvalid(t *testing.T) {
	for _, s := range []string{"", "example.com/*", "gopher://x/*", "https://noPath", "*://foo*bar.com/*"} {
		if _, err := webext.ParseMatchPattern(s); err == nil {
			t.Errorf("ParseMatchPattern(%q): want error, got nil", s)
		}
	}
}

func TestMatchPatternMatches(t *testing.T) {
	tests := []struct {
		pattern string
		url     string
		want    bool
	}{
		{"<all_urls>", "https://x.com/a", true},
		{"<all_urls>", "data:text/plain,hi", false},
		{"*://*.example.com/*", "https://www.example.com/path", true},
		{"*://*.example.com/*", "https://example.com/", true},
		{"*://*.example.com/*", "http://example.com/", true},
		{"*://*.example.com/*", "https://notexample.com/", false},
		{"*://*.example.com/*", "ftp://example.com/", false}, // '*' scheme excludes ftp
		{"https://example.com/*", "http://example.com/", false},
		{"https://example.com/foo*", "https://example.com/foobar", true},
		{"https://example.com/foo*", "https://example.com/bar", false},
		{"*://example.com/*", "https://sub.example.com/", false}, // exact host, no subdomain
	}
	for _, tt := range tests {
		mp, err := webext.ParseMatchPattern(tt.pattern)
		if err != nil {
			t.Fatalf("ParseMatchPattern(%q): %v", tt.pattern, err)
		}
		if got := mp.Matches(mustURL(t, tt.url)); got != tt.want {
			t.Errorf("%q.Matches(%q) = %v, want %v", tt.pattern, tt.url, got, tt.want)
		}
	}
}

func TestGlobMatches(t *testing.T) {
	tests := []struct {
		glob, s string
		want    bool
	}{
		{"*.png", "icon.png", true},
		{"*.png", "icon.jpg", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*", "anything", true},
		{"foo*bar", "fooXYZbar", true},
		{"foo*bar", "foobar", true},
	}
	for _, tt := range tests {
		if got := webext.Glob(tt.glob).Matches(tt.s); got != tt.want {
			t.Errorf("Glob(%q).Matches(%q) = %v, want %v", tt.glob, tt.s, got, tt.want)
		}
	}
}
