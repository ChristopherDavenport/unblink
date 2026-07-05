package fetch

import (
	"net/http"
	"net/url"
	"testing"
)

// TestPublicSuffixJarRejectsSupercookie confirms unblink's actual cookie jar (built
// by New with publicsuffix.List) refuses a cookie scoped to a public suffix —
// otherwise `Domain=.com` would be a supercookie readable by every *.com site —
// while a legitimate registrable-domain cookie is stored and shared with subdomains.
// The rejection is stdlib behavior, but this locks that unblink keeps the
// PublicSuffixList wired into its jar construction.
func TestPublicSuffixJarRejectsSupercookie(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	jar := c.Jar()
	com := jarURL(t, "https://example.com/")

	jar.SetCookies(com, []*http.Cookie{{Name: "super", Value: "x", Domain: ".com", Path: "/"}})
	if got := jar.Cookies(jarURL(t, "https://evil.com/")); len(got) != 0 {
		t.Errorf("public-suffix (.com) cookie leaked cross-site: evil.com got %v", got)
	}

	jar.SetCookies(com, []*http.Cookie{{Name: "ok", Value: "y", Domain: "example.com", Path: "/"}})
	if !jarHas(jar.Cookies(jarURL(t, "https://sub.example.com/")), "ok") {
		t.Error("legitimate example.com cookie was not shared with sub.example.com")
	}
	if jarHas(jar.Cookies(jarURL(t, "https://evil.com/")), "ok") {
		t.Error("example.com cookie leaked to evil.com")
	}
}

// TestTrackingJarRecordsHttpOnly confirms the HttpOnly-name tracking that lets the
// document.cookie bridge hide HttpOnly cookies (the stdlib jar drops the flag on
// read, so without this an HttpOnly session cookie would be visible to page JS).
func TestTrackingJarRecordsHttpOnly(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tj, ok := c.Jar().(*trackingJar)
	if !ok {
		t.Fatalf("jar is %T, want *trackingJar", c.Jar())
	}
	tj.SetCookies(jarURL(t, "https://example.com/"), []*http.Cookie{
		{Name: "sid", Value: "s", HttpOnly: true, Path: "/"},
		{Name: "theme", Value: "dark", Path: "/"},
	})
	if !tj.IsHTTPOnly("example.com", "sid") {
		t.Error("HttpOnly cookie sid was not recorded")
	}
	if tj.IsHTTPOnly("example.com", "theme") {
		t.Error("non-HttpOnly cookie theme was wrongly recorded as HttpOnly")
	}
	// A subdomain read still hides the domain-scoped HttpOnly cookie.
	if !tj.IsHTTPOnly("app.example.com", "sid") {
		t.Error("HttpOnly sid not hidden for a subdomain read")
	}
}

func jarURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func jarHas(cks []*http.Cookie, name string) bool {
	for _, ck := range cks {
		if ck.Name == name {
			return true
		}
	}
	return false
}
