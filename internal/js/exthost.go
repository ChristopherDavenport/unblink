package js

import (
	"net/url"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// ExtensionHost holds engine-lifetime WebExtension state, shared read-only across
// every concurrent render and live session — analogous to one browser process sharing
// its loaded extensions across all tabs. Phase 1 carries the network filter matchers
// (static declarativeNetRequest rulesets); Phase 2 adds a simple shared storage backing
// chrome.storage. Later phases add the background worker and messaging broker.
type ExtensionHost struct {
	bundles []*webext.Bundle
	storage *extStore
	broker  *msgBroker
	bg      *bgWorker // background worker for the first extension declaring one; nil otherwise
}

// newExtensionHost builds a host from the loaded bundles, or returns nil when none are
// configured (so the JS engine and its transports stay on their normal, unwrapped path).
func newExtensionHost(bundles []*webext.Bundle) *ExtensionHost {
	if len(bundles) == 0 {
		return nil
	}
	h := &ExtensionHost{bundles: bundles, storage: newExtStore(extStorageDir())}
	h.broker = &msgBroker{host: h}
	for _, b := range bundles {
		if b.Manifest != nil && (b.Manifest.Background.ServiceWorker != "" || len(b.Manifest.Background.Scripts) > 0) {
			h.bg = &bgWorker{bundle: b, host: h}
			break
		}
	}
	return h
}

// startBackground launches the background worker (if any) on its own eventloop. Called
// once by the engine right after construction so the memory guard tracks its runtime.
func (h *ExtensionHost) startBackground(memGuard *memGuard) {
	if h != nil && h.bg != nil {
		h.bg.start(memGuard)
	}
}

// Close tears down the background worker's runtime.
func (h *ExtensionHost) Close() {
	if h != nil && h.bg != nil {
		h.bg.close()
	}
}

// activeBundleFor picks the extension whose chrome.* the page's content scripts see.
// With unblink's same-world content-script model (one JS global; ADR 0010) there is a
// single shared chrome object, so it binds to the first extension with a content script
// matching the page, else the first loaded extension.
func (h *ExtensionHost) activeBundleFor(base *url.URL) *webext.Bundle {
	if h == nil || len(h.bundles) == 0 {
		return nil
	}
	if base != nil {
		for _, b := range h.bundles {
			if len(b.ContentScriptsFor(base)) > 0 {
				return b
			}
		}
	}
	return h.bundles[0]
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
