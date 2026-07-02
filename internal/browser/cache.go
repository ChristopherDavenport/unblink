package browser

import (
	"sync"
	"time"

	"github.com/christopherdavenport/unblink/internal/page"
)

// staleFactor sets how long past its TTL a cache entry is retained for HTTP
// conditional revalidation (If-None-Match/If-Modified-Since). A 304 then reuses
// the parsed page instead of refetching and re-parsing the body. Past
// ttl*staleFactor the entry is dropped outright.
const staleFactor = 10

// cache is a small TTL cache of fetched+parsed pages keyed by URL. Cached pages
// hold only the fetch/parse/extract results (Doc + Meta) and are treated as
// read-only by callers — Read copies the page before running reduce/emit.
// Expired entries linger (bounded by staleFactor and the capacity cap) so the
// fetch path can revalidate them with a conditional request.
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

// get returns a fresh (within-TTL) entry. An entry past its revalidation window
// is dropped; one merely expired is kept for getStale.
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
	age := time.Since(e.at)
	if age > c.ttl*staleFactor {
		delete(c.entries, key)
		return nil, false
	}
	if age > c.ttl {
		return nil, false
	}
	return e.page, true
}

// getStale returns an expired-but-retained entry, the candidate for a
// conditional revalidation. It never returns a fresh entry (get already did).
func (c *cache) getStale(key string) (*page.Page, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	age := time.Since(e.at)
	if age <= c.ttl || age > c.ttl*staleFactor {
		return nil, false
	}
	return e.page, true
}

// touch re-dates an entry after a 304 Not Modified: the origin confirmed the
// cached copy is current, so it is fresh again for a full TTL.
func (c *cache) touch(key string) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.at = time.Now()
		c.entries[key] = e
	}
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
