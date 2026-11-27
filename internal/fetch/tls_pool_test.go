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
	"time"
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

// --- pool behaviour under concurrency, stream limits and request contexts ---

// mimicAddr is the pool key (host:port) for a test server.
func mimicAddr(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "https://") }

// mimicGet performs one GET through rt and returns the response body.
func mimicGet(ctx context.Context, rt *utlsRoundTripper, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// eventually polls cond until it holds, failing the test if it never does. It is
// for state that settles asynchronously with no event to wait on.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// gatedDialer returns a dialer whose every dial parks in the Control hook until
// gate is closed, then fails with fail(n) if that is non-nil (n is the 1-based
// dial count). calls counts the dials.
func gatedDialer(gate <-chan struct{}, fail func(n int32) error) (*net.Dialer, *atomic.Int32) {
	calls := new(atomic.Int32)
	return &net.Dialer{Control: func(_, _ string, _ syscall.RawConn) error {
		n := calls.Add(1)
		<-gate
		if fail != nil {
			return fail(n)
		}
		return nil
	}}, calls
}

// dialWaiters reports how many requests are waiting on the in-progress dial for
// addr (0 if there is none).
func dialWaiters(rt *utlsRoundTripper, addr string) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if call := rt.dials[addr]; call != nil {
		return call.waiters
	}
	return 0
}

func pooledConn(rt *utlsRoundTripper, addr string) *http.ClientConn {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.conns[addr]
}

// A burst of requests to a host with no pooled connection must share one dial +
// handshake and one h2 connection. (Each used to dial its own and close the
// previous one under its in-flight request: 8 requests, 7 failures.)
func TestTLSMimicH2ColdBurstSharesOneConn(t *testing.T) {
	srv, newConns := mimicServer(t, true, nil)
	gate := make(chan struct{})
	dialer, dials := gatedDialer(gate, nil)
	rt := newUTLSRoundTripper(dialer, true)

	const n = 8
	type result struct {
		body string
		err  error
	}
	results := make(chan result, n)
	for range n {
		go func() {
			body, err := mimicGet(context.Background(), rt, srv.URL)
			results <- result{body, err}
		}()
	}
	// Hold the dial until every request is waiting on it, then let it finish.
	eventually(t, "all requests to join the dial", func() bool { return dialWaiters(rt, mimicAddr(srv)) == n })
	close(gate)

	for range n {
		if r := <-results; r.err != nil || r.body != "proto=HTTP/2.0" {
			t.Errorf("burst request = %q, %v; want proto=HTTP/2.0", r.body, r.err)
		}
	}
	if d, c := dials.Load(), newConns.Load(); d != 1 || c != 1 {
		t.Errorf("%d dials and %d connections for a burst of %d, want 1 and 1", d, c, n)
	}
}

// A request waiting on a shared dial honours its own context: it returns its
// context's error without waiting for the dial, and without failing the others
// — even when it is the request that started the dial.
func TestTLSMimicH2DialWaiterCancel(t *testing.T) {
	srv, newConns := mimicServer(t, true, nil)
	addr := mimicAddr(srv)
	gate := make(chan struct{})
	dialer, dials := gatedDialer(gate, nil)
	rt := newUTLSRoundTripper(dialer, true)

	// The first request starts the dial; it is the one that gets cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan error, 1)
	go func() {
		_, err := mimicGet(ctx, rt, srv.URL)
		cancelled <- err
	}()
	eventually(t, "the first request to start the dial", func() bool { return dialWaiters(rt, addr) == 1 })

	const others = 3
	results := make(chan error, others)
	for range others {
		go func() {
			_, err := mimicGet(context.Background(), rt, srv.URL)
			results <- err
		}()
	}
	eventually(t, "the other requests to join the dial", func() bool { return dialWaiters(rt, addr) == 1+others })

	// The dial is still parked: the cancelled request must return regardless.
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled waiter returned %v, want context.Canceled", err)
	}

	close(gate)
	for range others {
		if err := <-results; err != nil {
			t.Errorf("request sharing a dial whose starter was cancelled: %v", err)
		}
	}
	if d, c := dials.Load(), newConns.Load(); d != 1 || c != 1 {
		t.Errorf("%d dials and %d connections, want 1 and 1", d, c)
	}
}

// When the only request waiting on a dial is cancelled, the dial is abandoned
// (not left for a later request to inherit as a failure) and the next request
// starts its own.
func TestTLSMimicH2AbandonedDialIsNotReused(t *testing.T) {
	srv, _ := mimicServer(t, true, nil)
	addr := mimicAddr(srv)
	gate := make(chan struct{})
	dialer, dials := gatedDialer(gate, nil)
	rt := newUTLSRoundTripper(dialer, true)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := mimicGet(ctx, rt, srv.URL)
		errc <- err
	}()
	eventually(t, "the request to start the dial", func() bool { return dialWaiters(rt, addr) == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request returned %v, want context.Canceled", err)
	}
	rt.mu.Lock()
	inProgress := len(rt.dials)
	rt.mu.Unlock()
	if inProgress != 0 {
		t.Errorf("%d dials still registered after their only waiter left, want 0", inProgress)
	}

	close(gate)
	if _, err := mimicGet(context.Background(), rt, srv.URL); err != nil {
		t.Fatalf("request after an abandoned dial: %v", err)
	}
	if d := dials.Load(); d != 2 {
		t.Errorf("%d dials, want 2 (the abandoned one and a fresh one)", d)
	}
}

// A failed shared dial fails each waiter once with the dial's error, and the
// failure is not cached: the next request dials again.
func TestTLSMimicH2FailedDialIsSharedNotCached(t *testing.T) {
	srv, newConns := mimicServer(t, true, nil)
	gate := make(chan struct{})
	errRefused := errors.New("refused by test control hook")
	dialer, dials := gatedDialer(gate, func(n int32) error {
		if n == 1 {
			return errRefused
		}
		return nil
	})
	rt := newUTLSRoundTripper(dialer, true)

	const n = 4
	results := make(chan error, n)
	for range n {
		go func() {
			_, err := mimicGet(context.Background(), rt, srv.URL)
			results <- err
		}()
	}
	eventually(t, "all requests to join the dial", func() bool { return dialWaiters(rt, mimicAddr(srv)) == n })
	close(gate)
	for range n {
		if err := <-results; !errors.Is(err, errRefused) {
			t.Errorf("waiter on a failed dial returned %v, want the dial's error", err)
		}
	}
	if d, c := dials.Load(), newConns.Load(); d != 1 || c != 0 {
		t.Errorf("%d dials and %d connections for one failed shared dial, want 1 and 0", d, c)
	}

	if body, err := mimicGet(context.Background(), rt, srv.URL); err != nil || body != "proto=HTTP/2.0" {
		t.Fatalf("request after a failed dial = %q, %v; want a fresh, successful dial", body, err)
	}
	if d := dials.Load(); d != 2 {
		t.Errorf("%d dials, want 2 (the failure must not be cached)", d)
	}
}

// Waiters on a dial that negotiates HTTP/1.1 cannot share its connection: one
// takes it and the rest dial their own, so h1 stays one connection per request.
func TestTLSMimicHTTP1BurstDialsPerRequest(t *testing.T) {
	srv, newConns := mimicServer(t, false, nil)
	gate := make(chan struct{})
	dialer, _ := gatedDialer(gate, nil)
	rt := newUTLSRoundTripper(dialer, true)

	const n = 4
	type result struct {
		body string
		err  error
	}
	results := make(chan result, n)
	for range n {
		go func() {
			body, err := mimicGet(context.Background(), rt, srv.URL)
			results <- result{body, err}
		}()
	}
	eventually(t, "all requests to join the dial", func() bool { return dialWaiters(rt, mimicAddr(srv)) == n })
	close(gate)
	for range n {
		if r := <-results; r.err != nil || r.body != "proto=HTTP/1.1" {
			t.Errorf("h1 burst request = %q, %v; want proto=HTTP/1.1", r.body, r.err)
		}
	}
	if c := newConns.Load(); c != n {
		t.Errorf("server saw %d connections for %d concurrent h1 requests, want %d", c, n, n)
	}
	if cc := pooledConn(rt, mimicAddr(srv)); cc != nil {
		t.Error("an HTTP/1.1 connection was pooled")
	}
}

// gatedServer is an h2 test server whose handler, for any path but /warm,
// announces the path on entered and then blocks until release(path) or the
// request is cancelled.
type gatedServer struct {
	*httptest.Server
	newConns atomic.Int32
	entered  chan string
	quit     chan struct{} // closed at test end, so a failed test cannot strand a handler

	mu    sync.Mutex
	gates map[string]chan struct{}
}

func (s *gatedServer) gate(path string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gates[path]
	if g == nil {
		g = make(chan struct{})
		s.gates[path] = g
	}
	return g
}

func (s *gatedServer) release(path string) { close(s.gate(path)) }

func newGatedServer(t *testing.T, maxStreams int) *gatedServer {
	t.Helper()
	s := &gatedServer{entered: make(chan string, 16), quit: make(chan struct{}), gates: map[string]chan struct{}{}}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/warm" {
			s.entered <- r.URL.Path
			select {
			case <-s.gate(r.URL.Path):
			case <-r.Context().Done():
				return
			case <-s.quit:
				return
			}
		}
		fmt.Fprintf(w, "done %s", r.URL.Path)
	}))
	s.EnableHTTP2 = true
	if maxStreams > 0 {
		s.Config.HTTP2 = &http.HTTP2Config{MaxConcurrentStreams: maxStreams}
	}
	s.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			s.newConns.Add(1)
		}
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(s.quit) }) // runs first: Close waits for handlers
	return s
}

// valueSignalCtx closes touched the first time anything looks a value up in it.
// net/http's HTTP/2 client does so (for the httptrace hooks) on entering
// ClientConn.RoundTrip, which makes it a signal that a request has got past the
// pool and onto a connection — before it is known whether a stream is free.
type valueSignalCtx struct {
	context.Context
	once    sync.Once
	touched chan struct{}
}

func (c *valueSignalCtx) Value(key any) any {
	c.once.Do(func() { close(c.touched) })
	return c.Context.Value(key)
}

// A pooled connection at the server's MAX_CONCURRENT_STREAMS is busy, not dead.
// A request beyond the limit must wait for a stream; it must not get the
// connection closed under the requests already in flight (as it used to).
func TestTLSMimicH2AtStreamLimitQueues(t *testing.T) {
	srv := newGatedServer(t, 2)
	rt := newUTLSRoundTripper(&net.Dialer{}, true)
	ctx := context.Background()

	// Warm the pool. A completed response also means the client has processed
	// the server's SETTINGS, so it knows the stream limit.
	if _, err := mimicGet(ctx, rt, srv.URL+"/warm"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	cc := pooledConn(rt, mimicAddr(srv.Server))
	if cc == nil {
		t.Fatal("no pooled connection after the first request")
	}

	type result struct {
		path, body string
		err        error
	}
	results := make(chan result, 3)
	get := func(ctx context.Context, path string) {
		body, err := mimicGet(ctx, rt, srv.URL+path)
		results <- result{path, body, err}
	}
	go get(ctx, "/a")
	go get(ctx, "/b")
	<-srv.entered
	<-srv.entered
	if avail, inFlight := cc.Available(), cc.InFlight(); avail != 0 || inFlight != 2 {
		t.Fatalf("pooled connection has Available=%d InFlight=%d, want 0 and 2 (at the stream limit)", avail, inFlight)
	}

	// The third request arrives while the connection is full.
	cctx := &valueSignalCtx{Context: ctx, touched: make(chan struct{})}
	go get(cctx, "/c")
	select {
	case <-cctx.touched:
	case <-time.After(5 * time.Second):
		// net/http no longer consults the context where it did; the rest of the
		// test still holds, it just cannot prove the third request was queued.
		t.Log("third request did not signal reaching the connection")
	}
	select {
	case r := <-results:
		t.Fatalf("%s finished (%q, %v) while every handler was still blocked: a request at the stream limit disturbed the connection", r.path, r.body, r.err)
	default:
	}
	if pooledConn(rt, mimicAddr(srv.Server)) != cc || cc.Err() != nil {
		t.Fatalf("the full connection was evicted or closed (err %v)", cc.Err())
	}

	// Freeing one stream lets the queued request in, on the same connection,
	// while /b is still in flight.
	srv.release("/a")
	if got := <-srv.entered; got != "/c" {
		t.Fatalf("handler entered for %s, want /c", got)
	}
	srv.release("/b")
	srv.release("/c")
	for range 3 {
		r := <-results
		if r.err != nil || r.body != "done "+r.path {
			t.Errorf("%s = %q, %v; want it to complete", r.path, r.body, r.err)
		}
	}
	if n := srv.newConns.Load(); n != 1 {
		t.Errorf("server saw %d connections, want 1 (overflow queues on the pooled connection)", n)
	}
}

// manualDeadlineCtx is a context whose deadline passes when the test says so.
type manualDeadlineCtx struct {
	context.Context
	done chan struct{}
}

func (c *manualDeadlineCtx) Done() <-chan struct{} { return c.done }

func (c *manualDeadlineCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// A request that hits its own deadline mid-flight on a pooled connection fails
// with that context error. The connection is healthy: it must stay pooled (it
// used to be dropped unclosed, leaking the socket) and there must be no redial
// (which, on a dead context, turned the error into "dial tcp: i/o timeout").
func TestTLSMimicH2RequestTimeoutKeepsPooledConn(t *testing.T) {
	srv := newGatedServer(t, 0)
	addr := mimicAddr(srv.Server)
	dials := new(atomic.Int32)
	rt := newUTLSRoundTripper(&net.Dialer{Control: func(_, _ string, _ syscall.RawConn) error {
		dials.Add(1)
		return nil
	}}, true)

	if _, err := mimicGet(context.Background(), rt, srv.URL+"/warm"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	cc := pooledConn(rt, addr)

	ctx := &manualDeadlineCtx{Context: context.Background(), done: make(chan struct{})}
	errc := make(chan error, 1)
	go func() {
		_, err := mimicGet(ctx, rt, srv.URL+"/slow")
		errc <- err
	}()
	<-srv.entered // the request is in flight on the pooled connection
	close(ctx.done)
	if err := <-errc; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out request returned %v, want context.DeadlineExceeded", err)
	}

	if got := pooledConn(rt, addr); got != cc {
		t.Fatalf("pooled connection changed from %p to %p after a request timeout", cc, got)
	}
	if err := cc.Err(); err != nil {
		t.Fatalf("pooled connection was closed by a request timeout: %v", err)
	}
	if body, err := mimicGet(context.Background(), rt, srv.URL+"/warm"); err != nil || body != "done /warm" {
		t.Fatalf("request after the timeout = %q, %v", body, err)
	}
	if d, c := dials.Load(), srv.newConns.Load(); d != 1 || c != 1 {
		t.Errorf("%d dials and %d connections, want 1 and 1 (the timeout must not cost a connection)", d, c)
	}
}

// A pooled connection the server closed is closed on our side as well as
// replaced, not left behind.
func TestTLSMimicH2ClosesServerClosedConn(t *testing.T) {
	srv, newConns := mimicServer(t, true, nil)
	addr := mimicAddr(srv)
	rt := newUTLSRoundTripper(&net.Dialer{}, true)
	ctx := context.Background()

	if _, err := mimicGet(ctx, rt, srv.URL); err != nil {
		t.Fatalf("first get: %v", err)
	}
	old := pooledConn(rt, addr)
	srv.CloseClientConnections()
	if _, err := mimicGet(ctx, rt, srv.URL); err != nil {
		t.Fatalf("get after the server closed the pooled connection: %v", err)
	}
	eventually(t, "the dead connection to be closed", func() bool { return old.Err() != nil })
	if now := pooledConn(rt, addr); now == nil || now == old {
		t.Errorf("pool holds %p after the old connection %p died, want a new one", now, old)
	}
	if n := newConns.Load(); n != 2 {
		t.Errorf("server saw %d connections, want 2", n)
	}
}

// A connection that fails a request is retired: out of the pool at once, but
// closed only when the requests still running on it have finished.
func TestTLSMimicH2RetiredConnOutlivesInFlightRequests(t *testing.T) {
	var aborted atomic.Bool
	hold, entered := make(chan struct{}), make(chan struct{})
	srv, newConns := mimicServer(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hold":
			close(entered)
			<-hold
		case "/flaky":
			if aborted.CompareAndSwap(false, true) {
				panic(http.ErrAbortHandler) // RST_STREAM; the connection stays up
			}
		}
		fmt.Fprintf(w, "done %s", r.URL.Path)
	}))
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release) // before the server's Close, which waits for handlers
	addr := mimicAddr(srv)
	rt := newUTLSRoundTripper(&net.Dialer{}, true)
	ctx := context.Background()

	if _, err := mimicGet(ctx, rt, srv.URL); err != nil {
		t.Fatalf("warm: %v", err)
	}
	old := pooledConn(rt, addr)

	type result struct {
		body string
		err  error
	}
	held := make(chan result, 1)
	go func() {
		body, err := mimicGet(ctx, rt, srv.URL+"/hold")
		held <- result{body, err}
	}()
	<-entered

	// This request is reset on the pooled connection and retried on a new one.
	if body, err := mimicGet(ctx, rt, srv.URL+"/flaky"); err != nil || body != "done /flaky" {
		t.Fatalf("reset request = %q, %v; want a transparent retry", body, err)
	}
	if now := pooledConn(rt, addr); now == nil || now == old {
		t.Fatalf("pool holds %p after the old connection %p failed a request, want a new one", now, old)
	}
	if err := old.Err(); err != nil {
		t.Fatalf("retired connection closed with a request still in flight: %v", err)
	}

	release()
	if r := <-held; r.err != nil || r.body != "done /hold" {
		t.Errorf("request in flight on the retired connection = %q, %v; want it to complete", r.body, r.err)
	}
	eventually(t, "the retired connection to close once idle", func() bool { return old.Err() != nil })
	if n := newConns.Load(); n != 2 {
		t.Errorf("server saw %d connections, want 2", n)
	}
}

// Pooled connections do not live forever: an idle one is closed after the idle
// timeout and forgotten by the pool, and CloseIdleConnections closes them on
// demand.
func TestTLSMimicH2IdleConnsAreClosed(t *testing.T) {
	srv, newConns := mimicServer(t, true, nil)
	addr := mimicAddr(srv)
	ctx := context.Background()

	rt := newUTLSRoundTripper(&net.Dialer{}, true)
	if got := rt.h2.IdleConnTimeout; got != mimicIdleConnTimeout {
		t.Fatalf("h2 transport IdleConnTimeout = %v, want %v", got, mimicIdleConnTimeout)
	}
	rt.h2.IdleConnTimeout = 20 * time.Millisecond
	if _, err := mimicGet(ctx, rt, srv.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	// The connection may idle out before it can be looked up, so watch the pool.
	eventually(t, "the idle connection to be closed and forgotten", func() bool { return pooledConn(rt, addr) == nil })
	if _, err := mimicGet(ctx, rt, srv.URL); err != nil {
		t.Fatalf("get after the idle timeout: %v", err)
	}
	if n := newConns.Load(); n != 2 {
		t.Errorf("server saw %d connections, want 2 (the idle one was replaced)", n)
	}

	rt = newUTLSRoundTripper(&net.Dialer{}, true)
	if _, err := mimicGet(ctx, rt, srv.URL); err != nil {
		t.Fatalf("get: %v", err)
	}
	cc := pooledConn(rt, addr)
	rt.CloseIdleConnections()
	if err := cc.Err(); err == nil {
		t.Error("CloseIdleConnections left an idle pooled connection open")
	}
	if pooledConn(rt, addr) != nil {
		t.Error("CloseIdleConnections left the closed connection in the pool")
	}
}
