package js

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// Transport performs HTTP requests on behalf of page JavaScript — external
// <script src>, window.fetch, and XMLHttpRequest. The browser supplies an
// implementation bound to the right cookie jar and wrapped with the network
// policy (SSRF guard + per-render budget). Keeping this an interface means the js
// package never imports internal/fetch and stays decoupled.
type Transport interface {
	Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (*Response, error)
}

// Response is a raw HTTP response for in-page JavaScript (body already read).
type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
	// Raw is the body before the charset→UTF-8 transcode (the octets the server
	// sent), used only for Subresource Integrity hashing (see sri.go). May be nil
	// for synthesized responses (e.g. extension resources); SRI callers fall back
	// to Body when it is.
	Raw      []byte
	FinalURL string
}

// countingTransport wraps the caller's Transport to count subrequests for render
// diagnostics. It sits at the one choke point every page network path shares
// (fetch/XHR, external scripts, ES modules, dynamic import), so the counts cover
// them all. Failures include everything Do can reject: network errors and the
// browser's budget/rate denials alike. Counters are atomics because fetch/XHR and
// script loads call Do off the loop goroutine.
type countingTransport struct {
	inner     Transport
	total     atomic.Int32 // requests attempted
	failed    atomic.Int32 // requests that returned an error
	bytesDown atomic.Int64 // response-body bytes downloaded (diagnostic; independent of the budget)

	// records is a bounded, ordered log of each subrequest for the requests tool,
	// kept as a ring: once full it evicts the OLDEST entry so the most recent
	// maxReqRecords survive. That matters because a code-split SPA loads its
	// scripts first and fires its data (fetch/XHR) calls last — a head-keeping cap
	// would drop exactly the endpoint the requests tool exists to surface.
	// fetch/XHR/script loads call Do off the loop goroutine, so it's mutex-guarded.
	mu      sync.Mutex
	records []reqRecord
	start   int // index of the oldest record once the ring is full
	dropped int // records evicted past the cap, reported as truncation
}

// reqRecord is one logged subrequest.
type reqRecord struct {
	method string
	url    string
	status int    // 0 when the request errored before a response
	errMsg string // non-empty when the request failed
	kind   string // devtools-like resource type (see resourceKind)
}

// maxReqRecords bounds the request log so a page that fires many thousands of
// requests can't grow it unbounded. Sized to hold every subrequest of a heavy
// modern SPA (hundreds of code-split chunks plus its data calls) so nothing is
// dropped in practice; the ring only bites on pathological pages.
const maxReqRecords = 4096

func (t *countingTransport) Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (*Response, error) {
	t.total.Add(1)
	res, err := t.inner.Do(ctx, method, url, headers, body)
	rec := reqRecord{method: method, url: url}
	if err != nil {
		t.failed.Add(1)
		rec.errMsg = err.Error()
	}
	ctype := ""
	if res != nil {
		rec.status = res.Status
		ctype = res.Headers["content-type"]
		t.bytesDown.Add(int64(len(res.Body)))
	}
	rec.kind = resourceKind(reqTypeFrom(ctx), ctype, url)
	t.mu.Lock()
	if len(t.records) < maxReqRecords {
		t.records = append(t.records, rec)
	} else {
		// Ring is full: overwrite the oldest slot, advance start, count the drop.
		t.records[t.start] = rec
		t.start = (t.start + 1) % maxReqRecords
		t.dropped++
	}
	t.mu.Unlock()
	return res, err
}

// resourceKind labels a subrequest with a browser-devtools-style resource type
// for the requests tool. It reads the issuing call site's initiator (script vs
// fetch/XHR, carried in ctx by scriptCtx/xhrCtx) and refines it with the response
// Content-Type. A script/module/import load is always "js"; otherwise the
// Content-Type names the kind (a fetch that pulls an image/stylesheet/font/media/
// document is labeled as such), and a data fetch/XHR with no asset Content-Type
// (JSON, text, form, binary) is "xhr". unblink only ever initiates script and
// fetch/XHR subrequests — it loads no CSS/fonts/images/media of its own and opens
// no WebSocket — so css/fonts/images/media/html/websocket appear only when page JS
// fetches one, and "websocket" is never emitted today.
func resourceKind(reqType webext.ResourceType, contentType, rawURL string) string {
	if lu := strings.ToLower(rawURL); strings.HasPrefix(lu, "ws://") || strings.HasPrefix(lu, "wss://") {
		return "websocket"
	}
	// A script/module/dynamic-import load is JS regardless of how the origin
	// server mislabels the response.
	if reqType == webext.TypeScript {
		return "js"
	}
	ct := contentType
	if i := strings.IndexByte(ct, ';'); i >= 0 { // drop parameters (charset, boundary)
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case ct == "text/html", ct == "application/xhtml+xml":
		return "html"
	case ct == "text/css":
		return "css"
	case strings.HasPrefix(ct, "image/"):
		return "images"
	case strings.HasPrefix(ct, "audio/"), strings.HasPrefix(ct, "video/"), ct == "application/ogg":
		return "media"
	case strings.HasPrefix(ct, "font/"),
		ct == "application/font-woff", ct == "application/font-woff2",
		ct == "application/vnd.ms-fontobject",
		ct == "application/x-font-ttf", ct == "application/x-font-otf", ct == "application/x-font-woff":
		return "fonts"
	case ct == "application/javascript", ct == "text/javascript",
		ct == "application/ecmascript", ct == "text/ecmascript",
		ct == "application/x-javascript", ct == "application/mjs":
		return "js"
	}
	// No asset Content-Type: a fetch/XHR data call (JSON/text/XML/form/binary);
	// the initiator distinguishes it from a truly unknown request.
	if reqType == webext.TypeXHR {
		return "xhr"
	}
	return "other"
}

// ResetBudget clears the underlying transport's per-render/per-dispatch download
// budget, if it implements one. Called at the start of each live-session dispatch
// so a long session gets a fresh budget per agent action. The diagnostic totals
// (total/failed/records) are cumulative and intentionally left untouched.
func (t *countingTransport) ResetBudget() {
	if r, ok := t.inner.(interface{ ResetBudget() }); ok {
		r.ResetBudget()
	}
}

// snapshotRecords returns a copy of the request log in oldest-to-newest order and
// whether any were dropped. The ring may have wrapped, so entries are read from
// start; before the ring fills, start is 0 and this is a plain copy.
func (t *countingTransport) snapshotRecords() ([]reqRecord, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.records)
	if n == 0 {
		return nil, false
	}
	out := make([]reqRecord, n)
	for i := 0; i < n; i++ {
		out[i] = t.records[(t.start+i)%n]
	}
	return out, t.dropped > 0
}
