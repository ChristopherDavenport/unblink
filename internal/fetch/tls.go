package fetch

import (
	"bufio"
	"io"
	"net"
	"net/http"

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
// real CDNs negotiate h2, so we must handle both). It does not pool connections —
// one TLS conn per request — which is acceptable for unblink's occasional fetches
// plus its page cache. The SSRF Control dialer is reused for the TCP dial, so the
// guard still applies.
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
	}
}

func (rt *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}

	raw, err := rt.dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
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
		resp, err := cc.RoundTrip(req)
		if err != nil {
			_ = cc.Close()
			return nil, err
		}
		resp.Body = &closeConnBody{ReadCloser: resp.Body, conn: cc}
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

// closeConnBody closes the underlying connection when the response body is closed,
// since this transport does not pool connections.
type closeConnBody struct {
	io.ReadCloser
	conn io.Closer
}

func (b *closeConnBody) Close() error {
	err := b.ReadCloser.Close()
	_ = b.conn.Close()
	return err
}
