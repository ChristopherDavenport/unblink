package fetch

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Chrome desktop HTTP/2 SETTINGS values (best-effort, as far as net/http's
// HTTP/2 client exposes). Re-check these when utls.HelloChrome_Auto advances to a
// newer Chrome; they are advertised in our client SETTINGS frame.
const (
	chromeH2MaxFrameSize      = 16384  // SETTINGS_MAX_FRAME_SIZE
	chromeH2HeaderTableSize   = 65536  // SETTINGS_HEADER_TABLE_SIZE (Go uses 4096 and omits it)
	chromeH2MaxHeaderListSize = 262144 // SETTINGS_MAX_HEADER_LIST_SIZE (Go advertises 10MiB)
)

// netHTTPH2HeaderListPad is what net/http's HTTP/2 client adds to
// Transport.MaxResponseHeaderBytes to get the SETTINGS_MAX_HEADER_LIST_SIZE it
// advertises: 10 header fields at 32 bytes of per-field overhead. net/http has no
// field for the setting itself, so we set MaxResponseHeaderBytes to the Chrome
// value minus this pad.
//
// The pad mirrors adjustHTTP1MaxHeaderSize in GOROOT's
// net/http/internal/http2/config.go (applied in transport.go's
// maxHeaderListSize). It is unexported and undocumented, so nothing stops a Go
// release changing it; TestTLSMimicH2Settings reads the SETTINGS frame off the
// wire and fails if the advertised value ever moves.
const netHTTPH2HeaderListPad = 10 * 32

// utlsRoundTripper presents a recent-Chrome TLS ClientHello (uTLS) — keeping a
// genuine Chrome JA3/JA4 — and dispatches to HTTP/2 or HTTP/1.1 based on the ALPN
// the server negotiates. The TCP dial and the TLS handshake are always ours: the
// SSRF Control dialer makes the dial, so the guard still applies, and utls (never
// crypto/tls) makes the handshake.
//
// HTTP/2 runs on net/http's own client. Once the handshake has picked h2, the
// established connection is handed to http.Transport.NewClientConn through the
// transport's DialTLSContext hook, which accepts any conn exposing *tls.Conn's
// ConnectionState method (see stdTLSStateConn). That transport is used for
// nothing else: it never dials, never handshakes and never pools.
//
// HTTP/2 connections are pooled here, one per host:port, the way a browser keeps
// one h2 session per origin. The pool's rules:
//
//   - Dials are coordinated per address (connect, dialCall): requests that find
//     no pooled connection while a dial + handshake is in progress wait for that
//     one attempt and share its connection, so a cold burst opens one
//     connection, not one per request.
//   - A connection at the server's MAX_CONCURRENT_STREAMS is busy, not dead:
//     requests beyond the limit queue on it (http.ClientConn.RoundTrip waits for
//     a free stream, honouring the request context) rather than opening a second
//     connection.
//   - A connection is only ever closed when it has nothing in flight. One that
//     must go (it failed a request, or a newer one replaced it) is retired: taken
//     out of the pool at once, closed as soon as its last request finishes.
//   - A request that fails because of its own context says nothing about the
//     connection, which stays pooled.
//   - Idle connections close after mimicIdleConnTimeout.
//
// HTTP/1.1 remains one connection per request — h1 servers are the rare case on
// the mimic path, and a portable h1 pool over a pre-established TLS conn is not
// worth the machinery.
//
// The h2 SETTINGS are best-effort tuned toward Chrome (see the consts above), so
// the HTTP/2 layer no longer fingerprints as Go's default. What net/http's client
// cannot control — and so remains Go's — is the SETTINGS frame *order*,
// INITIAL_WINDOW_SIZE, ENABLE_PUSH, the connection-level WINDOW_UPDATE, and the
// pseudo-header order; matching those needs a forked http2.
// TestTLSMimicH2Settings pins all of it as sent.
type utlsRoundTripper struct {
	dialer   *net.Dialer
	insecure bool // test-only
	helloID  utls.ClientHelloID
	h2       *http.Transport // only ever used for NewClientConn; see adoptConn

	// mu guards conns, dials and every dialCall field. Never call a
	// *http.ClientConn method that can run its state hook (RoundTrip, Close,
	// Reserve, Release, SetStateHook) while holding it: the hook takes mu.
	mu    sync.Mutex
	conns map[string]*http.ClientConn // pooled h2 connections by host:port
	dials map[string]*dialCall        // dials in progress by host:port
}

// mimicIdleConnTimeout is how long a pooled h2 connection may sit with no
// request before it is closed (net/http's own default for idle connections).
const mimicIdleConnTimeout = 90 * time.Second

// mimicDialTimeout bounds a shared dial + TLS handshake. The shared attempt
// cannot use any one request's deadline, so it carries its own. (The TCP dial
// alone is also bounded by the dialer's Timeout.)
const mimicDialTimeout = 30 * time.Second

func newUTLSRoundTripper(dialer *net.Dialer, insecure bool) *utlsRoundTripper {
	// HTTP/2 only: adoptConn hands this transport nothing but connections whose
	// ALPN already picked h2.
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	return &utlsRoundTripper{
		dialer:   dialer,
		insecure: insecure,
		helloID:  utls.HelloChrome_Auto,
		h2: &http.Transport{
			Protocols:      protocols,
			DialTLSContext: adoptedConn,
			HTTP2: &http.HTTP2Config{
				MaxReadFrameSize:          chromeH2MaxFrameSize,
				MaxDecoderHeaderTableSize: chromeH2HeaderTableSize,
			},
			MaxResponseHeaderBytes: chromeH2MaxHeaderListSize - netHTTPH2HeaderListPad,
			// The HTTP/2 client arms a per-connection idle timer from this and
			// closes a connection that has had no stream for that long.
			IdleConnTimeout: mimicIdleConnTimeout,
		},
		conns: make(map[string]*http.ClientConn),
		dials: make(map[string]*dialCall),
	}
}

// adoptKey carries an established connection to adoptedConn.
type adoptKey struct{}

// adoptedConn is the h2 transport's DialTLSContext. It dials nothing: it returns
// the connection adoptConn put in the context, which is already past the TLS
// handshake as DialTLSContext requires.
func adoptedConn(ctx context.Context, _, _ string) (net.Conn, error) {
	conn, _ := ctx.Value(adoptKey{}).(net.Conn)
	if conn == nil {
		return nil, errors.New("fetch: tls-mimic h2 transport asked to dial; it only adopts established connections")
	}
	return conn, nil
}

// adoptConn turns an established utls connection that negotiated h2 into an
// HTTP/2 client connection. It takes no request context: the connection is
// pooled and must outlive whichever request happened to open it.
func (rt *utlsRoundTripper) adoptConn(addr string, uconn *utls.UConn) (*http.ClientConn, error) {
	ctx := context.WithValue(context.Background(), adoptKey{}, net.Conn(stdTLSStateConn{uconn}))
	cc, err := rt.h2.NewClientConn(ctx, "https", addr)
	if err != nil {
		return nil, err
	}
	// Forget the connection as soon as it closes (idle timeout, server close),
	// so the pool does not hold a dead entry for a host that is never revisited.
	cc.SetStateHook(func(cc *http.ClientConn) {
		if cc.Err() != nil {
			rt.forget(addr, cc)
		}
	})
	return cc, nil
}

// stdTLSStateConn presents a utls connection's state as a crypto/tls
// ConnectionState. utls returns its own ConnectionState type, and net/http reads
// the negotiated ALPN protocol only from a conn with *tls.Conn's exact method
// signature (the http.Transport.DialTLSContext contract).
type stdTLSStateConn struct {
	*utls.UConn
}

func (c stdTLSStateConn) ConnectionState() tls.ConnectionState {
	s := c.UConn.ConnectionState()
	return tls.ConnectionState{
		Version:                     s.Version,
		HandshakeComplete:           s.HandshakeComplete,
		DidResume:                   s.DidResume,
		CipherSuite:                 s.CipherSuite,
		NegotiatedProtocol:          s.NegotiatedProtocol,
		ServerName:                  s.ServerName,
		PeerCertificates:            s.PeerCertificates,
		VerifiedChains:              s.VerifiedChains,
		SignedCertificateTimestamps: s.SignedCertificateTimestamps,
		OCSPResponse:                s.OCSPResponse,
		ECHAccepted:                 s.ECHAccepted,
	}
}

func (rt *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	for retried := false; ; retried = true {
		cc := rt.pooled(addr)
		reused := cc != nil
		if cc == nil {
			var uconn *utls.UConn
			var err error
			cc, uconn, err = rt.connect(ctx, host, addr)
			if err != nil {
				return nil, err
			}
			if uconn != nil {
				return roundTripH1(req, uconn)
			}
		}

		// At the server's stream limit this waits for a free stream (or for ctx).
		resp, err := cc.RoundTrip(req)
		if err == nil {
			// The connection stays pooled; the response body does NOT close it.
			return resp, nil
		}
		// The request's own deadline or cancellation is not the connection's
		// fault: report it as such, keep the connection, and do not redial with
		// a context that is already done.
		if cerr := ctx.Err(); cerr != nil {
			if errors.Is(err, cerr) {
				return nil, err
			}
			return nil, cerr
		}
		// Anything else and the connection is suspect. Other requests may still
		// be running on it, so it is retired, not closed under them.
		rt.retire(addr, cc)

		// A failure on a reused connection usually means the server closed it
		// since last use; retry once on a fresh one (safe: page fetches are GETs
		// or carry a rewindable GetBody).
		if !reused || retried {
			return nil, err
		}
		if req.Body != nil && req.Body != http.NoBody {
			if req.GetBody == nil {
				return nil, err
			}
			b, gerr := req.GetBody()
			if gerr != nil {
				return nil, err
			}
			req.Body = b
		}
	}
}

// roundTripH1 writes the request and reads the response over the raw TLS conn,
// which the response body then owns.
func roundTripH1(req *http.Request, uconn *utls.UConn) (*http.Response, error) {
	if err := req.Write(uconn); err != nil {
		_ = uconn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(uconn), req)
	if err != nil {
		_ = uconn.Close()
		return nil, err
	}
	resp.Body = &closeConnBody{ReadCloser: resp.Body, conn: uconn}
	return resp, nil
}

// dial opens a TCP connection through the (SSRF-guarded) dialer and completes
// the Chrome-like TLS handshake on it.
func (rt *utlsRoundTripper) dial(ctx context.Context, host, addr string) (*utls.UConn, error) {
	raw, err := rt.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	uconn := utls.UClient(raw, &utls.Config{ServerName: host, InsecureSkipVerify: rt.insecure}, rt.helloID)
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return uconn, nil
}

// dialCall is one in-progress dial + handshake for an address, shared by every
// request that arrives for that address while it runs. Fields are guarded by
// utlsRoundTripper.mu; the result fields are set before done is closed.
type dialCall struct {
	done    chan struct{}
	cancel  context.CancelFunc // aborts the dial; called once nobody waits for it
	waiters int

	// At most one is set once done is closed. cc is an h2 connection, already
	// pooled, for every waiter to share. uconn is a connection that negotiated
	// HTTP/1.1, which only one request can use: the first waiter to take it gets
	// it (and nils the field), the rest dial their own.
	cc    *http.ClientConn
	uconn *utls.UConn
	err   error
}

// connect returns a connection for a request that found none pooled: either an
// h2 connection (shared, pooled) or an HTTP/1.1 connection for this request
// alone. Concurrent callers for one address share a single dial + handshake.
//
// The shared dial runs on its own context, not any caller's. A caller whose ctx
// ends stops waiting and returns ctx's error while the dial carries on for the
// others; it is aborted only when its last waiter has gone. A failed dial fails
// each of its waiters once and is not remembered: the next request dials anew.
func (rt *utlsRoundTripper) connect(ctx context.Context, host, addr string) (*http.ClientConn, *utls.UConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	rt.mu.Lock()
	// A dial may have finished since the caller looked at the pool.
	if cc := rt.conns[addr]; cc != nil && connState(cc) != connDead {
		rt.mu.Unlock()
		return cc, nil, nil
	}
	call := rt.dials[addr]
	if call == nil {
		dialCtx, cancel := context.WithTimeout(context.Background(), mimicDialTimeout)
		call = &dialCall{done: make(chan struct{}), cancel: cancel}
		rt.dials[addr] = call
		go rt.runDial(dialCtx, call, host, addr)
	}
	call.waiters++
	rt.mu.Unlock()

	var ctxErr error
	select {
	case <-call.done:
	case <-ctx.Done():
		ctxErr = ctx.Err()
	}

	rt.mu.Lock()
	call.waiters--
	if ctxErr != nil {
		// Leaving early. If nobody else is waiting, stop the dial (a later
		// request starts its own) and close an h1 connection nobody will take.
		var orphan *utls.UConn
		if call.waiters == 0 {
			call.cancel()
			if rt.dials[addr] == call {
				delete(rt.dials, addr)
			}
			orphan, call.uconn = call.uconn, nil
		}
		rt.mu.Unlock()
		if orphan != nil {
			_ = orphan.Close()
		}
		return nil, nil, ctxErr
	}
	cc, uconn, err := call.cc, call.uconn, call.err
	call.uconn = nil
	rt.mu.Unlock()

	switch {
	case err != nil:
		return nil, nil, err
	case cc != nil:
		return cc, nil, nil
	case uconn != nil:
		return nil, uconn, nil
	}

	// The server speaks HTTP/1.1 and another waiter took the shared connection:
	// dial one for this request, as h1 always does.
	uconn, err = rt.dial(ctx, host, addr)
	if err != nil {
		return nil, nil, err
	}
	if uconn.ConnectionState().NegotiatedProtocol != "h2" {
		return nil, uconn, nil
	}
	// The server picked h2 this time after all.
	cc, err = rt.adoptConn(addr, uconn)
	if err != nil {
		_ = uconn.Close()
		return nil, nil, err
	}
	rt.store(addr, cc)
	return cc, nil, nil
}

// runDial performs call's dial + handshake and publishes the result.
func (rt *utlsRoundTripper) runDial(ctx context.Context, call *dialCall, host, addr string) {
	defer call.cancel()

	var cc *http.ClientConn
	uconn, err := rt.dial(ctx, host, addr)
	if err == nil && uconn.ConnectionState().NegotiatedProtocol == "h2" {
		if cc, err = rt.adoptConn(addr, uconn); err != nil {
			_ = uconn.Close()
		}
		uconn = nil
	}

	rt.mu.Lock()
	if rt.dials[addr] == call {
		delete(rt.dials, addr)
	}
	var old *http.ClientConn
	if cc != nil {
		old = rt.conns[addr]
		rt.conns[addr] = cc
	}
	// If every waiter gave up, nobody is left to use an h1 connection. (An h2
	// connection is pooled regardless and idles out if never used.)
	var orphan *utls.UConn
	if call.waiters == 0 {
		orphan, uconn = uconn, nil
	}
	call.cc, call.uconn, call.err = cc, uconn, err
	close(call.done)
	rt.mu.Unlock()

	if orphan != nil {
		_ = orphan.Close()
	}
	if old != nil {
		closeWhenIdle(old)
	}
}

// connStateKind classifies a pooled connection.
type connStateKind int

const (
	connReady connStateKind = iota // can start a request now
	connBusy                       // has requests in flight and no free stream
	connDead                       // closed, or going away with nothing in flight
)

// connState reports whether cc can serve another request.
//
// http.ClientConn.Available is 0 both for a connection that is finished (closed,
// idled out, GOAWAY received) and for a healthy one that is merely at the
// server's MAX_CONCURRENT_STREAMS. InFlight tells them apart: a connection with
// no free stream but requests in flight is busy — RoundTrip on it queues for a
// stream (or, if it is in fact draining after a GOAWAY, fails at once without
// sending anything, which RoundTrip's retry handles).
func connState(cc *http.ClientConn) connStateKind {
	switch {
	case cc.Err() != nil:
		return connDead
	case cc.Available() > 0:
		return connReady
	case cc.InFlight() > 0:
		return connBusy
	default:
		return connDead
	}
}

// pooled returns the pooled h2 connection for addr if it is usable (possibly
// busy), discarding a dead one.
func (rt *utlsRoundTripper) pooled(addr string) *http.ClientConn {
	rt.mu.Lock()
	cc := rt.conns[addr]
	dead := cc != nil && connState(cc) == connDead
	if dead {
		delete(rt.conns, addr)
	}
	rt.mu.Unlock()
	if dead {
		closeWhenIdle(cc) // releases the socket if it is still open
		return nil
	}
	return cc
}

// store pools cc for addr, retiring any connection it replaces.
func (rt *utlsRoundTripper) store(addr string, cc *http.ClientConn) {
	rt.mu.Lock()
	old := rt.conns[addr]
	rt.conns[addr] = cc
	rt.mu.Unlock()
	if old != nil && old != cc {
		closeWhenIdle(old)
	}
}

// forget removes cc from the pool if it is still the pooled conn for addr.
func (rt *utlsRoundTripper) forget(addr string, cc *http.ClientConn) {
	rt.mu.Lock()
	if rt.conns[addr] == cc {
		delete(rt.conns, addr)
	}
	rt.mu.Unlock()
}

// retire takes cc out of the pool, so no new request uses it, and closes it once
// the requests already on it have finished.
func (rt *utlsRoundTripper) retire(addr string, cc *http.ClientConn) {
	rt.forget(addr, cc)
	closeWhenIdle(cc)
}

// closeWhenIdle closes cc as soon as it has no request in flight — immediately
// if it has none now. Closing a ClientConn interrupts its outstanding requests,
// so a connection that may be shared is never closed directly.
func closeWhenIdle(cc *http.ClientConn) {
	cc.SetStateHook(func(cc *http.ClientConn) {
		if cc.Err() == nil && cc.InFlight() == 0 {
			// Off the hook's goroutine: it can be net/http's own.
			go cc.Close()
		}
	})
	if cc.InFlight() == 0 {
		cc.Close()
	}
}

// CloseIdleConnections closes pooled connections with no request in flight
// (http.Client.CloseIdleConnections reaches it).
func (rt *utlsRoundTripper) CloseIdleConnections() {
	var idle []*http.ClientConn
	rt.mu.Lock()
	for addr, cc := range rt.conns {
		if cc.InFlight() == 0 {
			delete(rt.conns, addr)
			idle = append(idle, cc)
		}
	}
	rt.mu.Unlock()
	for _, cc := range idle {
		closeWhenIdle(cc)
	}
}

// closeConnBody closes the underlying connection when the response body is
// closed. Used only on the (unpooled) HTTP/1.1 path.
type closeConnBody struct {
	io.ReadCloser
	conn io.Closer
}

func (b *closeConnBody) Close() error {
	err := b.ReadCloser.Close()
	_ = b.conn.Close()
	return err
}
