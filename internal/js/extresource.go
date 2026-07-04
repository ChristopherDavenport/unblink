package js

import (
	"context"
	"net/url"
	"strings"
)

// extResourceTransport serves chrome-extension://<id>/path requests from the loaded
// extension's own files (web_accessible_resources / content-script self-access), so a
// content script's fetch(chrome.runtime.getURL(...)) resolves instead of hitting the
// network. It sits inside countingTransport but outside blockingTransport: extension
// resources are counted in diagnostics but never subject to declarativeNetRequest
// blocking or the network byte budget. Any other URL falls through to the inner chain.
type extResourceTransport struct {
	inner Transport
	host  *ExtensionHost
	page  *url.URL
}

func (t *extResourceTransport) Do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (*Response, error) {
	if u, err := url.Parse(rawURL); err == nil && u.Scheme == "chrome-extension" {
		if resp := t.host.serveResource(u, t.page); resp != nil {
			return resp, nil
		}
		return &Response{Status: 404, Headers: map[string]string{}, FinalURL: rawURL}, nil
	}
	return t.inner.Do(ctx, method, rawURL, headers, body)
}

// ResetBudget forwards down the chain so the live-session budget reset still reaches the
// guarded transport.
func (t *extResourceTransport) ResetBudget() {
	if r, ok := t.inner.(interface{ ResetBudget() }); ok {
		r.ResetBudget()
	}
}

// serveResource resolves a chrome-extension:// URL to a response from the owning
// bundle, honoring web_accessible_resources / content-script access. Returns nil when
// the bundle, resource, or access check fails (the caller then returns 404).
func (h *ExtensionHost) serveResource(u *url.URL, page *url.URL) *Response {
	if h == nil {
		return nil
	}
	for _, b := range h.bundles {
		if b.ID != u.Host {
			continue
		}
		if !b.ResourceAccessible(u.Path, page) {
			return nil
		}
		data, err := b.ReadResource(u.Path)
		if err != nil {
			return nil
		}
		return &Response{
			Status:   200,
			Headers:  map[string]string{"content-type": guessContentType(u.Path)},
			Body:     data,
			FinalURL: u.String(),
		}
	}
	return nil
}

// guessContentType maps a resource extension to a MIME type for the extension's own
// fetch (enough for the types extensions load).
func guessContentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".mjs"):
		return "text/javascript"
	case strings.HasSuffix(path, ".json"):
		return "application/json"
	case strings.HasSuffix(path, ".css"):
		return "text/css"
	case strings.HasSuffix(path, ".html"):
		return "text/html"
	case strings.HasSuffix(path, ".svg"):
		return "image/svg+xml"
	default:
		return "text/plain"
	}
}
