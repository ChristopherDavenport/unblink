package js

import (
	"context"
	"errors"
	"net/url"
	"sync/atomic"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// reqTypeKey tags a request context with the declarativeNetRequest resource type of the
// subrequest about to be issued, so blockingTransport can honor type-scoped rules
// ($script, $xmlhttprequest). It is set at each origin site in this package.
type reqTypeKey struct{}

func withReqType(ctx context.Context, t webext.ResourceType) context.Context {
	return context.WithValue(ctx, reqTypeKey{}, t)
}

func reqTypeFrom(ctx context.Context) webext.ResourceType {
	if t, ok := ctx.Value(reqTypeKey{}).(webext.ResourceType); ok {
		return t
	}
	return webext.TypeOther
}

// scriptCtx / xhrCtx tag a request context with its resource type at the origin site.
// Keeping them here spares the network call sites (scripts/modules/prefetch/async) a
// webext import. They are cheap no-ops of interest only when an extension is loaded.
func scriptCtx(ctx context.Context) context.Context { return withReqType(ctx, webext.TypeScript) }
func xhrCtx(ctx context.Context) context.Context    { return withReqType(ctx, webext.TypeXHR) }

// errBlockedByExtension is returned — faithfully, as a request failure — when a loaded
// extension's network rules cancel a page-JS subrequest, the same outcome a real browser
// produces (net::ERR_BLOCKED_BY_CLIENT).
var errBlockedByExtension = errors.New("blocked by extension")

// blockingTransport gates every page-JS subrequest against the ExtensionHost's network
// rules before forwarding to the inner transport. It sits inside countingTransport, so a
// blocked request still appears (as a failure) in the requests diagnostics. Do runs off
// the loop goroutine, so it holds no bridge state; the matchers it reads are
// concurrency-safe.
type blockingTransport struct {
	inner     Transport
	host      *ExtensionHost
	initiator string       // page origin host, for initiatorDomains + first/third-party
	pageURL   string       // full page URL, for webRequest documentUrl/originUrl
	tabID     int          // synthetic tab id, matching the fired navigation's tab
	blocked   atomic.Int32 // count of cancelled requests (render diagnostic)
}

func (t *blockingTransport) Do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (*Response, error) {
	if u, err := url.Parse(rawURL); err == nil {
		req := webext.Request{URL: u, Method: method, Type: reqTypeFrom(ctx), Initiator: t.initiator}
		// declarativeNetRequest (MV3, static + dynamic) then MV2 webRequest.onBeforeRequest.
		// A redirect target degrades to a block (cancelling the tracker is the safe subset).
		d := t.host.matchNetwork(req)
		if !d.Block && d.RedirectTo == "" {
			d = t.host.webRequestVerdict(req, t.pageURL, t.tabID)
		}
		if d.Block || d.RedirectTo != "" {
			t.blocked.Add(1)
			return nil, errBlockedByExtension
		}
	}
	return t.inner.Do(ctx, method, rawURL, headers, body)
}

// ResetBudget forwards to the inner transport so the live-session per-dispatch budget
// reset still reaches the guarded transport underneath.
func (t *blockingTransport) ResetBudget() {
	if r, ok := t.inner.(interface{ ResetBudget() }); ok {
		r.ResetBudget()
	}
}
