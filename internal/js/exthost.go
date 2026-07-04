package js

import (
	"net/url"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// ExtensionHost holds engine-lifetime WebExtension state, shared read-only across
// every concurrent render and live session — analogous to one browser process sharing
// its loaded extensions across all tabs. Phase 1 carries only the network filter
// matchers (static declarativeNetRequest rulesets); later phases add the background
// worker, messaging broker, and storage.
type ExtensionHost struct {
	bundles []*webext.Bundle
}

// newExtensionHost builds a host from the loaded bundles, or returns nil when none are
// configured (so the JS engine and its transports stay on their normal, unwrapped path).
func newExtensionHost(bundles []*webext.Bundle) *ExtensionHost {
	if len(bundles) == 0 {
		return nil
	}
	return &ExtensionHost{bundles: bundles}
}

// matchNetwork consults every loaded extension's rules; the first extension that blocks
// (or redirects) a request wins, matching how browsers apply extensions independently
// and cancel a request if any of them blocks it. Safe for concurrent use: the matchers
// are read-only in Phase 1.
func (h *ExtensionHost) matchNetwork(req webext.Request) webext.Decision {
	if h == nil {
		return webext.Decision{}
	}
	for _, b := range h.bundles {
		d := b.Net.Match(req)
		if d.Block || d.RedirectTo != "" {
			return d
		}
	}
	return webext.Decision{}
}

// initiatorHost returns the page origin host used for initiatorDomains and
// first/third-party rule conditions.
func initiatorHost(base *url.URL) string {
	if base == nil {
		return ""
	}
	return base.Hostname()
}
