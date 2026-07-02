package fetch

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The --tls-mimic transport must pool HTTP/2 connections: repeat fetches to one
// host reuse a single TCP+TLS connection instead of handshaking per request.
func TestTLSMimicH2ConnectionReuse(t *testing.T) {
	var newConns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "proto=%s", r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.StartTLS()
	defer srv.Close()

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
