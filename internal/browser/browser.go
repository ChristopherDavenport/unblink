// Package browser is unblink's orchestrator. It wires the pipeline stages
// (fetch -> parse -> extract -> reduce -> emit) together behind a small Go API
// and is the only package the MCP layer talks to. No MCP types appear below this
// package, keeping the engine transport-agnostic and unit-testable on its own.
package browser

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/content"
	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/emit"
	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/ratelimit"
	"github.com/christopherdavenport/unblink/internal/reduce"
	"github.com/christopherdavenport/unblink/internal/search"
	"github.com/christopherdavenport/unblink/internal/session"
	"github.com/christopherdavenport/unblink/internal/tokens"
)

// Defaults for the orchestrator.
const (
	DefaultCacheTTL      = 60 * time.Second
	DefaultCacheCap      = 64
	DefaultSiteCacheTTL  = 10 * time.Minute // robots.txt/llms.txt change rarely
	DefaultSiteCacheCap  = 128
	DefaultLLMsMaxTokens = 4000 // cap on inline llms.txt content
	DefaultMaxTokens     = 6000
	MaxReadTokens        = 24000 // ceiling on a read chunk: max_tokens cannot defeat pagination

	maxJSErrors          = 5   // uncaught JS errors surfaced per result
	maxJSErrorLen        = 300 // runes per surfaced JS error
	DefaultJSTimeout     = 5 * time.Second
	DefaultJSMaxRequests = 50 // generous enough for an ES-module graph; still bounded
	// DefaultJSMaxLive caps concurrent live (persistent) JS runtimes. Each is a full
	// goja heap plus an event-loop goroutine; without this cap the only bound was the
	// 256-session cap — far too much memory. LRU runtimes are torn down (the session
	// keeps its page; a later interact reopens).
	DefaultJSMaxLive = 16
	DefaultJSPrewarm = js.DefaultMaxConcurrent
	DefaultRateRPS   = 5.0 // per-host requests/sec (0 disables)
	DefaultRateBurst = 10
	DefaultRetries   = fetch.DefaultRetries
)

// Renderer executes a page's scripts against its DOM, mutating doc in place. env
// supplies the page's JS network transport and cookie jar (both may be nil).
// internal/js provides the default implementation; the interface keeps the JS
// engine optional and swappable.
type Renderer interface {
	Render(ctx context.Context, doc *html.Node, base *url.URL, env js.Env) error
}

// liveRenderer is an optional Renderer capability: opening a persistent per-session
// JavaScript runtime (a "true session") instead of a one-shot render. The built-in
// js.Engine implements it; when absent, sessions degrade to stateless renders.
type liveRenderer interface {
	Open(ctx context.Context, doc *html.Node, base *url.URL, env js.Env) (js.LiveContext, error)
}

// Browser runs the read/navigation pipeline. It is safe for concurrent use.
type Browser struct {
	client        *fetch.Client                               // default client for stateless calls
	newPageClient func(session.Config) (*fetch.Client, error) // builds a credentialed page client (session + one-shot)
	cache         *cache                                      // stateless URL cache
	siteCache     *siteCache                                  // per-origin robots.txt/llms.txt cache
	sessions      *session.Manager

	siteHints  bool // fold robots.txt/llms.txt presence hints into Browse output
	safeOutput bool // strip hidden content + defang image beacons in emitted content

	renderer       Renderer     // nil when JavaScript rendering is disabled
	liveEngine     liveRenderer // non-nil when the renderer supports persistent per-session runtimes
	jsNetwork      bool         // allow page JS to make network requests
	jsMaxRequests  int          // per-render JS request budget
	jsAllowPrivate bool         // permit JS requests to private/loopback IPs
	jsReqTimeout   time.Duration
	jsMaxLive      int // cap on concurrent live JS runtimes (LRU torn down)

	limiter *ratelimit.Limiter // shared per-host rate limiter (nil = disabled)
	retries int                // fetch retries, threaded into JS subrequest clients

	search search.Provider // optional web-search backend (nil = search tool disabled)
}

// Option configures a Browser.
type Option func(*options)

type options struct {
	fetchOpts      []fetch.Option
	renderer       Renderer
	js             bool
	jsNetwork      bool
	jsMaxRequests  int
	jsAllowPrivate bool
	jsTimeout      time.Duration
	jsPrewarm      int
	jsMaxLive      int
	rateRPS        float64
	rateBurst      int
	retries        int
	tlsMimic       bool
	siteHints      bool
	safeOutput     bool
	allowPrivate   bool
	search         search.Provider
	sessionTTL     time.Duration
	sessionCap     int
}

// WithFetchOptions forwards options to the underlying fetch clients (default and
// per-session).
func WithFetchOptions(fo ...fetch.Option) Option {
	return func(o *options) { o.fetchOpts = append(o.fetchOpts, fo...) }
}

// WithRenderer sets a custom JavaScript renderer.
func WithRenderer(r Renderer) Option { return func(o *options) { o.renderer = r } }

// WithSearchProvider enables the search tool with the given provider. Off by
// default (nil provider): the search tool then errors cleanly until configured.
func WithSearchProvider(p search.Provider) Option { return func(o *options) { o.search = p } }

// WithJS enables JavaScript rendering using the built-in engine with the given
// per-render wall-clock timeout (0 = default). The engine is constructed in New
// so it composes with WithJSPrewarm regardless of option order.
func WithJS(timeout time.Duration) Option {
	if timeout <= 0 {
		timeout = DefaultJSTimeout
	}
	return func(o *options) {
		o.js = true
		o.jsTimeout = timeout
	}
}

// WithJSPrewarm sets how many fresh JS runtimes are kept ready in the background
// (0 disables the pool). Defaults to the render concurrency cap.
func WithJSPrewarm(n int) Option { return func(o *options) { o.jsPrewarm = n } }

// WithJSNetwork enables/disables page-JS network requests (default enabled).
func WithJSNetwork(enabled bool) Option { return func(o *options) { o.jsNetwork = enabled } }

// WithJSMaxRequests caps the number of JS-initiated requests per render.
func WithJSMaxRequests(n int) Option { return func(o *options) { o.jsMaxRequests = n } }

// WithJSAllowPrivate permits JS requests to private/loopback addresses (off by
// default; for internal/dev targets and testing).
func WithJSAllowPrivate(allow bool) Option { return func(o *options) { o.jsAllowPrivate = allow } }

// WithJSMaxLive caps how many live (persistent, per-session) JS runtimes may
// exist at once; the least-recently-used runtime is torn down to make room (its
// session and page survive — the next interact reopens it). 0 keeps the default.
func WithJSMaxLive(n int) Option { return func(o *options) { o.jsMaxLive = n } }

// WithAllowPrivate permits page fetches (the default + per-session + one-shot
// clients) to reach private/loopback/metadata addresses. Off by default: direct
// fetches to localhost/private IPs are blocked by the SSRF dial guard. Enable for
// internal/dev targets. Independent of WithJSAllowPrivate (in-page JS subrequests).
func WithAllowPrivate(allow bool) Option { return func(o *options) { o.allowPrivate = allow } }

// WithRateLimit sets the per-host request rate (requests/sec) and burst (rps 0
// disables rate limiting).
func WithRateLimit(rps float64, burst int) Option {
	return func(o *options) { o.rateRPS, o.rateBurst = rps, burst }
}

// WithRetries sets the number of retries for transient fetch failures.
func WithRetries(n int) Option { return func(o *options) { o.retries = n } }

// WithTLSMimic enables browser-like TLS fingerprinting (utls) on page fetches.
func WithTLSMimic(enabled bool) Option { return func(o *options) { o.tlsMimic = enabled } }

// WithSessionLimits tunes the session manager: how long an idle session lives
// before eviction, and the maximum number of concurrent sessions (oldest evicted
// on overflow). Zero values keep the defaults (30m / 256).
func WithSessionLimits(ttl time.Duration, capacity int) Option {
	return func(o *options) { o.sessionTTL, o.sessionCap = ttl, capacity }
}

// WithSiteHints controls whether Browse folds robots.txt/llms.txt presence hints
// into its output (default enabled). Disabling it avoids the per-host origin-root
// probe on the first browse of each host. The site tool is unaffected.
func WithSiteHints(enabled bool) Option { return func(o *options) { o.siteHints = enabled } }

// WithSafeOutput toggles the untrusted-content safety pass on emitted content:
// stripping human-hidden text and defanging Markdown image beacons. On by default;
// disable to get the raw, unmodified reduction.
func WithSafeOutput(enabled bool) Option { return func(o *options) { o.safeOutput = enabled } }

// New returns a Browser with a default fetch client, a short-TTL page cache, and
// a session manager. Session clients are built with the same fetch options.
func New(opts ...Option) (*Browser, error) {
	o := options{
		jsNetwork: true, jsMaxRequests: DefaultJSMaxRequests, jsTimeout: DefaultJSTimeout,
		jsPrewarm: js.DefaultMaxConcurrent, jsMaxLive: DefaultJSMaxLive,
		rateRPS: DefaultRateRPS, rateBurst: DefaultRateBurst, retries: DefaultRetries,
		siteHints: true, safeOutput: true,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.renderer == nil && o.js {
		o.renderer = js.New(js.WithTimeout(o.jsTimeout), js.WithPrewarm(o.jsPrewarm))
	}

	var limiter *ratelimit.Limiter
	if o.rateRPS > 0 {
		limiter = ratelimit.New(o.rateRPS, o.rateBurst)
	}
	// Robustness options shared by the default + per-session page clients (the
	// limiter pointer is shared so throttling is per-host across all of them). The
	// SSRF dial guard covers every page-fetch client (default, per-session,
	// one-shot); page JS subrequests are guarded separately (see transport.go).
	base := append([]fetch.Option{
		fetch.WithRetries(o.retries),
		fetch.WithTLSMimic(o.tlsMimic),
		fetch.WithRateLimiter(limiter),
	}, o.fetchOpts...)
	if ctrl := ssrfControl(o.allowPrivate); ctrl != nil {
		base = append(base, fetch.WithDialControl(ctrl))
	}

	// newPageClient builds a page-fetch client carrying cfg's origin-scoped
	// credentials (empty cfg → an anonymous client). Cookies are seeded into the
	// jar for the credential origin. Used for the default client, every session,
	// and one-shot authed reads — so all share the same robustness + SSRF guard.
	newPageClient := func(cfg session.Config) (*fetch.Client, error) {
		opts := append(append([]fetch.Option{}, base...), authOptions(cfg)...)
		c, err := fetch.New(opts...)
		if err != nil {
			return nil, err
		}
		seedCookies(c, cfg)
		return c, nil
	}

	client, err := newPageClient(session.Config{})
	if err != nil {
		return nil, err
	}
	// Tear down a session's live JS runtime when the manager evicts/closes it.
	mgr := session.NewManager(o.sessionTTL, o.sessionCap, newPageClient,
		func(s *session.Session) { s.Close() })

	b := &Browser{
		client:         client,
		newPageClient:  newPageClient,
		cache:          newCache(DefaultCacheTTL, DefaultCacheCap),
		siteCache:      newSiteCache(DefaultSiteCacheTTL, DefaultSiteCacheCap),
		sessions:       mgr,
		siteHints:      o.siteHints,
		safeOutput:     o.safeOutput,
		renderer:       o.renderer,
		jsNetwork:      o.jsNetwork,
		jsMaxRequests:  o.jsMaxRequests,
		jsAllowPrivate: o.jsAllowPrivate,
		jsReqTimeout:   o.jsTimeout,
		jsMaxLive:      o.jsMaxLive,
		limiter:        limiter,
		retries:        o.retries,
		search:         o.search,
	}
	if lr, ok := o.renderer.(liveRenderer); ok {
		b.liveEngine = lr
	}
	return b, nil
}

// Close releases background resources, notably the JS engine's pre-warm pool. It
// is safe to call once at shutdown and a no-op when JS rendering is disabled.
func (b *Browser) Close() {
	b.sessions.CloseAll() // terminate every session's live JS runtime
	if c, ok := b.renderer.(interface{ Close() }); ok {
		c.Close()
	}
}

// Request describes which page a tool should act on: a stateless URL, or a
// session (optionally reusing its current page instead of navigating).
type Request struct {
	SessionID  string
	URL        string
	UseCurrent bool
	Render     bool // run the page's JavaScript before extracting (requires WithJS)
	// IncludeBytes is read-only by Read: for an image page it also surfaces the raw
	// image bytes (the MCP layer turns them into base64 ImageContent). Ignored by
	// every other tool and for non-image content.
	IncludeBytes bool

	// Format selects Read's output: "" / "markdown" (reduced, the default),
	// "raw_html" (unreduced page source), or "text" (visible plain text).
	Format string
	// Selector is an optional CSS selector applied when Format == "raw_html":
	// only the matching elements are serialized. Ignored by other formats.
	Selector string

	// WaitFor/WaitText/WaitTimeout gate a JavaScript render: keep it alive until the
	// selector resolves and/or the text appears (or WaitTimeout elapses). Setting
	// either WaitFor or WaitText implies Render and requires the server's --js engine.
	WaitFor     string
	WaitText    string
	WaitTimeout time.Duration

	// Headers and Auth are one-shot credentials for a stateless fetch (no
	// SessionID). They are scoped to the request URL's origin and ignored when a
	// SessionID is given (the session's own config governs). Auth is already
	// env-resolved by the MCP layer.
	Headers map[string]string
	Auth    *RequestAuth
}

// RequestAuth is env-resolved one-shot credentials for a stateless fetch.
type RequestAuth struct {
	Bearer    string
	BasicUser string
	BasicPass string
}

// authOptions turns a session credential config into fetch options.
func authOptions(cfg session.Config) []fetch.Option {
	var opts []fetch.Option
	if cfg.Origin != "" {
		opts = append(opts, fetch.WithCredentialScope(cfg.Origin))
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, fetch.WithHeaders(cfg.Headers))
	}
	switch {
	case cfg.Bearer != "":
		opts = append(opts, fetch.WithBearer(cfg.Bearer))
	case cfg.BasicUser != "" || cfg.BasicPass != "":
		opts = append(opts, fetch.WithBasicAuth(cfg.BasicUser, cfg.BasicPass))
	}
	return opts
}

// seedCookies pre-populates a client's jar with cfg's cookies, scoped to the
// credential origin. A no-op without both cookies and an origin.
func seedCookies(c *fetch.Client, cfg session.Config) {
	if len(cfg.Cookies) == 0 || cfg.Origin == "" {
		return
	}
	if u, err := url.Parse(cfg.Origin); err == nil {
		c.Jar().SetCookies(u, cfg.Cookies)
	}
}

// originOfURL reduces rawURL to "scheme://host", or "" if it has no host.
func originOfURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// renderOpts carries a request's JavaScript-render intent down to fetchPage: whether
// to render at all, an optional wait condition, and a per-render budget override.
type renderOpts struct {
	render      bool
	wait        *js.WaitCondition
	timeout     time.Duration
	storage     js.Storage // session-scoped localStorage backing; nil for stateless fetches
	sessStorage js.Storage // session-scoped sessionStorage backing; nil for stateless fetches
}

// renderOpts derives the render intent from a Request. A wait condition implies a
// render (there is nothing to wait for without running the page's JS).
func (r Request) renderOpts() renderOpts {
	ro := renderOpts{render: r.Render, timeout: r.WaitTimeout}
	if r.WaitFor != "" || r.WaitText != "" {
		ro.wait = &js.WaitCondition{Selector: r.WaitFor, Text: r.WaitText}
		ro.render = true
	}
	return ro
}

// resolve produces the page a request targets, navigating and recording history
// when a session and URL are given.
func (b *Browser) resolve(ctx context.Context, req Request) (*page.Page, *session.Session, error) {
	if req.SessionID != "" {
		sess, err := b.sessions.GetOrCreate(req.SessionID)
		if err != nil {
			return nil, nil, err
		}
		if req.UseCurrent || req.URL == "" {
			cur := sess.Current()
			if cur == nil {
				return nil, sess, errNoCurrentPage(req.SessionID)
			}
			// If a live runtime is driving this page, refresh from a fresh snapshot so
			// reads reflect interactions and background timer/fetch activity.
			if lc := sess.Live(); lc != nil {
				refreshed, err := b.refreshFromLive(ctx, sess, lc)
				if err != nil {
					return nil, sess, err
				}
				return refreshed, sess, nil
			}
			return cur, sess, nil
		}
		ro := req.renderOpts()
		ro.storage, ro.sessStorage = sess.Storage(), sess.SessionStorage()
		p, err := b.fetchPage(ctx, sess.Client(), req.URL, ro)
		if err != nil {
			return nil, sess, err
		}
		sess.Visit(p)
		return p, sess, nil
	}

	if req.URL == "" {
		return nil, nil, errf(ErrBadInput, "a url is required when no session is given")
	}
	if req.Auth != nil || len(req.Headers) > 0 {
		// One-shot credentials: fetch through a throwaway client scoped to the URL's
		// origin and skip the shared URL cache (never cache a credentialed response).
		p, err := b.fetchStatelessAuthed(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		return p, nil, nil
	}
	p, err := b.fetchStateless(ctx, req.URL, req.renderOpts())
	if err != nil {
		return nil, nil, err
	}
	return p, nil, nil
}

// fetchStatelessAuthed fetches req.URL through an ephemeral client carrying the
// request's one-shot credentials, pinned to the URL's origin. It bypasses the URL
// cache so credentialed responses are never shared.
func (b *Browser) fetchStatelessAuthed(ctx context.Context, req Request) (*page.Page, error) {
	cfg := session.Config{Origin: originOfURL(req.URL), Headers: req.Headers}
	if a := req.Auth; a != nil {
		cfg.Bearer, cfg.BasicUser, cfg.BasicPass = a.Bearer, a.BasicUser, a.BasicPass
	}
	ec, err := b.newPageClient(cfg)
	if err != nil {
		return nil, err
	}
	return b.fetchPage(ctx, ec, req.URL, req.renderOpts())
}

// fetchStateless fetches via the default client, using the short-TTL URL cache.
// Rendered and non-rendered results are cached under distinct keys. A waited render
// is time-sensitive, so it bypasses the cache entirely (never served or stored).
func (b *Browser) fetchStateless(ctx context.Context, url string, ro renderOpts) (*page.Page, error) {
	if ro.wait != nil {
		return b.fetchPage(ctx, b.client, url, ro)
	}
	key := cacheKey(url, ro.render)
	if p, ok := b.cache.get(key); ok {
		return p, nil
	}
	p, err := b.fetchPage(ctx, b.client, url, ro)
	if err != nil {
		return nil, err
	}
	b.cache.put(key, p)
	return p, nil
}

func cacheKey(url string, render bool) string {
	if render {
		return "render\x00" + url
	}
	return url
}

// fetchPage runs fetch -> parse -> [render] -> extract. When render is requested
// and a renderer is configured, the page's scripts mutate the canonical tree in
// place before extraction, so Meta/links/etc. reflect the post-script DOM.
func (b *Browser) fetchPage(ctx context.Context, client *fetch.Client, url string, ro renderOpts) (*page.Page, error) {
	p, err := client.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	return b.processFetched(ctx, client, p, ro)
}

// processFetched runs the post-fetch stages over an already-fetched page:
// classify -> parse -> [render] -> extract. Shared by fetchPage and Submit (a
// form response is a fetched page too).
func (b *Browser) processFetched(ctx context.Context, client *fetch.Client, p *page.Page, ro renderOpts) (*page.Page, error) {
	p.Kind = classify(p.ContentType, p.Raw)
	if p.Kind != page.KindHTML {
		// Non-HTML: skip HTML parse/extract (Doc stays nil). Conversion to Markdown
		// happens lazily in Read, mirroring reduce/emit.
		deriveNonHTMLMeta(p)
		return p, nil
	}
	if err := dom.Parse(p); err != nil {
		return nil, err
	}
	if ro.render && b.renderer != nil {
		var diag js.RenderResult
		env := js.Env{Cookies: cookieAdapter{jar: client.Jar()}, Storage: ro.storage, SessionStorage: ro.sessStorage, Diag: &diag, Wait: ro.wait, Timeout: ro.timeout}
		if b.jsNetwork {
			env.Transport = b.newRenderTransport(client, ro.timeout)
		}
		// Best-effort: a render failure other than cancellation keeps whatever the
		// scripts produced rather than failing the whole fetch.
		if err := b.renderer.Render(ctx, p.Doc, dom.BaseURL(p), env); err != nil && ctx.Err() != nil {
			return nil, err
		}
		p.Rendered = true
		applyRenderDiag(p, diag, pageURL(p))
	}
	if err := dom.Extract(p); err != nil {
		return nil, err
	}
	return p, nil
}

// pageURL is the page's best-known URL for logging.
func pageURL(p *page.Page) string {
	if p.FinalURL != nil {
		return p.FinalURL.String()
	}
	if p.RequestURL != nil {
		return p.RequestURL.String()
	}
	return ""
}

// applyRenderDiag records JS render diagnostics on the page and logs them at debug,
// so a framework that failed to render (uncaught error, unknown framework) is
// observable without changing the read output.
func applyRenderDiag(p *page.Page, diag js.RenderResult, url string) {
	p.RenderDiag = &page.RenderDiag{
		Framework:         diag.Framework,
		Errors:            diag.Errors,
		Upgrades:          diag.Upgrades,
		WaitRequested:     diag.WaitRequested,
		WaitMet:           diag.WaitMet,
		PendingNavigation: diag.PendingNavigation,
	}
	if diag.Framework != "" || len(diag.Errors) > 0 || diag.Upgrades > 0 {
		slog.Debug("js: render diagnostics",
			"url", url, "framework", diag.Framework,
			"upgrades", diag.Upgrades, "errors", len(diag.Errors))
	}
	for _, e := range diag.Errors {
		slog.Debug("js: uncaught script error", "url", url, "err", e)
	}
}

// deriveNonHTMLMeta sets a minimal Meta.Title for a non-HTML page — the URL's last
// path segment — so browse/read show a sensible name without parsing HTML.
func deriveNonHTMLMeta(p *page.Page) {
	if p.Meta.Title != "" {
		return
	}
	u := p.FinalURL
	if u == nil {
		u = p.RequestURL
	}
	if u == nil {
		return
	}
	name := path.Base(u.Path)
	if name == "" || name == "/" || name == "." {
		name = u.Host
	}
	p.Meta.Title = name
}

// ReadResult is the paginated Markdown rendering of a page.
type ReadResult struct {
	Markdown    string `json:"-"`
	Mode        string `json:"mode"`
	Page        int    `json:"page"`
	TotalPages  int    `json:"total_pages"`
	NextCursor  string `json:"next_cursor,omitempty"`
	Truncated   bool   `json:"truncated"`
	ContentType string `json:"content_type,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Bytes       int    `json:"bytes,omitempty"`

	// ArticleFallback is true when mode=article was requested but readability found
	// no distinct article body, so the whole reduced page was returned instead
	// (Mode then reports "full"). The agent asked for an article and didn't get one.
	ArticleFallback bool `json:"article_fallback,omitempty"`

	// WaitMet is set only when the request carried a wait_for/wait_text gate: true if
	// the condition appeared before the render returned, false if it never did (the
	// content the agent asked for is likely missing). PendingNavigation is a URL the
	// page's JS asked to navigate to but that the render did not follow.
	WaitMet           *bool  `json:"wait_met,omitempty"`
	PendingNavigation string `json:"pending_navigation,omitempty"`

	// Framework/JSErrors surface JavaScript render diagnostics: the SPA framework
	// detected (if any) and up to maxJSErrors uncaught script errors. A page that
	// failed to hydrate is visible here rather than silently thin.
	Framework string   `json:"framework,omitempty"`
	JSErrors  []string `json:"js_errors,omitempty"`

	// ImageBytes/ImageMIME carry the raw image for the MCP layer to base64-encode,
	// set only when the page is an image and the request asked for include_bytes.
	// Kept out of the JSON structured output (the bytes go over MCP as ImageContent).
	ImageBytes []byte `json:"-"`
	ImageMIME  string `json:"-"`
}

// Read resolves req's page, reduces it (mode "article" — main content, the
// default — or "full" — the whole page), renders Markdown, and returns the chunk
// addressed by cursor within maxTokens. reduce/emit run on a copy so shared
// (cached or session) pages are never mutated.
func (b *Browser) Read(ctx context.Context, req Request, mode string, maxTokens int, cursor string) (*ReadResult, error) {
	if (req.WaitFor != "" || req.WaitText != "") && b.renderer == nil {
		return nil, errf(ErrJSRequired, "wait_for/wait_text require JavaScript; start the server with --js")
	}
	p, _, err := b.resolve(ctx, req)
	if err != nil {
		return nil, err
	}

	articleFallback := false
	pc := *p
	switch format := strings.ToLower(strings.TrimSpace(req.Format)); format {
	case "raw_html", "text":
		// Escape hatch past semantic reduction. Binary bodies have no text/DOM to
		// surface, so refuse them (the caller can use include_bytes instead).
		switch pc.Kind {
		case page.KindImage, page.KindPDF, page.KindBinary:
			return nil, errf(ErrBadInput, "format %q unavailable for %s content", format, kindString(pc.Kind))
		}
		var out string
		if format == "raw_html" {
			out, err = b.rawHTML(&pc, req.Selector)
		} else {
			out = b.rawText(&pc)
		}
		if err != nil {
			return nil, err
		}
		pc.Markdown, mode = out, format
	default:
		switch pc.Kind {
		case "", page.KindHTML:
			switch strings.ToLower(strings.TrimSpace(mode)) {
			case "full":
				mode, err = "full", reduce.Full(&pc, b.safeOutput)
			default:
				mode, err = "article", reduce.Article(&pc, b.safeOutput)
			}
			if err != nil {
				return nil, fmt.Errorf("reduce: %w", err)
			}
			// Report what actually ran: readability finding no article falls back to
			// the full reduction, and pretending otherwise misleads the agent.
			if mode == "article" && pc.Article != nil && pc.Article.Source == "full" {
				mode, articleFallback = "full", true
			}
			if err := emit.Markdown(&pc); err != nil {
				return nil, fmt.Errorf("emit: %w", err)
			}
		default:
			// Non-HTML: convert to Markdown in place (lazily, on the copy) and report
			// the kind as the mode so the caller sees what happened.
			if err := content.Render(ctx, &pc); err != nil {
				return nil, fmt.Errorf("convert: %w", err)
			}
			mode = string(pc.Kind)
		}
		// Neutralize auto-rendering image-beacon exfiltration in emitted Markdown.
		if b.safeOutput {
			pc.Markdown = emit.DefangImages(pc.Markdown)
		}
	}

	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	if maxTokens > MaxReadTokens {
		maxTokens = MaxReadTokens
	}
	chunks := tokens.Paginate(pc.Markdown, maxTokens)
	fp := tokens.Fingerprint(pc.Markdown)
	idx, err := tokens.DecodeCursor(cursor, fp)
	if err != nil {
		if errors.Is(err, tokens.ErrStaleCursor) {
			return nil, err // Classify maps it to cursor_expired with guidance
		}
		return nil, errf(ErrBadInput, "%v", err)
	}
	if idx >= len(chunks) {
		idx = len(chunks) - 1
	}

	res := &ReadResult{
		Markdown:        chunks[idx],
		Mode:            mode,
		ArticleFallback: articleFallback,
		Page:            idx + 1,
		TotalPages:      len(chunks),
		ContentType:     pc.ContentType,
		Kind:            kindString(pc.Kind),
		Bytes:           len(pc.Raw),
	}
	if idx+1 < len(chunks) {
		res.NextCursor = tokens.EncodeCursor(idx+1, fp)
		res.Truncated = true
	}
	if d := pc.RenderDiag; d != nil {
		if d.WaitRequested {
			wm := d.WaitMet
			res.WaitMet = &wm
		}
		res.PendingNavigation = d.PendingNavigation
		res.Framework = d.Framework
		res.JSErrors = capErrors(d.Errors)
	}
	if req.IncludeBytes && pc.Kind == page.KindImage {
		res.ImageBytes = pc.Raw
		res.ImageMIME = pc.ContentType
	}
	return res, nil
}

// capErrors bounds a JS-error list for tool output: at most maxJSErrors entries,
// each truncated to maxJSErrorLen runes.
func capErrors(errs []string) []string {
	if len(errs) == 0 {
		return nil
	}
	out := make([]string, 0, maxJSErrors)
	for _, e := range errs {
		if len(out) == maxJSErrors {
			out = append(out, fmt.Sprintf("… (%d more)", len(errs)-maxJSErrors))
			break
		}
		if r := []rune(e); len(r) > maxJSErrorLen {
			e = string(r[:maxJSErrorLen]) + "…"
		}
		out = append(out, e)
	}
	return out
}

// kindString reports a page's Kind for the wire, mapping the "" zero value (an
// internally-built or HTML page) to "html".
func kindString(k page.Kind) string {
	if k == "" {
		return string(page.KindHTML)
	}
	return string(k)
}

// rawHTML returns the page's HTML source for Read's raw_html format. With a
// selector, only the matching subtrees are serialized. Otherwise, when JavaScript
// ran (a --js render or a live session snapshot) the post-JS DOM is serialized;
// with no JS the exact fetched bytes are returned (best fidelity for SSR-embedded
// JSON). All paths are read-only and leave relative URLs untouched (raw source).
func (b *Browser) rawHTML(pc *page.Page, selector string) (string, error) {
	if s := strings.TrimSpace(selector); s != "" {
		if pc.Doc == nil {
			return "", errf(ErrBadInput, "selector requires an HTML page (got %s)", kindString(pc.Kind))
		}
		out, n, err := dom.RenderSelector(pc.Doc, s)
		if err != nil {
			return "", errf(ErrBadInput, "%v", err)
		}
		if n == 0 {
			return "", errf(ErrBadInput, "selector %q matched no elements", s)
		}
		return out, nil
	}
	if pc.Rendered && pc.Doc != nil {
		return dom.Render(pc.Doc)
	}
	return string(pc.Raw), nil
}

// rawText returns the page's visible plain text for Read's text format, or the
// raw body when there is no DOM (json/feed/text bodies are already plain UTF-8).
func (b *Browser) rawText(pc *page.Page) string {
	if pc.Doc != nil {
		return dom.Text(pc.Doc, b.safeOutput)
	}
	return string(pc.Raw)
}

// Counts summarizes the structural elements found on a page.
type Counts struct {
	Links    int `json:"links"`
	Forms    int `json:"forms"`
	Images   int `json:"images"`
	Headings int `json:"headings"`
	Controls int `json:"controls"`
}

// BrowseResult is a cheap orientation summary of a page.
type BrowseResult struct {
	FinalURL    string `json:"final_url"`
	Status      int    `json:"status"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Lang        string `json:"lang,omitempty"`
	Counts      Counts `json:"counts"`
	Excerpt     string `json:"excerpt,omitempty"`
	Outline     string `json:"outline,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Bytes       int    `json:"bytes,omitempty"`

	// Agent-facing site hints (populated by Browse when site hints are enabled).
	LLMsTxt bool        `json:"llms_txt,omitempty"`
	Robots  *RobotsHint `json:"robots,omitempty"`

	// Framework/JSErrors surface JavaScript render diagnostics when the page was
	// rendered (browse/click/submit with render=true) — same contract as read.
	Framework string   `json:"framework,omitempty"`
	JSErrors  []string `json:"js_errors,omitempty"`
}

// summarize builds a BrowseResult from a page.
func summarize(p *page.Page) *BrowseResult {
	excerpt := p.Meta.Description
	if excerpt == "" && p.Doc != nil {
		excerpt = dom.FirstParagraph(p.Doc)
	}
	final := ""
	if p.FinalURL != nil {
		final = p.FinalURL.String()
	}
	res := &BrowseResult{
		FinalURL:    final,
		Status:      p.StatusCode,
		Title:       p.Meta.Title,
		Description: p.Meta.Description,
		SiteName:    p.Meta.SiteName,
		Lang:        p.Meta.Lang,
		Counts: Counts{
			Links:    len(p.Meta.Links),
			Forms:    len(p.Meta.Forms),
			Images:   len(p.Meta.Images),
			Headings: len(p.Meta.Headings),
			Controls: len(p.Meta.Controls),
		},
		Excerpt:     excerpt,
		Outline:     truncateOutline(emit.Outline(p)),
		ContentType: p.ContentType,
		Kind:        kindString(p.Kind),
		Bytes:       len(p.Raw),
	}
	if d := p.RenderDiag; d != nil {
		res.Framework = d.Framework
		res.JSErrors = capErrors(d.Errors)
	}
	return res
}

// maxOutlineBytes bounds a browse outline — a pathological page with thousands
// of headings must not turn the "cheap orientation" tool into a token bomb.
const maxOutlineBytes = 8 << 10

func truncateOutline(outline string) string {
	if len(outline) <= maxOutlineBytes {
		return outline
	}
	cut := strings.LastIndexByte(outline[:maxOutlineBytes], '\n')
	if cut <= 0 {
		cut = maxOutlineBytes
	}
	return outline[:cut] + "\n… (outline truncated)"
}

// Browse returns a cheap orientation summary of req's page. When site hints are
// enabled, it also folds in llms.txt/robots.txt presence (host-cached, so only
// the first browse of a host pays the origin-root probe).
func (b *Browser) Browse(ctx context.Context, req Request) (*BrowseResult, error) {
	p, _, err := b.resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("browser: browse: %w", err)
	}
	res := summarize(p)
	if b.siteHints {
		res.LLMsTxt, res.Robots = b.siteHint(ctx, p.FinalURL)
	}
	return res, nil
}

// Links returns req's links, optionally filtered by a case-insensitive substring
// (matched against text or href) and/or restricted to internal links.
func (b *Browser) Links(ctx context.Context, req Request, filter string, internalOnly bool) ([]page.Link, error) {
	p, _, err := b.resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("browser: links: %w", err)
	}
	f := strings.ToLower(strings.TrimSpace(filter))
	out := make([]page.Link, 0, len(p.Meta.Links))
	for _, l := range p.Meta.Links {
		if internalOnly && !l.Internal {
			continue
		}
		if f != "" &&
			!strings.Contains(strings.ToLower(l.Text), f) &&
			!strings.Contains(strings.ToLower(l.Href), f) {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// Forms returns req's forms and their fields.
func (b *Browser) Forms(ctx context.Context, req Request) ([]page.Form, error) {
	p, _, err := b.resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("browser: forms: %w", err)
	}
	return p.Meta.Forms, nil
}

// Controls returns req's non-anchor interactive controls (buttons, role=button,
// onclick/tabindex elements, submit/reset inputs, tabs, summaries), each with a
// stable CSS selector the interact tool accepts.
func (b *Browser) Controls(ctx context.Context, req Request) ([]page.Control, error) {
	p, _, err := b.resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("browser: controls: %w", err)
	}
	return p.Meta.Controls, nil
}

// DataCounts reports how many items of each structured-data kind were found.
type DataCounts struct {
	JSONLD    int `json:"jsonld"`
	Tables    int `json:"tables"`
	Microdata int `json:"microdata"`
}

// DataResult is the structured data extracted from a page by the data tool.
type DataResult struct {
	Kind      string               `json:"kind"`
	JSONLD    []any                `json:"jsonld,omitempty"`
	Tables    []page.Table         `json:"tables,omitempty"`
	Microdata []page.MicrodataItem `json:"microdata,omitempty"`
	Counts    DataCounts           `json:"counts"`
}

// Data extracts machine-readable structured data from req's page: JSON-LD, HTML
// data tables, and/or microdata, selected by kind (jsonld|tables|microdata|all,
// default all). HTML pages only — the extraction is read-only over p.Doc.
func (b *Browser) Data(ctx context.Context, req Request, kind string) (*DataResult, error) {
	p, _, err := b.resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("browser: data: %w", err)
	}
	if p.Doc == nil {
		return nil, fmt.Errorf("browser: data: structured extraction requires an HTML page (got %s)", kindString(p.Kind))
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "all"
	}
	base := dom.BaseURL(p)
	res := &DataResult{Kind: kind}
	switch kind {
	case "jsonld":
		res.JSONLD = dom.JSONLD(p.Doc)
	case "tables":
		res.Tables = dom.Tables(p.Doc)
	case "microdata":
		res.Microdata = dom.Microdata(p.Doc, base)
	case "all":
		res.JSONLD = dom.JSONLD(p.Doc)
		res.Tables = dom.Tables(p.Doc)
		res.Microdata = dom.Microdata(p.Doc, base)
	default:
		return nil, fmt.Errorf("browser: data: unknown kind %q (want jsonld|tables|microdata|all)", kind)
	}
	res.Counts = DataCounts{JSONLD: len(res.JSONLD), Tables: len(res.Tables), Microdata: len(res.Microdata)}
	return res, nil
}

// Find searches req's page text for query and returns up to maxHits snippets.
func (b *Browser) Find(ctx context.Context, req Request, query string, maxHits int) ([]dom.Hit, error) {
	p, _, err := b.resolve(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("browser: find: %w", err)
	}
	if p.Doc == nil {
		// Non-HTML pages have no DOM to search.
		return nil, nil
	}
	return dom.Find(p.Doc, query, maxHits), nil
}

// Click follows a link from the session's current page — by match (case-
// insensitive substring of text or href) when given, otherwise by linkIndex —
// navigating the session to it.
func (b *Browser) Click(ctx context.Context, sessionID string, linkIndex int, match string, render bool) (*BrowseResult, error) {
	sess, err := b.sessions.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	cur := sess.Current()
	if cur == nil {
		return nil, errNoCurrentPage(sessionID)
	}
	links := cur.Meta.Links

	var href string
	if m := strings.ToLower(strings.TrimSpace(match)); m != "" {
		for _, l := range links {
			if strings.Contains(strings.ToLower(l.Text), m) || strings.Contains(strings.ToLower(l.Href), m) {
				href = l.Href
				break
			}
		}
		if href == "" {
			return nil, errf(ErrBadInput, "no link matching %q on the current page (%d links; list them with the links tool)", match, len(links))
		}
	} else {
		if linkIndex < 0 || linkIndex >= len(links) {
			return nil, errf(ErrBadInput, "link_index %d out of range (%d links)", linkIndex, len(links))
		}
		href = links[linkIndex].Href
	}

	p, err := b.fetchPage(ctx, sess.Client(), href, renderOpts{render: render, storage: sess.Storage(), sessStorage: sess.SessionStorage()})
	if err != nil {
		return nil, err
	}
	sess.Visit(p)
	return summarize(p), nil
}

// Submit submits a form from the session's current page. The form is chosen by
// formRef (id, name, or numeric index; optional when the page has one form).
// values overlay the form's default field values.
func (b *Browser) Submit(ctx context.Context, sessionID, formRef string, values map[string]string, render bool) (*BrowseResult, error) {
	sess, err := b.sessions.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	cur := sess.Current()
	if cur == nil {
		return nil, errNoCurrentPage(sessionID)
	}
	form, ok := selectForm(cur.Meta.Forms, formRef)
	if !ok {
		return nil, errf(ErrBadInput, "no form matching %q (%d forms on page; list them with the forms tool)", formRef, len(cur.Meta.Forms))
	}

	vals := url.Values{}
	for _, f := range form.Fields {
		if f.Name != "" && f.Value != "" {
			vals.Set(f.Name, f.Value)
		}
	}
	for k, v := range values {
		vals.Set(k, v)
	}

	p, err := sess.Client().Submit(ctx, form.Method, form.Action, vals)
	if err != nil {
		return nil, err
	}
	if _, err := b.processFetched(ctx, sess.Client(), p, renderOpts{render: render, storage: sess.Storage(), sessStorage: sess.SessionStorage()}); err != nil {
		return nil, err
	}
	sess.Visit(p)
	return summarize(p), nil
}

// InteractResult reports the outcome of an interact call.
type InteractResult struct {
	Summary  *BrowseResult  `json:"summary"`
	Selector string         `json:"selector"`
	Event    string         `json:"event"`
	Matched  bool           `json:"matched"` // the selector resolved to a node during dispatch
	Changed  bool           `json:"changed"` // the live DOM differs from before the action
	Controls []page.Control `json:"controls,omitempty"`
	// JSErrors are uncaught script errors recorded while this interaction ran
	// (capped) — a handler that threw is visible instead of a silent no-op.
	JSErrors []string `json:"js_errors,omitempty"`
	// PendingNavigation is a URL the handler asked to navigate to via
	// location.href/assign/replace (interact never navigates itself); follow it with
	// read/click. Empty when the interaction requested no cross-document navigation.
	PendingNavigation string `json:"pending_navigation,omitempty"`
}

// Interact dispatches a DOM event (event, default "click") at selector on the
// session's current page and runs its JavaScript so handlers fire and mutate the
// DOM, then snapshots the updated page. value, when given, is written to the target
// form control before the event fires.
//
// The session holds a persistent JS runtime (opened lazily on first interact), so
// state — listeners, timers, variables, fetched data — survives across calls; there
// is no replay. Requires WithJS; the flat-DOM model lets frameworks hydrate, so the
// handlers they attach fire on dispatch. Never navigates.
func (b *Browser) Interact(ctx context.Context, sessionID, selector, event, value string) (*InteractResult, error) {
	if b.liveEngine == nil {
		return nil, errf(ErrJSRequired, "interact requires JavaScript; start the server with --js")
	}
	if strings.TrimSpace(selector) == "" {
		return nil, errf(ErrBadInput, "selector is required (discover selectors with the controls tool)")
	}
	sess, err := b.sessions.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	cur := sess.Current()
	if cur == nil {
		return nil, errNoCurrentPage(sessionID)
	}

	event = strings.TrimSpace(event)
	if event == "" {
		event = "click"
	}

	before := docFingerprint(cur.Doc)
	lc, err := b.ensureLive(ctx, sess)
	if err != nil {
		return nil, err
	}
	res, err := lc.Dispatch(ctx, js.Action{Selector: selector, Type: event, Value: value})
	if err != nil {
		return nil, err
	}
	// A handler may have requested a cross-document navigation (location.href); read
	// and clear it so the caller can follow it. Best-effort — a snapshot failure below
	// still returns the nav signal.
	nav, _ := lc.PendingNavigation(ctx)
	np, err := b.refreshFromLive(ctx, sess, lc)
	if err != nil {
		return nil, err
	}

	return &InteractResult{
		Summary:           summarize(np),
		Selector:          selector,
		Event:             event,
		Matched:           res.Matched,
		Changed:           docFingerprint(np.Doc) != before,
		Controls:          np.Meta.Controls,
		JSErrors:          capErrors(res.Errors),
		PendingNavigation: nav,
	}, nil
}

// ensureLive returns the session's live JS runtime, opening one from the current
// page's original bytes if none exists yet (the first interact pays the page-load
// cost; later interacts are cheap single dispatches). Opening a new runtime first
// enforces the live-runtime cap.
func (b *Browser) ensureLive(ctx context.Context, sess *session.Session) (js.LiveContext, error) {
	if lc := sess.Live(); lc != nil {
		return lc, nil
	}
	b.trimLiveContexts(sess)
	cur := sess.Current()
	if cur == nil {
		return nil, fmt.Errorf("session has no current page")
	}
	tmp := &page.Page{Raw: cur.Raw, RequestURL: cur.RequestURL, FinalURL: cur.FinalURL}
	if err := dom.Parse(tmp); err != nil {
		return nil, err
	}
	env := js.Env{Cookies: cookieAdapter{jar: sess.Client().Jar()}, Storage: sess.Storage(), SessionStorage: sess.SessionStorage()}
	if b.jsNetwork {
		env.Transport = b.newLiveTransport(sess.Client())
	}
	lc, err := b.liveEngine.Open(ctx, tmp.Doc, dom.BaseURL(tmp), env)
	if err != nil {
		return nil, err
	}
	sess.SetLive(lc)
	return lc, nil
}

// trimLiveContexts enforces the live-runtime cap before a new context opens:
// each runtime is a full goja heap plus a goroutine, so the most-idle sessions'
// runtimes are torn down until there is room for one more. The sessions and
// their pages survive — a later interact reopens the runtime from the stored
// page (JS state is lost, exactly as on eviction).
func (b *Browser) trimLiveContexts(opening *session.Session) {
	if b.jsMaxLive <= 0 {
		return
	}
	var lively []*session.Session
	for _, s := range b.sessions.List() {
		if s != opening && s.Live() != nil {
			lively = append(lively, s)
		}
	}
	drop := len(lively) - (b.jsMaxLive - 1)
	if drop <= 0 {
		return
	}
	sort.Slice(lively, func(i, j int) bool { return lively[i].Idle() > lively[j].Idle() })
	for _, s := range lively[:drop] {
		s.Close() // tears down only the live runtime; the session and page remain
		slog.Debug("js: live runtime evicted to honor cap", "session", s.ID, "cap", b.jsMaxLive)
	}
}

// refreshFromLive snapshots the live runtime's current DOM, re-parses it into a
// detached tree, extracts its structure, and stores it as the session's current
// page (so the read pipeline never touches the loop-owned live tree). On snapshot
// failure it degrades to the last stored page.
func (b *Browser) refreshFromLive(ctx context.Context, sess *session.Session, lc js.LiveContext) (*page.Page, error) {
	cur := sess.Current()
	snap, err := lc.Snapshot(ctx)
	if err != nil {
		return cur, nil
	}
	tmp := &page.Page{Raw: snap, RequestURL: cur.RequestURL, FinalURL: cur.FinalURL}
	if err := dom.Parse(tmp); err != nil {
		return cur, nil
	}
	np := *cur // carries the original Raw forward for any later re-open
	np.Doc = tmp.Doc
	np.Article = nil
	np.Markdown = ""
	np.Rendered = true
	if err := dom.Extract(&np); err != nil {
		return cur, nil
	}
	sess.ReplaceCurrentPage(&np)
	return &np, nil
}

// docFingerprint hashes a rendered tree so an interaction can report whether it
// changed the DOM. nil hashes to 0.
func docFingerprint(doc *html.Node) uint64 {
	if doc == nil {
		return 0
	}
	h := fnv.New64a()
	_ = html.Render(h, doc)
	return h.Sum64()
}

func selectForm(forms []page.Form, ref string) (page.Form, bool) {
	ref = strings.TrimSpace(ref)
	for _, f := range forms {
		if (f.ID != "" && f.ID == ref) || (f.Name != "" && f.Name == ref) {
			return f, true
		}
	}
	if i, err := strconv.Atoi(ref); err == nil && i >= 0 && i < len(forms) {
		return forms[i], true
	}
	if ref == "" && len(forms) == 1 {
		return forms[0], true
	}
	return page.Form{}, false
}

// --- session lifecycle / navigation ---

// SessionState is a session's current position. AuthType/AuthScope report the
// session's credentials without ever echoing the secret itself.
type SessionState struct {
	ID         string `json:"id"`
	HasCurrent bool   `json:"has_current"`
	CurrentURL string `json:"current_url,omitempty"`
	Title      string `json:"title,omitempty"`
	LiveJS     bool   `json:"live_js,omitempty"`    // a persistent JS runtime is attached
	AuthType   string `json:"auth_type,omitempty"`  // bearer|basic|headers|cookies (redacted)
	AuthScope  string `json:"auth_scope,omitempty"` // origin the credentials are pinned to
}

// SessionHistory is a session's visited URLs and current index.
type SessionHistory struct {
	ID       string   `json:"id"`
	URLs     []string `json:"urls"`
	Position int      `json:"position"`
}

// NewSession creates a session (generating an id when id is empty) with the given
// credential config and returns its id. Pass session.Config{} for an anonymous
// session.
func (b *Browser) NewSession(id string, cfg session.Config) (string, error) {
	s, err := b.sessions.NewWithConfig(id, cfg)
	if err != nil {
		return "", err
	}
	return s.ID, nil
}

// SessionState returns the current state of an existing session.
func (b *Browser) SessionState(id string) (*SessionState, error) {
	s, err := b.sessions.Get(id)
	if err != nil {
		return nil, err
	}
	st := sessionStateOf(s)
	return &st, nil
}

// sessionStateOf builds a SessionState snapshot without touching secrets.
func sessionStateOf(s *session.Session) SessionState {
	st := SessionState{ID: s.ID}
	if cfg := s.Config(); cfg.HasCredentials() {
		st.AuthScope = cfg.Origin
		switch {
		case cfg.Bearer != "":
			st.AuthType = "bearer"
		case cfg.BasicUser != "":
			st.AuthType = "basic"
		case len(cfg.Headers) > 0:
			st.AuthType = "headers"
		default:
			st.AuthType = "cookies"
		}
	}
	if cur := s.Current(); cur != nil {
		st.HasCurrent = true
		if cur.FinalURL != nil {
			st.CurrentURL = cur.FinalURL.String()
		}
		st.Title = cur.Meta.Title
	}
	st.LiveJS = s.Live() != nil
	return st
}

// SessionList returns the state of every live session, sorted by id.
func (b *Browser) SessionList() []SessionState {
	live := b.sessions.List()
	out := make([]SessionState, 0, len(live))
	for _, s := range live {
		out = append(out, sessionStateOf(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SessionHistoryOf returns the navigation history of an existing session.
func (b *Browser) SessionHistoryOf(id string) (*SessionHistory, error) {
	s, err := b.sessions.Get(id)
	if err != nil {
		return nil, err
	}
	urls, pos := s.HistoryURLs()
	return &SessionHistory{ID: id, URLs: urls, Position: pos}, nil
}

// Back moves a session to the previous page and returns its summary.
func (b *Browser) Back(id string) (*BrowseResult, error) {
	s, err := b.sessions.Get(id)
	if err != nil {
		return nil, err
	}
	p, ok := s.Back()
	if !ok {
		return nil, errf(ErrBadInput, "already at the start of history")
	}
	return summarize(p), nil
}

// Forward moves a session to the next page and returns its summary.
func (b *Browser) Forward(id string) (*BrowseResult, error) {
	s, err := b.sessions.Get(id)
	if err != nil {
		return nil, err
	}
	p, ok := s.Forward()
	if !ok {
		return nil, errf(ErrBadInput, "already at the end of history")
	}
	return summarize(p), nil
}

// CloseSession removes a session, reporting whether one existed.
func (b *Browser) CloseSession(id string) bool {
	return b.sessions.Close(id)
}
