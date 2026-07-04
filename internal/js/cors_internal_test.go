package js

import (
	"net/url"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://x.com/p", "https://x.com/q", true},
		{"https://x.com:443/p", "https://x.com/q", true}, // default port normalized
		{"http://x.com:80/p", "http://x.com/q", true},
		{"https://x.com/p", "http://x.com/q", false},  // scheme differs
		{"https://x.com/p", "https://y.com/q", false}, // host differs
		{"https://x.com:8443/p", "https://x.com/q", false},
		{"https://X.COM/p", "https://x.com/q", true}, // host case-insensitive
	}
	for _, c := range cases {
		if got := sameOrigin(mustURL(t, c.a), mustURL(t, c.b)); got != c.want {
			t.Errorf("sameOrigin(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestOriginString(t *testing.T) {
	cases := map[string]string{
		"https://x.com/p?q":    "https://x.com",
		"https://x.com:8443/p": "https://x.com:8443",
		"http://x.com:80/p":    "http://x.com",
		"http://x.com:8080/":   "http://x.com:8080",
	}
	for in, want := range cases {
		if got := originString(mustURL(t, in)); got != want {
			t.Errorf("originString(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsSimpleRequest(t *testing.T) {
	cases := []struct {
		method  string
		headers map[string]string
		want    bool
	}{
		{"GET", nil, true},
		{"POST", map[string]string{"content-type": "text/plain"}, true},
		{"POST", map[string]string{"content-type": "application/json"}, false}, // non-safelisted CT
		{"PUT", nil, false}, // non-safelisted method
		{"GET", map[string]string{"x-custom": "1"}, false},
		{"HEAD", map[string]string{"accept": "*/*"}, true},
		{"POST", map[string]string{"content-type": "multipart/form-data; boundary=z"}, true},
	}
	for _, c := range cases {
		if got := isSimpleRequest(c.method, c.headers); got != c.want {
			t.Errorf("isSimpleRequest(%q,%v)=%v want %v", c.method, c.headers, got, c.want)
		}
	}
}

func TestValidateActualCORS(t *testing.T) {
	origin := "https://page.com"
	resp := func(h map[string]string) *Response { return &Response{Status: 200, Headers: h} }
	cases := []struct {
		name         string
		headers      map[string]string
		credentialed bool
		want         bool
	}{
		{"wildcard uncredentialed", map[string]string{"access-control-allow-origin": "*"}, false, true},
		{"echo uncredentialed", map[string]string{"access-control-allow-origin": "https://page.com"}, false, true},
		{"missing", map[string]string{}, false, false},
		{"other origin", map[string]string{"access-control-allow-origin": "https://evil.com"}, false, false},
		{"wildcard credentialed rejected", map[string]string{"access-control-allow-origin": "*", "access-control-allow-credentials": "true"}, true, false},
		{"echo credentialed ok", map[string]string{"access-control-allow-origin": "https://page.com", "access-control-allow-credentials": "true"}, true, true},
		{"echo credentialed no flag", map[string]string{"access-control-allow-origin": "https://page.com"}, true, false},
	}
	for _, c := range cases {
		if got := validateActualCORS(resp(c.headers), origin, c.credentialed); got != c.want {
			t.Errorf("%s: validateActualCORS=%v want %v", c.name, got, c.want)
		}
	}
}

func TestValidatePreflight(t *testing.T) {
	origin := "https://page.com"
	base := map[string]string{
		"access-control-allow-origin":  "https://page.com",
		"access-control-allow-methods": "PUT, DELETE",
		"access-control-allow-headers": "x-custom",
	}
	req := map[string]string{"x-custom": "1"}
	if !validatePreflight(&Response{Status: 204, Headers: base}, origin, "PUT", req, false) {
		t.Error("valid preflight rejected")
	}
	// Method not allowed.
	if validatePreflight(&Response{Status: 204, Headers: map[string]string{
		"access-control-allow-origin":  "https://page.com",
		"access-control-allow-methods": "GET",
		"access-control-allow-headers": "x-custom",
	}}, origin, "PUT", req, false) {
		t.Error("preflight allowed a method not in Allow-Methods")
	}
	// Header not allowed.
	if validatePreflight(&Response{Status: 204, Headers: map[string]string{
		"access-control-allow-origin":  "https://page.com",
		"access-control-allow-methods": "PUT",
	}}, origin, "PUT", req, false) {
		t.Error("preflight allowed a header not in Allow-Headers")
	}
	// Wildcard header (uncredentialed) covers custom headers.
	if !validatePreflight(&Response{Status: 204, Headers: map[string]string{
		"access-control-allow-origin":  "*",
		"access-control-allow-methods": "*",
		"access-control-allow-headers": "*",
	}}, origin, "PUT", req, false) {
		t.Error("wildcard preflight rejected")
	}
	// Error status fails.
	if validatePreflight(&Response{Status: 500, Headers: base}, origin, "PUT", req, false) {
		t.Error("preflight with 5xx status accepted")
	}
}
