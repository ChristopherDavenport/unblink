package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

// mimicServer starts a TLS test server that reports the request protocol and
// counts the connections it accepts. h2 controls whether it offers h2 via ALPN.
func mimicServer(t *testing.T, h2 bool, handler http.Handler) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "proto=%s", r.Proto)
		})
	}
	newConns := new(atomic.Int32)
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = h2
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, newConns
}

// The --tls-mimic transport must pool HTTP/2 connections: repeat fetches to one
// host reuse a single TCP+TLS connection instead of handshaking per request.
func TestTLSMimicH2ConnectionReuse(t *testing.T) {
	srv, newConns := mimicServer(t, true, nil)

	c, _ := New(WithTLSMimic(true), withInsecureTLS())
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		p, err := c.Get(ctx, srv.URL)
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if got := string(p.Raw); got != "proto=HTTP/2.0" {
			t.Fatalf("get %d: body = %q, want h2 (ALPN must negotiate h2 for pooling)", i, got)
		}
	}
	if n := newConns.Load(); n != 1 {
		t.Errorf("server saw %d connections for 3 requests, want 1 (pooled)", n)
	}
}

// A pooled connection the server has since closed must not fail the next fetch:
// it is replaced by a fresh dial, whether the close was noticed before the
// request (the pool's liveness check) or by the request failing (the retry).
func TestTLSMimicH2ReplacesClosedPooledConn(t *testing.T) {
	srv, newConns := mimicServer(t, true, nil)

	c, _ := New(WithTLSMimic(true), withInsecureTLS())
	ctx := context.Background()
	if _, err := c.Get(ctx, srv.URL); err != nil {
		t.Fatalf("first get: %v", err)
	}
	srv.CloseClientConnections()
	p, err := c.Get(ctx, srv.URL)
	if err != nil {
		t.Fatalf("get after the server closed the pooled connection: %v", err)
	}
	if got := string(p.Raw); got != "proto=HTTP/2.0" {
		t.Errorf("body = %q, want proto=HTTP/2.0", got)
	}
	if n := newConns.Load(); n != 2 {
		t.Errorf("server saw %d connections, want 2 (one replaced)", n)
	}
}

// When a request fails on a pooled connection that still looked alive, RoundTrip
// retries it once on a fresh connection, rewinding the body via GetBody. The
// server here resets the second stream of every connection, so the pooled
// connection passes the liveness check and the failure is the request's own.
func TestTLSMimicH2RetriesFailedPooledRequest(t *testing.T) {
	var mu sync.Mutex
	perConn := map[string]int{}
	srv, newConns := mimicServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		perConn[r.RemoteAddr]++
		n := perConn[r.RemoteAddr]
		mu.Unlock()
		if n == 2 {
			panic(http.ErrAbortHandler) // RST_STREAM; the connection stays up
		}
		fmt.Fprintf(w, "%s %s body=%q", r.Proto, r.Method, body)
	}))

	rt := newUTLSRoundTripper(&net.Dialer{}, true)
	do := func(method, body string) (string, error) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body) // NewRequest sets GetBody for a strings.Reader
		}
		req, err := http.NewRequest(method, srv.URL, rd)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	if got, err := do(http.MethodGet, ""); err != nil || got != `HTTP/2.0 GET body=""` {
		t.Fatalf("first request = %q, %v", got, err)
	}
	// Second request on the pooled connection is reset; the retry must deliver
	// the full body on a new connection.
	got, err := do(http.MethodPost, "payload")
	if err != nil {
		t.Fatalf("request on a pooled connection that fails: %v (want a transparent retry)", err)
	}
	if got != `HTTP/2.0 POST body="payload"` {
		t.Errorf("retried response = %q, want the POST body replayed", got)
	}
	if n := newConns.Load(); n != 2 {
		t.Errorf("server saw %d connections, want 2 (retry on a fresh dial)", n)
	}
}

// A server that does not pick h2 is served over HTTP/1.1 on the same utls
// connection, one connection per request.
func TestTLSMimicHTTP1Fallback(t *testing.T) {
	srv, newConns := mimicServer(t, false, nil)

	c, _ := New(WithTLSMimic(true), withInsecureTLS())
	for i := 0; i < 2; i++ {
		p, err := c.Get(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if got := string(p.Raw); got != "proto=HTTP/1.1" {
			t.Errorf("get %d: body = %q, want proto=HTTP/1.1", i, got)
		}
	}
	if n := newConns.Load(); n != 2 {
		t.Errorf("server saw %d connections for 2 h1 requests, want 2 (h1 is unpooled)", n)
	}
}

// Certificate verification is on unless the test-only insecure hook is set: the
// handshake is utls's, so this is utls's verification, on both ALPN outcomes.
func TestTLSMimicVerifiesCertificates(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		srv, _ := mimicServer(t, h2, nil)

		strict, _ := New(WithTLSMimic(true))
		if _, err := strict.Get(context.Background(), srv.URL); err == nil {
			t.Errorf("h2=%v: get of a self-signed server succeeded without the insecure hook", h2)
		} else if !strings.Contains(err.Error(), "certificate") {
			t.Errorf("h2=%v: error = %v, want a certificate verification failure", h2, err)
		}

		lax, _ := New(WithTLSMimic(true), withInsecureTLS())
		if _, err := lax.Get(context.Background(), srv.URL); err != nil {
			t.Errorf("h2=%v: get with the insecure hook: %v", h2, err)
		}
	}
}

// The dialer's Control hook (the SSRF guard, see ssrfControl in
// internal/browser) must stay on the dial path of the mimic transport: every
// connection it opens goes through the hook, and a hook that refuses stops the
// request before anything reaches the server.
func TestTLSMimicDialControl(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		srv, newConns := mimicServer(t, h2, nil)

		var calls atomic.Int32
		allow, _ := New(WithTLSMimic(true), withInsecureTLS(),
			WithDialControl(func(_, _ string, _ syscall.RawConn) error {
				calls.Add(1)
				return nil
			}))
		if _, err := allow.Get(context.Background(), srv.URL); err != nil {
			t.Fatalf("h2=%v: get: %v", h2, err)
		}
		if calls.Load() != 1 || newConns.Load() != 1 {
			t.Errorf("h2=%v: control hook ran %d times for %d connections, want 1 and 1", h2, calls.Load(), newConns.Load())
		}

		errBlocked := errors.New("blocked by test control hook")
		deny, _ := New(WithTLSMimic(true), withInsecureTLS(),
			WithDialControl(func(_, _ string, _ syscall.RawConn) error { return errBlocked }))
		if _, err := deny.Get(context.Background(), srv.URL); !errors.Is(err, errBlocked) {
			t.Errorf("h2=%v: error = %v, want the control hook's refusal", h2, err)
		}
		if n := newConns.Load(); n != 1 {
			t.Errorf("h2=%v: server saw %d connections, want 1 (the refused dial must not connect)", h2, n)
		}
	}
}

// The ClientHello on the wire must be utls's Chrome hello, not crypto/tls's: it
// carries GREASE values (RFC 8701), which Go's own TLS stack never sends, and
// offers h2 then http/1.1. This guards against the handshake ever moving to the
// HTTP transport's built-in TLS.
func TestTLSMimicClientHelloIsChrome(t *testing.T) {
	isGREASE := func(v uint16) bool { return v&0x0f0f == 0x0a0a && v>>8 == v&0xff }

	var mu sync.Mutex
	var hellos []*tls.ClientHelloInfo
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s", r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
		mu.Lock()
		hellos = append(hellos, chi)
		mu.Unlock()
		return nil, nil
	}}
	srv.StartTLS()
	defer srv.Close()

	c, _ := New(WithTLSMimic(true), withInsecureTLS())
	p, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := string(p.Raw); got != "proto=HTTP/2.0" {
		t.Fatalf("body = %q, want proto=HTTP/2.0", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hellos) != 1 {
		t.Fatalf("server saw %d ClientHellos, want 1", len(hellos))
	}
	chi := hellos[0]
	if !slices.ContainsFunc(chi.CipherSuites, isGREASE) {
		t.Errorf("ClientHello cipher suites carry no GREASE value: %#04x", chi.CipherSuites)
	}
	if !slices.ContainsFunc(chi.SupportedVersions, isGREASE) {
		t.Errorf("ClientHello supported_versions carry no GREASE value: %#04x", chi.SupportedVersions)
	}
	if want := []string{"h2", "http/1.1"}; !slices.Equal(chi.SupportedProtos, want) {
		t.Errorf("ClientHello ALPN = %v, want %v", chi.SupportedProtos, want)
	}
}
