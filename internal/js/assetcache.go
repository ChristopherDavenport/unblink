package js

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// assetCache is the Engine-owned, TTL'd cross-render cache for JS *assets*:
// external classic-script bytes, module sources fetched by esbuild's loader,
// and finished esbuild bundle outputs. Every render used to re-download and
// re-bundle the same site bundles. Page *data* requests (fetch/XHR) never go
// through it — those stay live.
//
// Staleness posture: the browser already serves whole rendered pages from a
// 60s cache, so "a bundle may change server-side within the TTL" is within the
// product's accepted staleness; --js-asset-cache=false opts out. Cache hits
// bypass the transport entirely, so they do not count toward NetRequests
// (which reports real network attempts) or the per-render request budget.
type assetCache struct {
	mu        sync.Mutex
	ttl       time.Duration
	cur, prev map[string]assetEntry // two-generation swap eviction
	curBytes  int64
}

type assetEntry struct {
	body []byte
	at   time.Time
}

const (
	assetCacheMaxBytes   = 16 << 20 // summed body bytes per generation
	assetCacheMaxEntries = 256
)

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
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cur) >= assetCacheMaxEntries || a.curBytes+int64(len(body)) > assetCacheMaxBytes {
		a.prev, a.cur = a.cur, map[string]assetEntry{}
		a.curBytes = 0
	}
	a.cur[key] = assetEntry{body: body, at: time.Now()}
	a.curBytes += int64(len(body))
}

// bundleKey builds the cache key for an esbuild output: the bundle is a pure
// function of the entry source, the base URL its imports resolve against, and
// the import map — all hashed (import map serialized in sorted order). The
// base's query and fragment are cleared first: relative specifiers resolve
// against scheme/host/path only, so `?utm=`-style variants of the same page
// must not each pay a full esbuild rebuild of the module graph.
func bundleKey(base *url.URL, entry string, importMap map[string]string) string {
	h := sha256.New()
	if base != nil {
		c := *base
		c.RawQuery, c.Fragment, c.RawFragment = "", "", ""
		h.Write([]byte(c.String()))
	}
	h.Write([]byte{0})
	h.Write([]byte(entry))
	h.Write([]byte{0})
	if len(importMap) > 0 {
		keys := make([]string, 0, len(importMap))
		for k := range importMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			h.Write([]byte(k))
			h.Write([]byte{1})
			h.Write([]byte(importMap[k]))
			h.Write([]byte{2})
		}
	}
	return "bundle\x00" + hex.EncodeToString(h.Sum(nil))
}

// assetKey is the cache key for fetched script/module bytes.
func assetKey(absURL string) string {
	return "asset\x00" + strings.TrimSpace(absURL)
}
