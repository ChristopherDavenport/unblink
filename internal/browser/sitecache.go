package browser

import (
	"sync"
	"time"
)

// siteCache is a small TTL cache of per-origin site metadata (parsed robots.txt
// + llms.txt presence/content), keyed by "scheme://host". It mirrors cache.go
// but with a longer TTL, since robots.txt and llms.txt change rarely. Negative
// results (no robots.txt, no llms.txt) are cached too, so a repeated browse of a
// host does not re-probe its origin-root files.
type siteCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	cap     int
	entries map[string]siteCacheEntry
}

type siteCacheEntry struct {
	info *siteInfo
	at   time.Time
}

func newSiteCache(ttl time.Duration, capacity int) *siteCache {
	return &siteCache{ttl: ttl, cap: capacity, entries: make(map[string]siteCacheEntry)}
}

func (c *siteCache) get(origin string) (*siteInfo, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[origin]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > c.ttl {
		delete(c.entries, origin)
		return nil, false
	}
	return e.info, true
}

func (c *siteCache) put(origin string, info *siteInfo) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[origin]; !exists && len(c.entries) >= c.cap {
		c.evictOldest()
	}
	c.entries[origin] = siteCacheEntry{info: info, at: time.Now()}
}

// evictOldest removes the least-recently-stored entry. Caller holds c.mu.
func (c *siteCache) evictOldest() {
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range c.entries {
		if first || e.at.Before(oldestAt) {
			oldestKey, oldestAt, first = k, e.at, false
		}
	}
	if !first {
		delete(c.entries, oldestKey)
	}
}
