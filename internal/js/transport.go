package js

import "context"

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
