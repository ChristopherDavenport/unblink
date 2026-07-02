// Package fetch is unblink's HTTP-client-as-browser. It performs the network
// request, follows redirects, manages cookies, and decodes the response body to
// UTF-8. It has no HTML knowledge beyond charset detection, and returns a
// *page.Page with only the transport fields and Raw populated.
package fetch

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/andybalholm/brotli"
	"golang.org/x/net/html/charset"
	"golang.org/x/net/publicsuffix"

	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/ratelimit"
)

// chromeMajor is the single source of truth for the Chrome version we imitate;
// DefaultUserAgent and the sec-ch-ua client hint both derive from it so they never
// contradict. Keep it aligned with the Chrome that utls HelloChrome_Auto emits.
const chromeMajor = "133"

// DefaultUserAgent is a realistic desktop UA (aligned with the Chrome major that
// utls HelloChrome_Auto emits). The stdlib default ("Go-http-client/1.1") is
// frequently blocked outright.
const DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/" + chromeMajor + ".0.0.0 Safari/537.36"

// chromeSecCHUA is the sec-ch-ua client hint matching DefaultUserAgent's platform
// and version. Sent only on the --tls-mimic path (see setChromeMimicHeaders).
const chromeSecCHUA = `"Not(A:Brand";v="99", "Google Chrome";v="` + chromeMajor + `", "Chromium";v="` + chromeMajor + `"`

// DefaultTimeout bounds a single request (including body read).
const DefaultTimeout = 30 * time.Second

// DefaultRetries is the default number of retries for transient failures.
const DefaultRetries = 2

// DefaultMaxBytes caps the decompressed response body. It is generous enough for
// large PDFs while bounding memory and decompression bombs. fetch is the only
// correct home for a download size cap.
const DefaultMaxBytes = 10 << 20 // 10 MiB

// Client fetches pages. It is safe for concurrent use. Each Client owns one
// cookie jar, so a per-session Client gives a session its own cookies.
type Client struct {
	http        *http.Client
	userAgent   string
	limiter     *ratelimit.Limiter
	maxRetries  int
	maxBytes    int64
	dialControl func(network, address string, c syscall.RawConn) error
	tlsMimic    bool
	tlsInsecure bool // test-only: skip cert verification on the utls path

	// Injected credentials, scoped to a single origin (credOrigin, "scheme://host").
	// They are added only to requests whose origin matches, and stripped on any
	// cross-origin redirect, so a bearer token or custom header never leaks to
	// another host. credOrigin is empty when no credentials are configured.
	extraHeaders map[string]string
	authHeader   string // precomputed Authorization value ("Bearer …" / "Basic …")
	credOrigin   string
}

// Option configures a Client.
type Option func(*Client)

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithHeaders adds custom default headers sent on every request to the credential
// origin (see WithCredentialScope). They are stripped on cross-origin redirects.
// Accept-Encoding cannot be overridden — fetch owns response decoding.
func WithHeaders(h map[string]string) Option {
	return func(c *Client) {
		if len(h) == 0 {
			return
		}
		if c.extraHeaders == nil {
			c.extraHeaders = make(map[string]string, len(h))
		}
		for k, v := range h {
			c.extraHeaders[k] = v
		}
	}
}

// WithBearer sets an Authorization: Bearer header on requests to the credential
// origin (see WithCredentialScope).
func WithBearer(token string) Option {
	return func(c *Client) {
		if token != "" {
			c.authHeader = "Bearer " + token
		}
	}
}

// WithBasicAuth sets an Authorization: Basic header on requests to the credential
// origin (see WithCredentialScope).
func WithBasicAuth(user, pass string) Option {
	return func(c *Client) {
		c.authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
}

// WithCredentialScope pins injected credentials (WithHeaders/WithBearer/
// WithBasicAuth) to a single origin, normalized to "scheme://host". Credentials
// are added only to requests to this origin and dropped on cross-origin redirects.
// Without a scope, configured credentials are never sent (fail-closed).
func WithCredentialScope(origin string) Option {
	return func(c *Client) { c.credOrigin = normalizeOrigin(origin) }
}

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.http.Timeout = d } }

// WithJar sets a cookie jar. By default New creates an in-memory
// public-suffix-aware jar.
func WithJar(jar http.CookieJar) Option { return func(c *Client) { c.http.Jar = jar } }

// WithDialControl installs a net.Dialer.Control hook (called with the resolved
// dial address) so a client can reject connections to disallowed IPs — the SSRF
// guard. Checking the resolved address makes it robust against DNS rebinding.
func WithDialControl(control func(network, address string, c syscall.RawConn) error) Option {
	return func(c *Client) { c.dialControl = control }
}

// WithTLSMimic enables a browser-like TLS ClientHello (utls) to avoid being
// flagged by anti-bot fingerprinting.
func WithTLSMimic(enabled bool) Option { return func(c *Client) { c.tlsMimic = enabled } }

// WithRateLimiter shares a per-host rate limiter across clients.
func WithRateLimiter(l *ratelimit.Limiter) Option { return func(c *Client) { c.limiter = l } }

// WithRetries sets the number of retries for transient failures.
func WithRetries(n int) Option {
	return func(c *Client) {
		if n >= 0 {
			c.maxRetries = n
		}
	}
}

// WithMaxBytes caps the decompressed response body size. A body larger than the
// cap fails the fetch rather than being silently truncated. Non-positive values
// are ignored (the default cap stays in effect).
func WithMaxBytes(n int64) Option {
	return func(c *Client) {
		if n > 0 {
			c.maxBytes = n
		}
	}
}

// New returns a Client with its own cookie jar and sane browser-like defaults.
func New(opts ...Option) (*Client, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("fetch: new cookie jar: %w", err)
	}
	c := &Client{
		http:       &http.Client{Jar: jar, Timeout: DefaultTimeout},
		userAgent:  DefaultUserAgent,
		maxRetries: DefaultRetries,
		maxBytes:   DefaultMaxBytes,
	}
	for _, opt := range opts {
		opt(c)
	}
	// Wrap the final jar (default or WithJar-supplied) so HttpOnly cookies can be
	// hidden from document.cookie; idempotent when a client shares another's jar.
	c.http.Jar = wrapTrackingJar(c.http.Jar)
	c.http.Transport = c.buildTransport()
	c.http.CheckRedirect = c.checkRedirect
	return c, nil
}

// checkRedirect enforces the standard redirect limit and strips origin-scoped
// credentials when a redirect leaves the credential origin. The stdlib already
// drops Authorization/Cookie on cross-*domain* hops, but not custom headers and
// not same-registered-domain host changes; this closes both gaps.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("fetch: stopped after 10 redirects")
	}
	if !c.originMatches(req.URL) {
		req.Header.Del("Authorization")
		for k := range c.extraHeaders {
			req.Header.Del(k)
		}
	}
	return nil
}

// buildTransport assembles the HTTP transport from the client's config. With TLS
// mimicry it returns a uTLS RoundTripper (h1/h2 dispatch on negotiated ALPN);
// otherwise the stock transport with the SSRF dial guard.
func (c *Client) buildTransport() http.RoundTripper {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: c.dialControl}
	if c.tlsMimic {
		return newUTLSRoundTripper(dialer, c.tlsInsecure)
	}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// Get fetches rawURL and returns a *page.Page with the transport fields and Raw
// (the UTF-8-decoded body) populated. It does not parse HTML.
func (c *Client) Get(ctx context.Context, rawURL string) (*page.Page, error) {
	return c.GetConditional(ctx, rawURL, "", "")
}

// GetConditional fetches rawURL like Get, but performs an HTTP conditional
// request using cache validators from a previously fetched copy: etag becomes
// If-None-Match and lastModified becomes If-Modified-Since (either may be
// empty). When the origin answers 304 Not Modified, the returned page carries
// StatusCode == http.StatusNotModified and no body — the caller keeps its
// cached copy. With both validators empty it is exactly Get.
func (c *Client) GetConditional(ctx context.Context, rawURL, etag, lastModified string) (*page.Page, error) {
	reqURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: parse url %q: %w", rawURL, err)
	}
	if reqURL.Scheme != "http" && reqURL.Scheme != "https" {
		return nil, fmt.Errorf("fetch: unsupported scheme %q (want http/https)", reqURL.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: build request: %w", err)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	return c.do(req)
}

// Submit submits an HTML form url-encoded. For GET the values become the query
// string; for POST they are sent url-encoded as the body. Forms declaring
// enctype=multipart/form-data (and any file upload) go through SubmitMultipart.
// Redirects and cookies are handled by the underlying http.Client.
func (c *Client) Submit(ctx context.Context, method, action string, values url.Values) (*page.Page, error) {
	actionURL, err := parseActionURL(action)
	if err != nil {
		return nil, err
	}

	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}

	var req *http.Request
	switch method {
	case http.MethodGet:
		actionURL.RawQuery = values.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, actionURL.String(), nil)
	case http.MethodPost:
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, actionURL.String(), strings.NewReader(values.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	default:
		return nil, fmt.Errorf("fetch: unsupported form method %q", method)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch: build request: %w", err)
	}
	return c.do(req)
}

// FilePart is one file in a multipart form submission. Data is content supplied
// by the caller — unblink never reads a file from local disk for an upload, so a
// page (or a manipulated agent) cannot exfiltrate server-side files.
type FilePart struct {
	Field    string // form field name the file is attached to
	Filename string
	MIME     string // optional; defaults to application/octet-stream
	Data     []byte
}

// SubmitMultipart submits a form as multipart/form-data: text fields first (in
// sorted-key order, for determinism), then file parts. Multipart is POST-only —
// that is the only method the HTML spec gives it meaning for.
func (c *Client) SubmitMultipart(ctx context.Context, action string, values url.Values, files []FilePart) (*page.Page, error) {
	actionURL, err := parseActionURL(action)
	if err != nil {
		return nil, err
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range values[k] {
			if err := w.WriteField(k, v); err != nil {
				return nil, fmt.Errorf("fetch: multipart field %q: %w", k, err)
			}
		}
	}
	for _, f := range files {
		if f.Field == "" {
			return nil, fmt.Errorf("fetch: multipart file part is missing its form field name")
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`,
			escapeQuotes(f.Field), escapeQuotes(f.Filename)))
		mimeType := f.MIME
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		h.Set("Content-Type", mimeType)
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, fmt.Errorf("fetch: multipart file %q: %w", f.Field, err)
		}
		if _, err := part.Write(f.Data); err != nil {
			return nil, fmt.Errorf("fetch: multipart file %q: %w", f.Field, err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("fetch: finalize multipart body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, actionURL.String(), bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("fetch: build request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return c.do(req)
}

// escapeQuotes matches mime/multipart's quoting of names in Content-Disposition.
func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s)
}

// parseActionURL validates a form action URL (http/https only).
func parseActionURL(action string) (*url.URL, error) {
	actionURL, err := url.Parse(action)
	if err != nil {
		return nil, fmt.Errorf("fetch: parse action %q: %w", action, err)
	}
	if actionURL.Scheme != "http" && actionURL.Scheme != "https" {
		return nil, fmt.Errorf("fetch: unsupported scheme %q (want http/https)", actionURL.Scheme)
	}
	return actionURL, nil
}

// Jar returns the client's cookie jar, so a separate (e.g. SSRF-guarded) client
// can share the same session cookies for in-page subrequests.
func (c *Client) Jar() http.CookieJar { return c.http.Jar }

// Result is a raw HTTP response (body decoded to UTF-8). It is used for in-page
// JavaScript subrequests (external scripts, fetch, XHR) that want bytes rather
// than a parsed page.
type Result struct {
	Status   int
	Header   http.Header
	Body     []byte
	FinalURL string
}

// Fetch performs an arbitrary request and returns the raw decoded response. It is
// the low-level primitive behind JavaScript networking; the caller is responsible
// for any policy (SSRF guard, budget).
func (c *Client) Fetch(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (*Result, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("fetch: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	status, header, respBody, finalURL, err := c.roundTrip(req)
	if err != nil {
		return nil, err
	}
	fu := ""
	if finalURL != nil {
		fu = finalURL.String()
	}
	return &Result{Status: status, Header: header, Body: respBody, FinalURL: fu}, nil
}

// do sends req and decodes the response into a *page.Page. The body is
// charset-decoded to UTF-8 for textual content types and left as raw
// decompressed bytes otherwise.
func (c *Client) do(req *http.Request) (*page.Page, error) {
	status, header, body, finalURL, err := c.roundTrip(req)
	if err != nil {
		return nil, err
	}
	return &page.Page{
		RequestURL:  req.URL,
		FinalURL:    finalURL,
		StatusCode:  status,
		Header:      header,
		ContentType: header.Get("Content-Type"),
		Raw:         body,
		FetchedAt:   time.Now(),
	}, nil
}

// roundTrip sets browser-like headers, performs the request (with per-host rate
// limiting and retry-with-backoff), decompresses (gzip/brotli/deflate), and
// decodes the body to UTF-8.
func (c *Client) roundTrip(req *http.Request) (int, http.Header, []byte, *url.URL, error) {
	c.setHeaders(req)
	ctx := req.Context()
	start := time.Now()

	var resp *http.Response
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := c.limiter.Wait(ctx, req.URL.Hostname()); err != nil {
			return 0, nil, nil, nil, err
		}
		if attempt > 0 && req.GetBody != nil {
			if b, err := req.GetBody(); err == nil {
				req.Body = b
			}
		}
		resp, lastErr = c.http.Do(req)
		if !c.shouldRetry(attempt, resp, lastErr) {
			break
		}
		wait := backoffDelay(attempt, resp)
		if resp != nil {
			_ = resp.Body.Close()
		}
		slog.Warn("fetch retry", "url", redactURL(req.URL), "attempt", attempt+1, "wait", wait.String())
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return 0, nil, nil, nil, ctx.Err()
		}
	}
	if lastErr != nil {
		slog.Warn("fetch error", "method", req.Method, "url", redactURL(req.URL), "err", lastErr.Error())
		return 0, nil, nil, nil, fmt.Errorf("fetch: %s %s: %w", req.Method, redactURL(req.URL), lastErr)
	}
	defer resp.Body.Close()

	decoded, err := decodeBody(resp)
	if err != nil {
		return 0, nil, nil, nil, fmt.Errorf("fetch: decompress: %w", err)
	}
	// Read the decompressed body into memory under a hard cap (reading one byte
	// past the limit lets us detect an over-cap body). Capping here bounds memory
	// and stops the decompressor from being pulled indefinitely (decompression
	// bombs), and it is content-type-agnostic.
	limit := c.maxBytes
	raw, err := io.ReadAll(io.LimitReader(decoded, limit+1))
	if err != nil {
		return 0, nil, nil, nil, fmt.Errorf("fetch: read body: %w", err)
	}
	if int64(len(raw)) > limit {
		return 0, nil, nil, nil, fmt.Errorf("fetch: response body exceeds max bytes (%d)", limit)
	}
	if len(raw) == 0 {
		// Empty body (e.g. a HEAD request or 204 No Content): nothing to decode.
		return resp.StatusCode, resp.Header, nil, resp.Request.URL, nil
	}
	// Only charset-decode textual bodies. Running charset.NewReader over a PDF,
	// image, or other binary transcodes it as if it were text and corrupts the
	// bytes — so binary passes through untouched (see isTextualResponse).
	ct := resp.Header.Get("Content-Type")
	body := raw
	if isTextualResponse(ct, raw) {
		if r, cerr := charset.NewReader(bytes.NewReader(raw), ct); cerr == nil {
			if b, rerr := io.ReadAll(r); rerr == nil {
				body = b
			}
		}
		// On any charset error, keep the raw bytes rather than failing the fetch on
		// a charset quirk.
	}
	slog.Debug("fetch", "method", req.Method, "url", redactURL(req.URL), "status", resp.StatusCode, "bytes", len(body), "dur", time.Since(start))
	return resp.StatusCode, resp.Header, body, resp.Request.URL, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.userAgent)
	if c.tlsMimic {
		c.setChromeMimicHeaders(req)
	} else if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// We request br/gzip and own the decode (decodeBody) — this also disables
	// net/http's transparent gzip handling. Left as-is even under --tls-mimic:
	// fetch cannot decode zstd, so matching Chrome's "gzip, deflate, br, zstd"
	// would break decoding — correctness beats fingerprint fidelity here.
	req.Header.Set("Accept-Encoding", "gzip, br")
	c.injectCredentials(req)
}

// setChromeMimicHeaders adds the modern-Chrome request persona — a current Accept,
// the sec-ch-ua client hints, and Sec-Fetch metadata — so an origin that
// fingerprints request headers sees a coherent Chrome that matches the utls
// ClientHello and the tuned h2 SETTINGS. Only used on the --tls-mimic path; the
// plain path keeps its minimal header set. Note: over HTTP/2 Go controls header
// ordering, so only header presence/values align with Chrome, not their order.
func (c *Client) setChromeMimicHeaders(req *http.Request) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,"+
			"image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	}
	req.Header.Set("sec-ch-ua", chromeSecCHUA)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Linux"`)
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
}

// injectCredentials adds configured auth/custom headers, but only to requests to
// the credential origin, and never overriding Accept-Encoding (fetch owns decode).
func (c *Client) injectCredentials(req *http.Request) {
	if c.authHeader == "" && len(c.extraHeaders) == 0 {
		return
	}
	if !c.originMatches(req.URL) {
		return
	}
	for k, v := range c.extraHeaders {
		if strings.EqualFold(k, "Accept-Encoding") {
			continue
		}
		req.Header.Set(k, v)
	}
	if c.authHeader != "" {
		req.Header.Set("Authorization", c.authHeader)
	}
}

// originMatches reports whether u's origin equals the configured credential scope.
// A client with no scope matches nothing (fail-closed).
func (c *Client) originMatches(u *url.URL) bool {
	return c.credOrigin != "" && u != nil && u.Scheme+"://"+u.Host == c.credOrigin
}

// CredentialOptions returns options that replicate this client's origin-scoped
// credentials on a derived client (e.g. the guarded transport for in-page JS
// requests), so same-origin subrequests carry the session's auth. Returns nil when
// no credentials are configured.
func (c *Client) CredentialOptions() []Option {
	if c.authHeader == "" && len(c.extraHeaders) == 0 {
		return nil
	}
	auth, origin := c.authHeader, c.credOrigin
	hdrs := make(map[string]string, len(c.extraHeaders))
	for k, v := range c.extraHeaders {
		hdrs[k] = v
	}
	return []Option{func(nc *Client) {
		nc.authHeader = auth
		nc.credOrigin = origin
		if len(hdrs) > 0 {
			nc.extraHeaders = hdrs
		}
	}}
}

// normalizeOrigin reduces a URL or origin string to "scheme://host" (host includes
// any port). It returns the input unchanged if it cannot be parsed to a host.
func normalizeOrigin(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return origin
	}
	return u.Scheme + "://" + u.Host
}

// redactURL renders u for logging with known secret query parameters masked. It
// never mutates u.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.RawQuery == "" {
		return u.String()
	}
	q := u.Query()
	redacted := false
	for k := range q {
		if sensitiveParam(k) {
			q.Set(k, "REDACTED")
			redacted = true
		}
	}
	if !redacted {
		return u.String()
	}
	clone := *u
	clone.RawQuery = q.Encode()
	return clone.String()
}

func sensitiveParam(k string) bool {
	switch strings.ToLower(k) {
	case "token", "access_token", "api_key", "apikey", "key", "signature", "sig", "password", "secret":
		return true
	}
	return false
}

// shouldRetry reports whether to retry, bounded by maxRetries. Only transient HTTP
// statuses (429/5xx) are retried; network errors (DNS/refused/blocked/timeout) are
// treated as terminal — retrying them rarely helps and would, e.g., slow down an
// SSRF-blocked dial.
func (c *Client) shouldRetry(attempt int, resp *http.Response, err error) bool {
	if err != nil || attempt >= c.maxRetries {
		return false
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// maxRetryAfter caps a server-supplied Retry-After. The retry sleep happens outside
// http.Client.Do, so the request timeout does not bound it; without a cap a hostile
// or misconfigured server could return 429 with Retry-After: 999999 and hang the
// client (only caller-context cancellation would break it).
const maxRetryAfter = 30 * time.Second

// backoffDelay honors Retry-After when present (clamped to maxRetryAfter), else
// exponential backoff with jitter, capped at 5s.
func backoffDelay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
				return min(time.Duration(secs)*time.Second, maxRetryAfter)
			}
			if t, err := http.ParseTime(ra); err == nil {
				if d := time.Until(t); d > 0 {
					return min(d, maxRetryAfter)
				}
			}
		}
	}
	d := 300 * time.Millisecond * time.Duration(1<<attempt)
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d + rand.N(d/5) // up to +20% jitter
}

// isTextualResponse reports whether a response body should be charset-decoded to
// UTF-8. It gates on the Content-Type essence but overrides to binary when the
// leading bytes are a known binary signature (defending against servers that
// mislabel a PDF/image as text/*). This is the full extent of fetch's
// content-type awareness — it never routes or classifies beyond this.
func isTextualResponse(contentType string, body []byte) bool {
	if looksBinary(body) {
		return false
	}
	essence := contentType
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		essence = mt
	} else {
		// Fall back to the substring before any ";" for unparseable headers.
		if i := strings.IndexByte(essence, ';'); i >= 0 {
			essence = essence[:i]
		}
		essence = strings.ToLower(strings.TrimSpace(essence))
	}
	if essence == "" || essence == "application/octet-stream" {
		// Unknown or generic binary type: sniff. Treat as textual only when the
		// content sniffer sees text.
		return strings.HasPrefix(http.DetectContentType(body), "text/")
	}
	if strings.HasPrefix(essence, "text/") {
		return true
	}
	if strings.HasSuffix(essence, "+json") || strings.HasSuffix(essence, "+xml") {
		return true
	}
	switch essence {
	case "application/json",
		"application/ld+json",
		"application/xml",
		"application/xhtml+xml",
		"application/javascript",
		"application/ecmascript",
		"image/svg+xml":
		return true
	}
	return false
}

// looksBinary reports whether body starts with a well-known binary file
// signature (magic bytes), used to override a mislabeled textual Content-Type.
func looksBinary(body []byte) bool {
	switch {
	case bytes.HasPrefix(body, []byte("%PDF-")): // PDF
		return true
	case bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")): // PNG
		return true
	case bytes.HasPrefix(body, []byte("\xFF\xD8\xFF")): // JPEG
		return true
	case bytes.HasPrefix(body, []byte("GIF87a")), bytes.HasPrefix(body, []byte("GIF89a")): // GIF
		return true
	case len(body) >= 12 && bytes.HasPrefix(body, []byte("RIFF")) && bytes.Equal(body[8:12], []byte("WEBP")): // WebP
		return true
	case bytes.HasPrefix(body, []byte("\x1f\x8b")): // gzip
		return true
	case bytes.HasPrefix(body, []byte("PK\x03\x04")): // zip / office / jar
		return true
	}
	return false
}

// decodeBody wraps the response body with the right decompressor per
// Content-Encoding. (We set Accept-Encoding ourselves, so net/http does not.)
func decodeBody(resp *http.Response) (io.Reader, error) {
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		return resp.Body, nil
	}
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		return gzip.NewReader(resp.Body)
	case "br":
		return brotli.NewReader(resp.Body), nil
	case "deflate":
		return flate.NewReader(resp.Body), nil
	default:
		return resp.Body, nil
	}
}
