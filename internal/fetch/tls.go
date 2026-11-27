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
// HTTP/2 connections are pooled here, per host:port, and reused while the server
// keeps them open (a real browser never handshakes per request, and neither
// should the mimic — repeat fetches to one host skip the TCP+TLS round trips). A
// pooled connection that has died (GOAWAY, server close) is detected via
// Available/RoundTrip failure and replaced with a fresh dial. HTTP/1.1 remains
// one connection per request — h1 servers are the rare case on the mimic path,
// and a portable h1 pool over a pre-established TLS conn is not worth the
// machinery.
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

	mu    sync.Mutex
	conns map[string]*http.ClientConn // pooled h2 connections by host:port
}

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
		},
		conns: make(map[string]*http.ClientConn),
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
// HTTP/2 client connection. The context is detached from the request's: the
// connection is pooled and must outlive the request that happened to open it.
func (rt *utlsRoundTripper) adoptConn(ctx context.Context, addr string, uconn *utls.UConn) (*http.ClientConn, error) {
	ctx = context.WithValue(context.WithoutCancel(ctx), adoptKey{}, net.Conn(stdTLSStateConn{uconn}))
	return rt.h2.NewClientConn(ctx, "https", addr)
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

	// Reuse a pooled h2 connection when one is alive. A failure here usually
	// means the server closed it since last use; retry once on a fresh dial
	// (safe: page fetches are GETs or carry a rewindable GetBody).
	if cc := rt.pooled(addr); cc != nil {
		resp, err := cc.RoundTrip(req)
		if err == nil {
			return resp, nil
		}
		rt.drop(addr, cc)
		if req.Body != nil && req.GetBody == nil {
			return nil, err
		}
		if req.GetBody != nil {
			b, gerr := req.GetBody()
			if gerr != nil {
				return nil, err
			}
			req.Body = b
		}
	}

	raw, err := rt.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	uconn := utls.UClient(raw, &utls.Config{ServerName: host, InsecureSkipVerify: rt.insecure}, rt.helloID)
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}

	if uconn.ConnectionState().NegotiatedProtocol == "h2" {
		cc, err := rt.adoptConn(ctx, addr, uconn)
		if err != nil {
			_ = uconn.Close()
			return nil, err
		}
		rt.store(addr, cc)
		resp, err := cc.RoundTrip(req)
		if err != nil {
			rt.drop(addr, cc)
			_ = cc.Close()
			return nil, err
		}
		// The connection stays pooled; the response body does NOT close it.
		return resp, nil
	}

	// HTTP/1.1: write the request and read the response over the raw TLS conn.
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

// pooled returns a live pooled h2 connection for addr, discarding a dead one.
func (rt *utlsRoundTripper) pooled(addr string) *http.ClientConn {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	cc := rt.conns[addr]
	if cc == nil {
		return nil
	}
	// Available is 0 once the connection is closed or has seen GOAWAY, and also
	// while it is at the server's concurrent-stream limit.
	if cc.Available() <= 0 {
		delete(rt.conns, addr)
		go cc.Close()
		return nil
	}
	return cc
}

// store pools cc for addr, closing any previous connection it replaces.
func (rt *utlsRoundTripper) store(addr string, cc *http.ClientConn) {
	rt.mu.Lock()
	old := rt.conns[addr]
	rt.conns[addr] = cc
	rt.mu.Unlock()
	if old != nil && old != cc {
		go old.Close()
	}
}

// drop removes cc from the pool if it is still the pooled conn for addr.
func (rt *utlsRoundTripper) drop(addr string, cc *http.ClientConn) {
	rt.mu.Lock()
	if rt.conns[addr] == cc {
		delete(rt.conns, addr)
	}
	rt.mu.Unlock()
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
