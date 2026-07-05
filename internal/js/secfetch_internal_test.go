package js

import (
	"net/url"
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

func TestSecFetchSite(t *testing.T) {
	base, _ := url.Parse("https://www.example.com/page")
	cases := map[string]string{
		"https://www.example.com/x":      "same-origin",
		"https://www.example.com:8443/x": "same-site", // same registrable domain + scheme, diff port
		"https://api.example.com/x":      "same-site",
		"http://www.example.com/x":       "cross-site", // scheme differs
		"https://other.com/x":            "cross-site",
		"https://192.168.0.1/x":          "cross-site", // bare IP, no registrable domain
	}
	for target, want := range cases {
		if got := secFetchSite(base, target); got != want {
			t.Errorf("secFetchSite(%q)=%q want %q", target, got, want)
		}
	}
}

func TestSecFetchHeaders(t *testing.T) {
	base, _ := url.Parse("https://example.com/page")

	// A script subrequest → dest script, mode no-cors.
	h := secFetchHeaders(webext.TypeScript, base, "https://example.com/app.js", nil)
	if h["Sec-Fetch-Dest"] != "script" || h["Sec-Fetch-Mode"] != "no-cors" || h["Sec-Fetch-Site"] != "same-origin" {
		t.Errorf("script headers = %v", h)
	}

	// A fetch/XHR subrequest → dest empty, mode cors.
	h = secFetchHeaders(webext.TypeXHR, base, "https://api.other.com/x", nil)
	if h["Sec-Fetch-Dest"] != "empty" || h["Sec-Fetch-Mode"] != "cors" || h["Sec-Fetch-Site"] != "cross-site" {
		t.Errorf("xhr headers = %v", h)
	}

	// Page-supplied Sec-Fetch-* (forbidden headers) must be stripped and replaced.
	in := map[string]string{"sec-fetch-dest": "document", "Sec-Fetch-Mode": "navigate", "X-Keep": "1"}
	h = secFetchHeaders(webext.TypeXHR, base, "https://example.com/x", in)
	if h["Sec-Fetch-Dest"] != "empty" || h["Sec-Fetch-Mode"] != "cors" {
		t.Errorf("page-spoofed Sec-Fetch not overridden: %v", h)
	}
	if _, ok := h["sec-fetch-dest"]; ok {
		t.Errorf("lowercase page Sec-Fetch header survived: %v", h)
	}
	if h["X-Keep"] != "1" {
		t.Errorf("non-Sec-Fetch header dropped: %v", h)
	}
}
