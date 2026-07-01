package browser

import (
	"sync"
	"time"

	"github.com/christopherdavenport/unblink/internal/page"
)

// cache is a small TTL cache of fetched+parsed pages keyed by URL. Cached pages
// hold only the fetch/parse/extract results (Doc + Meta) and are treated as
// read-only by callers — Read copies the page before running reduce/emit. It is
// a precursor to the Phase 3 session store.
type cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	cap     int
	entries map[string]cacheEntry
}

type cacheEntry struct {
	page *page.Page
	at   time.Time
}

func newCache(ttl time.Duration, capacity int) *cache {
	return &cache{ttl: ttl, cap: capacity, entries: make(map[string]cacheEntry)}
}

func (c *cache) get(key string) (*page.Page, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > c.ttl {
		delete(c.entries, key)
		return nil, false
	}
	return e.page, true
}

func (c *cache) put(key string, p *page.Page) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.cap {
		c.evictOldest()
	}
	c.entries[key] = cacheEntry{page: p, at: time.Now()}
}

// evictOldest removes the least-recently-stored entry. Caller holds c.mu.
func (c *cache) evictOldest() {
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
