package js

import (
	"context"
	"sync"
	"sync/atomic"
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
	Status   int
	Headers  map[string]string
	Body     []byte
	FinalURL string
}

// countingTransport wraps the caller's Transport to count subrequests for render
// diagnostics. It sits at the one choke point every page network path shares
// (fetch/XHR, external scripts, ES modules, dynamic import), so the counts cover
// them all. Failures include everything Do can reject: network errors and the
// browser's budget/rate denials alike. Counters are atomics because fetch/XHR and
// script loads call Do off the loop goroutine.
type countingTransport struct {
	inner  Transport
	total  atomic.Int32 // requests attempted
	failed atomic.Int32 // requests that returned an error

	// records is a capped, ordered log of each subrequest for the requests tool.
	// fetch/XHR/script loads call Do off the loop goroutine, so it's mutex-guarded.
	mu      sync.Mutex
	records []reqRecord
	dropped int // records past the cap, reported as truncation
}

// reqRecord is one logged subrequest.
type reqRecord struct {
	method string
	url    string
	status int    // 0 when the request errored before a response
	errMsg string // non-empty when the request failed
}

// maxReqRecords bounds the request log so a page that fires thousands of requests
// can't grow it unbounded.
const maxReqRecords = 256

func (t *countingTransport) Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (*Response, error) {
	t.total.Add(1)
	res, err := t.inner.Do(ctx, method, url, headers, body)
	rec := reqRecord{method: method, url: url}
	if err != nil {
		t.failed.Add(1)
		rec.errMsg = err.Error()
	}
	if res != nil {
		rec.status = res.Status
	}
	t.mu.Lock()
	if len(t.records) < maxReqRecords {
		t.records = append(t.records, rec)
	} else {
		t.dropped++
	}
	t.mu.Unlock()
	return res, err
}

// snapshotRecords returns a copy of the request log and whether any were dropped.
func (t *countingTransport) snapshotRecords() ([]reqRecord, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]reqRecord(nil), t.records...), t.dropped > 0
}
