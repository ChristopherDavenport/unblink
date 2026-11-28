package js

import (
	"strings"
	"sync"
	"time"
)

// assetCache is the Engine-owned, TTL'd cross-render cache for JS *assets*:
// external classic-script bytes, module sources, and each module's esbuild
// CommonJS transform (keyed by a hash of its source). Every render used to re-download and
// re-bundle the same site bundles. Page *data* requests (fetch/XHR) never go
// through it — those stay live.
//
// Staleness posture: the browser already serves whole rendered pages from a
// 60s cache, so "a bundle may change server-side within the TTL" is within the
// product's accepted staleness; --js-asset-cache=false opts out. Cache hits
// bypass the transport entirely, so they do not count toward NetRequests
// (which reports real network attempts) or the per-render download budget.
type assetCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	cur, prev map[string]assetEntry // two-generation swap eviction, by summed bytes
	curBytes  int64
}

type assetEntry struct {
	body []byte
	at   time.Time
}

// assetCacheMaxBytes bounds the summed body bytes per generation (two generations
// live at once, so worst-case memory is 2×). Sized at/above the per-render download
// budget (DefaultJSMaxBytes) so a heavy code-split SPA's chunk graph stays in one
// generation without a mid-render swap that would force re-fetching evicted chunks —
// the 256-entry count cap this replaces thrashed on exactly that graph. Still well
// under the 1 GiB heap guard (ADR 0003).
const assetCacheMaxBytes = 64 << 20

func newAssetCache(ttl time.Duration) *assetCache {
	if ttl <= 0 {
		return nil
	}
	return &assetCache{ttl: ttl, cur: map[string]assetEntry{}}
}

// get is nil-safe: a disabled cache (nil) always misses.
func (a *assetCache) get(key string) ([]byte, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e, ok := a.cur[key]; ok && time.Since(e.at) < a.ttl {
		return e.body, true
	}
	if e, ok := a.prev[key]; ok && time.Since(e.at) < a.ttl {
		a.cur[key] = e
		a.curBytes += int64(len(e.body))
		return e.body, true
	}
	return nil, false
}

func (a *assetCache) put(key string, body []byte) {
	if a == nil || int64(len(body)) > assetCacheMaxBytes {
		return
	}
	// A data: URL is its own body: caching it would store the bytes twice (once
	// as the key) to save a decode.
	if strings.HasPrefix(key, "asset\x00data:") || strings.HasPrefix(key, "asset\x00DATA:") {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.curBytes+int64(len(body)) > assetCacheMaxBytes {
		a.prev, a.cur = a.cur, map[string]assetEntry{}
		a.curBytes = 0
	}
	a.cur[key] = assetEntry{body: body, at: time.Now()}
	a.curBytes += int64(len(body))
}

// assetKey is the cache key for fetched script/module bytes.
func assetKey(absURL string) string {
	return "asset\x00" + strings.TrimSpace(absURL)
}
