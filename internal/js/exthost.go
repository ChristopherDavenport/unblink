package js

import (
	"net/url"
	"strings"
	"sync"

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
}

// newExtensionHost builds a host from the loaded bundles, or returns nil when none are
// configured (so the JS engine and its transports stay on their normal, unwrapped path).
func newExtensionHost(bundles []*webext.Bundle) *ExtensionHost {
	if len(bundles) == 0 {
		return nil
	}
	return &ExtensionHost{bundles: bundles, storage: newExtStore()}
}

// extStore is the in-memory backing for chrome.storage (local/session/sync/managed),
// engine-lifetime and shared across renders. Values are plain Go (JSON-compatible) so
// they cross goja runtimes safely. Phase 3 adds disk persistence + onChanged fan-out.
type extStore struct {
	mu sync.Mutex
	m  map[string]any // full key: "<extID>\x00<area>\x00<key>"
}

func newExtStore() *extStore { return &extStore{m: map[string]any{}} }

func (s *extStore) get(prefix string, keys []string, all bool) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{}
	if all {
		for k, v := range s.m {
			if rest, ok := strings.CutPrefix(k, prefix); ok {
				out[rest] = v
			}
		}
		return out
	}
	for _, k := range keys {
		if v, ok := s.m[prefix+k]; ok {
			out[k] = v
		}
	}
	return out
}

func (s *extStore) set(prefix string, kv map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range kv {
		s.m[prefix+k] = v
	}
}

func (s *extStore) remove(prefix string, keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.m, prefix+k)
	}
}

func (s *extStore) clear(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.m {
		if strings.HasPrefix(k, prefix) {
			delete(s.m, k)
		}
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
