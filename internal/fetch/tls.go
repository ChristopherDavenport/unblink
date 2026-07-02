package fetch

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"sync"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// Chrome desktop HTTP/2 SETTINGS values (best-effort, as far as stock
// golang.org/x/net/http2 exposes). Re-check these when utls.HelloChrome_Auto
// advances to a newer Chrome; they are advertised in our client SETTINGS frame.
const (
	chromeH2MaxFrameSize      = 16384  // SETTINGS_MAX_FRAME_SIZE
	chromeH2HeaderTableSize   = 65536  // SETTINGS_HEADER_TABLE_SIZE (Go uses 4096 and omits it)
	chromeH2MaxHeaderListSize = 262144 // SETTINGS_MAX_HEADER_LIST_SIZE (Go advertises 10MiB)
)

// utlsRoundTripper presents a recent-Chrome TLS ClientHello (uTLS) — keeping a
// genuine Chrome JA3/JA4 — and dispatches to HTTP/2 or HTTP/1.1 based on the ALPN
// the server negotiates (a stock http.Transport.DialTLSContext can only do h1, and
// real CDNs negotiate h2, so we must handle both). The SSRF Control dialer is
// reused for the TCP dial, so the guard still applies.
//
// HTTP/2 connections are pooled per host:port and reused while the server keeps
// them open (a real browser never handshakes per request, and neither should the
// mimic — repeat fetches to one host skip the TCP+TLS round trips). A pooled
// connection that has died (GOAWAY, server close) is detected via
// CanTakeNewRequest/RoundTrip failure and replaced with a fresh dial. HTTP/1.1
// remains one connection per request — h1 servers are the rare case on the
// mimic path, and a portable h1 pool over a pre-established TLS conn is not
// worth the machinery.
//
// The h2 SETTINGS are best-effort tuned toward Chrome (see the consts above), so
// the HTTP/2 layer no longer fingerprints as Go's default. What stock
// golang.org/x/net/http2 still cannot control — and so remains Go's — is the
// SETTINGS frame *order*, INITIAL_WINDOW_SIZE, ENABLE_PUSH, the connection-level
// WINDOW_UPDATE, and the pseudo-header order; matching those needs a forked http2.
type utlsRoundTripper struct {
	dialer   *net.Dialer
	insecure bool // test-only
	helloID  utls.ClientHelloID
	h2       *http2.Transport

	mu    sync.Mutex
	conns map[string]*http2.ClientConn // pooled h2 connections by host:port
}

func newUTLSRoundTripper(dialer *net.Dialer, insecure bool) *utlsRoundTripper {
	return &utlsRoundTripper{
		dialer:   dialer,
		insecure: insecure,
		helloID:  utls.HelloChrome_Auto,
		h2: &http2.Transport{
			MaxReadFrameSize:          chromeH2MaxFrameSize,
			MaxDecoderHeaderTableSize: chromeH2HeaderTableSize,
			MaxHeaderListSize:         chromeH2MaxHeaderListSize,
		},
		conns: make(map[string]*http2.ClientConn),
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
		cc, err := rt.h2.NewClientConn(uconn)
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
func (rt *utlsRoundTripper) pooled(addr string) *http2.ClientConn {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	cc := rt.conns[addr]
	if cc == nil {
		return nil
	}
	if !cc.CanTakeNewRequest() {
		delete(rt.conns, addr)
		go cc.Close()
		return nil
	}
	return cc
}

// store pools cc for addr, closing any previous connection it replaces.
func (rt *utlsRoundTripper) store(addr string, cc *http2.ClientConn) {
	rt.mu.Lock()
	old := rt.conns[addr]
	rt.conns[addr] = cc
	rt.mu.Unlock()
	if old != nil && old != cc {
		go old.Close()
	}
}

// drop removes cc from the pool if it is still the pooled conn for addr.
func (rt *utlsRoundTripper) drop(addr string, cc *http2.ClientConn) {
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
