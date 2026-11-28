//go:build corpus

package corpus

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Proxy is an archive-backed forward proxy: it lets a tool that drives a real
// browser (which cannot take an in-process transport) be served the same
// capture, through the same matcher, as the in-process replay. Every request —
// plain HTTP, or HTTPS inside a CONNECT tunnel it terminates itself — is handed
// to the current http.RoundTripper (a page's replay); nothing is ever forwarded
// to the network.
//
// HTTPS interception uses a throwaway in-memory CA whose key never leaves the
// process, so the browser has to be started with certificate checks off
// (--ignore-certificate-errors). That is acceptable only because the proxy
// serves recorded bytes and the browser is given no route to the live web.
type Proxy struct {
	ln    net.Listener
	outer *http.Server
	inner *http.Server
	conns *chanListener

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey

	mu         sync.Mutex
	rt         http.RoundTripper
	leaves     map[string]*tls.Certificate
	background int
}

// NewProxy starts the proxy on a loopback port. Until SetRoundTripper is called
// every request is refused.
func NewProxy() (*Proxy, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "unblink corpus replay (throwaway)"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{
		ln: ln, caCert: ca, caKey: key, leaves: map[string]*tls.Certificate{},
		conns: &chanListener{ch: make(chan net.Conn), done: make(chan struct{}), addr: ln.Addr()},
	}
	p.outer = &http.Server{Handler: http.HandlerFunc(p.serveOuter), ReadHeaderTimeout: 30 * time.Second}
	p.inner = &http.Server{Handler: http.HandlerFunc(p.serveTunnelled), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = p.outer.Serve(ln) }()
	go func() { _ = p.inner.Serve(p.conns) }()
	return p, nil
}

// Addr is the proxy's host:port.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// SetRoundTripper selects what answers requests from now on; nil refuses all.
func (p *Proxy) SetRoundTripper(rt http.RoundTripper) {
	p.mu.Lock()
	p.rt = rt
	p.background = 0
	p.mu.Unlock()
}

// Background reports how many requests since the last SetRoundTripper were the
// browser's own housekeeping (update checks, variations, safe browsing) rather
// than the page's. They are refused without reaching the replay, so they are
// never counted as the page's unmatched requests.
func (p *Proxy) Background() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.background
}

// Close stops the proxy and drops every open connection.
func (p *Proxy) Close() {
	_ = p.outer.Close()
	_ = p.inner.Close()
	p.conns.Close()
}

// browserBackgroundRE matches hosts a Chromium build contacts on its own
// account. The page did not ask for them, so they are not replay misses.
var browserBackgroundRE = regexp.MustCompile(`(^|\.)(` +
	`clients[0-9]*\.google\.com|clientservices\.googleapis\.com|update\.googleapis\.com|` +
	`optimizationguide-pa\.googleapis\.com|safebrowsing\.googleapis\.com|safebrowsingohttpgateway\.googleapis\.com|` +
	`content-autofill\.googleapis\.com|accounts\.google\.com|edgedl\.me\.gvt1\.com|redirector\.gvt1\.com|` +
	`dl\.google\.com|android\.clients\.google\.com|passwordsleakcheck-pa\.googleapis\.com|` +
	`chromewebstore\.googleapis\.com|privacysandbox-pa\.googleapis\.com` +
	`)$`)

// browserInitiated reports whether a request is the browser's own rather than
// the page's. Besides the known hosts, Fetch Metadata tells them apart: every
// request a document makes carries the Sec-Fetch-Site of its initiator, and
// only a browser-initiated request is "none" — which for a page means the
// top-level navigation alone (mode "navigate"). Chromium's background fetches
// (e.g. www.google.com/async/folae) are "none" with mode "no-cors".
func browserInitiated(r *http.Request) bool {
	if browserBackgroundRE.MatchString(strings.ToLower(r.URL.Hostname())) {
		return true
	}
	return strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "none") &&
		!strings.EqualFold(r.Header.Get("Sec-Fetch-Mode"), "navigate")
}

func (p *Proxy) serveOuter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		if !r.URL.IsAbs() {
			http.Error(w, "corpus replay proxy: not an origin server", http.StatusBadRequest)
			return
		}
		p.answer(w, r)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "443"
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "corpus replay proxy: cannot tunnel", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	tc := tls.Server(conn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			return p.leaf(name)
		},
	})
	p.conns.push(&tunnelConn{Conn: tc, port: port})
}

// serveTunnelled handles a request read off a terminated CONNECT tunnel: an
// origin-form request whose scheme and authority are the tunnel's.
func (p *Proxy) serveTunnelled(w http.ResponseWriter, r *http.Request) {
	r.URL.Scheme = "https"
	r.URL.Host = r.Host
	p.answer(w, r)
}

// abort drops the connection without a response, which the browser reports as a
// network error — what an unanswerable request is to the in-process replay too.
func abort() { panic(http.ErrAbortHandler) }

func (p *Proxy) answer(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	rt := p.rt
	if browserInitiated(r) {
		p.background++
		rt = nil
	}
	p.mu.Unlock()
	if rt == nil {
		abort()
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		abort()
	}
	out.Header = r.Header.Clone()
	resp, err := rt.RoundTrip(out)
	if err != nil {
		abort()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		abort()
	}
	h := w.Header()
	for k, vs := range resp.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	noBody := r.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusNotModified || resp.StatusCode < 200
	if !noBody {
		h.Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(resp.StatusCode)
	if !noBody {
		_, _ = w.Write(body)
	}
}

// hopByHop are headers that describe the recorded connection, not the
// resource. Alt-Svc is dropped with them: it would invite the browser to
// retry over HTTP/3, which the proxy does not speak.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"Alt-Svc": true, "Content-Length": true,
}

func (p *Proxy) leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || len(host) > 253 {
		return nil, errors.New("corpus replay proxy: no server name")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.leaves[host]; c != nil {
		return c, nil
	}
	if len(p.leaves) >= 4096 {
		return nil, errors.New("corpus replay proxy: too many hosts")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(len(p.leaves) + 2)),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, fmt.Errorf("corpus replay proxy: leaf for %s: %w", host, err)
	}
	c := &tls.Certificate{Certificate: [][]byte{der, p.caCert.Raw}, PrivateKey: key}
	p.leaves[host] = c
	return c, nil
}

// tunnelConn is a terminated CONNECT tunnel.
type tunnelConn struct {
	net.Conn
	port string
}

// chanListener feeds terminated tunnels to the inner HTTP server.
type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
	addr net.Addr
}

func (l *chanListener) push(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }
