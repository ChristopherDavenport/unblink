package js

import (
	"net/http"
	"net/url"
	"testing"
)

func TestIsSecureContext(t *testing.T) {
	cases := map[string]bool{
		"https://x.com/p":       true,
		"http://x.com/p":        false,
		"http://localhost/p":    true,
		"http://localhost:8080": true,
		"http://app.localhost/": true,
		"http://127.0.0.1/p":    true,
		"http://[::1]/p":        true,
		"http://10.0.0.1/p":     false,
		"wss://x.com/p":         true,
	}
	for in, want := range cases {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatalf("parse %q: %v", in, err)
		}
		if got := isSecureContext(u); got != want {
			t.Errorf("isSecureContext(%q)=%v want %v", in, got, want)
		}
	}
	if isSecureContext(nil) {
		t.Error("isSecureContext(nil) should be false")
	}
}

func TestCrossOriginIsolated(t *testing.T) {
	secure, _ := url.Parse("https://x.com/")
	insecure, _ := url.Parse("http://x.com/")
	hdr := func(coop, coep string) http.Header {
		h := http.Header{}
		if coop != "" {
			h.Set("Cross-Origin-Opener-Policy", coop)
		}
		if coep != "" {
			h.Set("Cross-Origin-Embedder-Policy", coep)
		}
		return h
	}
	cases := []struct {
		name string
		base *url.URL
		h    http.Header
		want bool
	}{
		{"both + secure", secure, hdr("same-origin", "require-corp"), true},
		{"credentialless", secure, hdr("same-origin", "credentialless"), true},
		{"coop param tolerated", secure, hdr("same-origin; report-to=\"x\"", "require-corp"), true},
		{"missing coep", secure, hdr("same-origin", ""), false},
		{"missing coop", secure, hdr("", "require-corp"), false},
		{"wrong coop", secure, hdr("unsafe-none", "require-corp"), false},
		{"not secure", insecure, hdr("same-origin", "require-corp"), false},
		{"nil headers", secure, nil, false},
	}
	for _, c := range cases {
		if got := crossOriginIsolated(c.h, c.base); got != c.want {
			t.Errorf("%s: crossOriginIsolated=%v want %v", c.name, got, c.want)
		}
	}
}
