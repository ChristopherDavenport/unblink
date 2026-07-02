package browser

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/fetch"
)

func TestIsBlockedIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":       true,
		"::1":             true,
		"10.0.0.1":        true,
		"172.16.0.1":      true,
		"192.168.1.1":     true,
		"169.254.169.254": true, // cloud metadata (link-local)
		"0.0.0.0":         true,
		"100.100.100.200": true, // Alibaba Cloud metadata (CGNAT range)
		"100.64.0.1":      true, // CGNAT / shared address space
		"100.127.255.255": true, // CGNAT upper bound
		"255.255.255.255": true, // limited broadcast
		"8.8.8.8":         false,
		"1.1.1.1":         false,
		"100.63.255.255":  false, // just below CGNAT
		"100.128.0.0":     false, // just above CGNAT
	}
	for s, want := range cases {
		if got := isBlockedIP(net.ParseIP(s)); got != want {
			t.Errorf("isBlockedIP(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestGuardedTransportSSRF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	ctx := context.Background()

	// Default policy blocks the loopback test server.
	b, _ := New(WithJS(time.Second))
	if _, err := b.newRenderTransport(b.client, 0).Do(ctx, "GET", srv.URL, nil, nil); err == nil {
		t.Error("expected loopback request to be blocked by the SSRF guard")
	}

	// allow-private permits it.
	bp, _ := New(WithJS(time.Second), WithJSAllowPrivate(true))
	if _, err := bp.newRenderTransport(bp.client, 0).Do(ctx, "GET", srv.URL, nil, nil); err != nil {
		t.Errorf("allow-private should reach loopback: %v", err)
	}
}

func TestCookieAdapterHidesHttpOnly(t *testing.T) {
	client, err := fetch.New() // fetch.New wraps its jar to track HttpOnly
	if err != nil {
		t.Fatalf("fetch.New: %v", err)
	}
	u, _ := url.Parse("https://example.com/")
	client.Jar().SetCookies(u, []*http.Cookie{
		{Name: "sid", Value: "SECRETSESSION", HttpOnly: true},
		{Name: "theme", Value: "dark"},
	})

	got := cookieAdapter{jar: client.Jar()}.Cookies("https://example.com/page")
	if strings.Contains(got, "SECRETSESSION") || strings.Contains(got, "sid=") {
		t.Errorf("document.cookie exposed an HttpOnly cookie: %q", got)
	}
	if !strings.Contains(got, "theme=dark") {
		t.Errorf("document.cookie should still expose non-HttpOnly cookies, got %q", got)
	}
}

func TestGuardedTransportBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client, _ := fetch.New() // no dial guard → can reach the loopback server
	g := &guardedTransport{client: client, max: 2}
	ctx := context.Background()

	if _, err := g.Do(ctx, "GET", srv.URL, nil, nil); err != nil {
		t.Fatalf("request 1: %v", err)
	}
	if _, err := g.Do(ctx, "GET", srv.URL, nil, nil); err != nil {
		t.Fatalf("request 2: %v", err)
	}
	if _, err := g.Do(ctx, "GET", srv.URL, nil, nil); err == nil {
		t.Error("request 3 should exceed the budget")
	}
	if got := g.Denied(); got != 1 {
		t.Errorf("Denied() = %d, want 1", got)
	}
}

func TestGuardedTransportWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client, _ := fetch.New() // no dial guard → can reach the loopback server
	// Live-session style: no monotonic cap, but a rolling window of 2 per long window.
	g := &guardedTransport{client: client, windowMax: 2, window: time.Hour}
	ctx := context.Background()

	for i := 1; i <= 2; i++ {
		if _, err := g.Do(ctx, "GET", srv.URL, nil, nil); err != nil {
			t.Fatalf("request %d within window: %v", i, err)
		}
	}
	if _, err := g.Do(ctx, "GET", srv.URL, nil, nil); err == nil {
		t.Error("request 3 should exceed the rolling window cap")
	}
	if got := g.Denied(); got != 1 {
		t.Errorf("Denied() = %d, want 1", got)
	}
}
