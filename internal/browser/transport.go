package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/js"
)

// guardedTransport implements js.Transport for in-page JavaScript requests
// (external scripts, fetch, XHR). It shares the page client's cookie jar, blocks
// requests to private/loopback/metadata addresses (SSRF guard, via the underlying
// client's dial control), and enforces a per-render request budget.
type guardedTransport struct {
	client *fetch.Client
	max    int32 // monotonic per-render cap (one-shot); 0 = no per-render cap
	count  int32
	denied int32 // requests rejected by the budget/window caps, for render diagnostics

	// Rolling fixed-window cap for persistent (live) sessions, where a monotonic
	// per-render count doesn't fit: at most windowMax requests per window. Bounds
	// slow exfiltration/scan by a long-lived page without a hard lifetime cap.
	windowMax int
	window    time.Duration
	mu        sync.Mutex
	winStart  time.Time
	winCount  int
}

// Do enforces the request budget and forwards to the guarded fetch client.
func (g *guardedTransport) Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (*js.Response, error) {
	if g.max > 0 && atomic.AddInt32(&g.count, 1) > g.max {
		atomic.AddInt32(&g.denied, 1)
		return nil, fmt.Errorf("unblink: JavaScript request budget exceeded (max %d)", g.max)
	}
	if g.windowMax > 0 && !g.allowWindow() {
		atomic.AddInt32(&g.denied, 1)
		return nil, fmt.Errorf("unblink: JavaScript request rate exceeded (max %d per %s)", g.windowMax, g.window)
	}
	res, err := g.client.Fetch(ctx, method, url, headers, body)
	if err != nil {
		return nil, err
	}
	hdr := make(map[string]string, len(res.Header))
	for k := range res.Header {
		hdr[strings.ToLower(k)] = res.Header.Get(k)
	}
	return &js.Response{Status: res.Status, Headers: hdr, Body: res.Body, FinalURL: res.FinalURL}, nil
}

// Denied reports how many requests the budget/window caps rejected, for render
// diagnostics ("the page wanted more network than the per-render budget allows").
func (g *guardedTransport) Denied() int {
	return int(atomic.LoadInt32(&g.denied))
}

// allowWindow reports whether a request fits within the current fixed window,
// resetting the window when it has elapsed. Safe for concurrent callers (async
// fetches run off the loop goroutine).
func (g *guardedTransport) allowWindow() bool {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.winStart.IsZero() || now.Sub(g.winStart) >= g.window {
		g.winStart = now
		g.winCount = 0
	}
	g.winCount++
	return g.winCount <= g.windowMax
}

// newRenderTransport builds the guarded transport for one render, sharing the
// active page client's cookie jar so JS requests carry session cookies. reqTimeout is
// the per-request HTTP timeout: normally b.jsReqTimeout, but a wait_timeout override
// raises it so a slow in-page fetch the render is waiting for isn't cut short.
func (b *Browser) newRenderTransport(client *fetch.Client, reqTimeout time.Duration) js.Transport {
	if reqTimeout < b.jsReqTimeout {
		reqTimeout = b.jsReqTimeout
	}
	opts := []fetch.Option{
		fetch.WithJar(client.Jar()),
		fetch.WithTimeout(reqTimeout),
		fetch.WithRateLimiter(b.limiter),
		fetch.WithRetries(b.retries),
		// The shared pool carries the SSRF dial guard and keeps subrequest
		// connections reusable across renders (a fresh client per render used
		// to mean a fresh pool and a TCP+TLS handshake per script).
		fetch.WithSharedTransport(b.jsRT),
	}
	// Carry the page client's origin-scoped credentials so same-origin in-page
	// fetch/XHR authenticate (cross-origin subrequests are excluded by the scope).
	opts = append(opts, client.CredentialOptions()...)
	gc, err := fetch.New(opts...)
	if err != nil {
		return nil
	}
	return &guardedTransport{client: gc, max: int32(b.jsMaxRequests)}
}

// newLiveTransport builds the guarded transport for a persistent per-session
// runtime. Unlike a one-shot render, a live page runs for the session's lifetime,
// so the per-render request count cap doesn't fit; abuse is bounded instead by the
// SSRF dial guard, the per-host rate limiter, and the session idle TTL/cap.
func (b *Browser) newLiveTransport(client *fetch.Client) js.Transport {
	opts := []fetch.Option{
		fetch.WithJar(client.Jar()),
		fetch.WithTimeout(b.jsReqTimeout),
		fetch.WithRateLimiter(b.limiter),
		fetch.WithRetries(b.retries),
		fetch.WithSharedTransport(b.jsRT), // SSRF-guarded shared pool (see newRenderTransport)
	}
	// Carry the page client's origin-scoped credentials so same-origin in-page
	// fetch/XHR authenticate (cross-origin subrequests are excluded by the scope).
	opts = append(opts, client.CredentialOptions()...)
	gc, err := fetch.New(opts...)
	if err != nil {
		return nil
	}
	// No monotonic per-render cap (a live page runs for the session's lifetime), but a
	// rolling window bounds sustained abuse on top of the SSRF guard + rate limiter.
	return &guardedTransport{client: gc, windowMax: liveJSWindowMax, window: liveJSWindow}
}

// Rolling-window subrequest cap for persistent live sessions. Generous enough for a
// heavy SPA's initial burst, low enough to bound a malicious page's sustained
// beaconing/scanning below the per-host rate limiter over a long session.
const (
	liveJSWindow    = time.Minute
	liveJSWindowMax = 300
)

// errBlockedAddr is the SSRF guard's sentinel, wrapped into every dial rejection
// so error classification can identify a blocked private/metadata target through
// the net/http error chain.
var errBlockedAddr = errors.New("blocked non-public address")

// ssrfControl returns a dial-control hook that rejects connections to
// private/loopback/link-local/metadata addresses, or nil when private addresses
// are explicitly allowed.
func ssrfControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	if allowPrivate {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}
		if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
			return fmt.Errorf("unblink: %w: %s", errBlockedAddr, ip)
		}
		return nil
	}
}

// cookieAdapter implements js.CookieJar over an http.CookieJar, so document.cookie
// reads and writes the same cookies the page's HTTP requests use.
type cookieAdapter struct{ jar http.CookieJar }

// httpOnlyChecker is satisfied by the fetch package's tracking jar; it lets the
// adapter hide HttpOnly cookies from document.cookie without importing fetch.
type httpOnlyChecker interface {
	IsHTTPOnly(host, name string) bool
}

func (c cookieAdapter) Cookies(pageURL string) string {
	if c.jar == nil {
		return ""
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	cs := c.jar.Cookies(u)
	ho, _ := c.jar.(httpOnlyChecker)
	host := u.Hostname()
	parts := make([]string, 0, len(cs))
	for _, ck := range cs {
		// Hide HttpOnly cookies from page JS, as a real browser does — otherwise an
		// HttpOnly session token is readable via document.cookie and exfiltratable.
		if ho != nil && ho.IsHTTPOnly(host, ck.Name) {
			continue
		}
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}

func (c cookieAdapter) SetCookie(pageURL, cookie string) {
	if c.jar == nil {
		return
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return
	}
	ck, err := http.ParseSetCookie(cookie)
	if err != nil || ck == nil {
		return
	}
	ck.HttpOnly = false // document.cookie writes cannot create HttpOnly cookies
	c.jar.SetCookies(u, []*http.Cookie{ck})
}

// cgnatRange is the RFC 6598 carrier-grade-NAT / shared address space. net.IP's
// IsPrivate does not cover it, yet it commonly reaches provider infrastructure —
// notably Alibaba Cloud's metadata service at 100.100.100.200.
var cgnatRange = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// isBlockedIP reports whether an IP is in a range fetches (page or JavaScript) must
// not reach: loopback, RFC1918/ULA private, link-local incl. 169.254.169.254
// metadata, unspecified, multicast, plus CGNAT (Alibaba metadata) and the limited
// broadcast address.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() ||
		ip.Equal(net.IPv4bcast) ||
		(cgnatRange != nil && cgnatRange.Contains(ip))
}
