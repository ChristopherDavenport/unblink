package js

import (
	"context"
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
}

func (t *countingTransport) Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (*Response, error) {
	t.total.Add(1)
	res, err := t.inner.Do(ctx, method, url, headers, body)
	if err != nil {
		t.failed.Add(1)
	}
	return res, err
}
