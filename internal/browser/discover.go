package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/robots"
	"github.com/christopherdavenport/unblink/internal/search"
	"github.com/christopherdavenport/unblink/internal/sitemap"
)

// Discovery bounds for the map tool. maxURLs/maxDepth are caller-tunable up to a
// hard cap; the rest are fixed safety budgets so a crawl can never run away.
const (
	DefaultMapURLs  = 200
	MaxMapURLs      = 2000
	DefaultMapDepth = 2
	MaxMapDepth     = 5

	mapWallClock    = 60 * time.Second // total time budget for one Map call
	maxSitemapFetch = 50               // sitemap documents fetched per Map call
	maxSitemapDepth = 3                // sitemapindex -> child recursion cap
)

// MapEntry is one discovered URL. Source is "sitemap" (declared in a sitemap) or
// "crawl" (reached by following same-origin links); Depth is the BFS distance
// from the seed for crawl entries (0 for the seed and for sitemap entries).
type MapEntry struct {
	URL    string `json:"url"`
	Source string `json:"source"`
	Depth  int    `json:"depth"`
}

// MapResult is the exposure map of a site's URLs: sitemap-declared URLs plus the
// same-origin links reachable from the seed, bounded and de-duplicated.
type MapResult struct {
	Origin    string     `json:"origin"`              // canonical scheme://host mapped
	Count     int        `json:"count"`               // len(URLs)
	URLs      []MapEntry `json:"urls"`                // discovered URLs (dedup, same-origin)
	Sitemaps  []string   `json:"sitemaps,omitempty"`  // sitemap documents consulted
	Truncated bool       `json:"truncated,omitempty"` // a cap/budget stopped the walk
	Note      string     `json:"note,omitempty"`      // why the walk stopped, when truncated
}

// MapProgress reports a Map walk's progress: URLs discovered so far, the
// max_urls budget, and a short human-readable phase message. Calls are
// throttled; the final call fires when the walk completes. A plain func type so
// the MCP layer can bridge it to progress notifications without any MCP type
// appearing below mcpserver.
type MapProgress func(done, total int, msg string)

// mapProgressInterval throttles progress callbacks so a fast crawl cannot flood
// the transport with notifications.
const mapProgressInterval = 500 * time.Millisecond

// Map discovers a site's URLs: it harvests sitemap.xml (from robots.txt and the
// /sitemap.xml convention, following sitemap indexes) and then crawls same-origin
// links breadth-first from req.URL. It is exposure-grade — it surfaces robots.txt
// context but never skips disallowed paths — and returns a bounded, deduplicated
// list. Budgets (max_urls/max_depth, sitemap fetch/depth caps, a wall-clock) yield
// a partial result with Truncated set rather than an error; only a missing seed
// origin or an unreachable seed is a hard error. Fetches go through the default
// client, so the SSRF dial guard and per-host rate limiter apply automatically.
// progress, when non-nil, receives throttled updates as the walk advances (a Map
// can legitimately run for its full 60s wall-clock).
func (b *Browser) Map(ctx context.Context, req Request, maxURLs, maxDepth int, progress MapProgress) (*MapResult, error) {
	if maxURLs <= 0 {
		maxURLs = DefaultMapURLs
	}
	if maxURLs > MaxMapURLs {
		maxURLs = MaxMapURLs
	}
	if maxDepth <= 0 {
		maxDepth = DefaultMapDepth
	}
	if maxDepth > MaxMapDepth {
		maxDepth = MaxMapDepth
	}
	if originOfURL(req.URL) == "" {
		return nil, errf(ErrBadInput, "a url is required")
	}

	deadline := time.Now().Add(mapWallClock)

	// Fetch the seed once (render off — exposure maps stay cheap and deterministic).
	// This doubles as BFS depth 0 and yields the post-redirect canonical origin.
	seed, err := b.fetchPage(ctx, b.client, req.URL, renderOpts{})
	if err != nil {
		return nil, err
	}
	crawlOrigin := originOfURL(req.URL)
	if seed.FinalURL != nil && seed.FinalURL.Host != "" {
		crawlOrigin = seed.FinalURL.Scheme + "://" + seed.FinalURL.Host
	}

	res := &MapResult{Origin: crawlOrigin}
	visited := map[string]bool{}

	// notify emits a throttled progress update; the unthrottled final call below
	// always reports the completed count.
	notify := func(string) {}
	if progress != nil {
		var lastNotify time.Time
		notify = func(msg string) {
			if time.Since(lastNotify) < mapProgressInterval {
				return
			}
			lastNotify = time.Now()
			progress(len(res.URLs), maxURLs, msg)
		}
	}

	// record adds a discovered URL. It returns false only when maxURLs is reached
	// (the caller should stop); a duplicate or unparseable URL is skipped silently.
	record := func(rawURL, source string, depth int) bool {
		norm := normalizeMapURL(rawURL)
		if norm == "" || visited[norm] {
			return true
		}
		if len(res.URLs) >= maxURLs {
			res.Truncated = true
			if res.Note == "" {
				res.Note = "reached max_urls"
			}
			return false
		}
		visited[norm] = true
		res.URLs = append(res.URLs, MapEntry{URL: norm, Source: source, Depth: depth})
		return true
	}

	// The seed itself is the first result (crawl depth 0).
	seedURL := req.URL
	if seed.FinalURL != nil {
		seedURL = seed.FinalURL.String()
	}
	record(seedURL, "crawl", 0)

	// --- sitemaps first ---
	crawlDelay := b.harvestSitemaps(ctx, crawlOrigin, maxURLs, deadline, record, notify, res)

	// --- same-origin BFS crawl fills the remaining budget ---
	b.crawlSameOrigin(ctx, seed, crawlOrigin, maxURLs, maxDepth, crawlDelay, deadline, visited, record, notify, res)

	res.Count = len(res.URLs)
	if progress != nil {
		progress(len(res.URLs), maxURLs, "map complete")
	}
	return res, nil
}

// harvestSitemaps seeds from robots.txt Sitemap: directives plus the /sitemap.xml
// convention, follows sitemap indexes (bounded), and records same-origin <loc>
// URLs. It returns the star-group Crawl-delay (surfaced for pacing; never gates).
func (b *Browser) harvestSitemaps(ctx context.Context, origin string, maxURLs int, deadline time.Time, record func(string, string, int) bool, notify func(string), res *MapResult) float64 {
	var pol *robots.Policy
	if r, err := b.client.Fetch(ctx, http.MethodGet, origin+"/robots.txt", nil, nil); err == nil && r != nil {
		pol = robots.Parse(r.Body)
	} else {
		pol = &robots.Policy{}
	}
	var crawlDelay float64
	if g := pol.StarGroup(); g != nil {
		crawlDelay = g.CrawlDelay
	}

	type smItem struct {
		url   string
		depth int
	}
	var queue []smItem
	seen := map[string]bool{}
	enqueue := func(u string, depth int) {
		n := normalizeMapURL(u)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		queue = append(queue, smItem{url: n, depth: depth})
	}
	for _, sm := range pol.Sitemaps {
		enqueue(sm, 0)
	}
	enqueue(origin+"/sitemap.xml", 0)

	fetches := 0
	for len(queue) > 0 {
		if time.Now().After(deadline) {
			res.Truncated, res.Note = true, "reached wall-clock budget"
			return crawlDelay
		}
		if len(res.URLs) >= maxURLs {
			res.Truncated, res.Note = true, "reached max_urls"
			return crawlDelay
		}
		if fetches >= maxSitemapFetch {
			res.Truncated, res.Note = true, "reached sitemap fetch budget"
			return crawlDelay
		}
		item := queue[0]
		queue = queue[1:]
		fetches++
		notify("harvesting sitemap " + item.url)

		r, err := b.client.Fetch(ctx, http.MethodGet, item.url, nil, nil)
		if err != nil {
			continue // best-effort; a bad sitemap URL is skipped
		}
		if r.Status < 200 || r.Status >= 300 {
			continue
		}
		res.Sitemaps = append(res.Sitemaps, item.url)

		doc, err := sitemap.Parse(r.Body, maxURLs-len(res.URLs))
		if err != nil {
			continue
		}
		switch doc.Kind {
		case sitemap.KindIndex:
			if item.depth < maxSitemapDepth {
				for _, c := range doc.Sitemaps {
					enqueue(c.URL, item.depth+1) // children are declared by this origin
				}
			}
		case sitemap.KindURLSet:
			for _, loc := range doc.URLs {
				if originOfURL(loc.URL) != origin {
					continue // drop cross-origin locs (e.g. a CDN-hosted sitemap)
				}
				if !record(loc.URL, "sitemap", 0) {
					return crawlDelay // hit max_urls
				}
			}
		}
	}
	return crawlDelay
}

// crawlSameOrigin runs a bounded breadth-first crawl from the already-fetched seed,
// following only same-origin links (recomputed by scheme+host — page.Link.Internal
// is registrable-domain, too loose). A page that redirects off-origin is recorded
// but not expanded (open-redirector guard). crawlDelay paces fetches (politeness).
func (b *Browser) crawlSameOrigin(ctx context.Context, seed *page.Page, origin string, maxURLs, maxDepth int, crawlDelay float64, deadline time.Time, visited map[string]bool, record func(string, string, int) bool, notify func(string), res *MapResult) {
	type crawlItem struct {
		page  *page.Page // non-nil only for the seed (already fetched)
		url   string
		depth int
	}
	frontier := []crawlItem{{page: seed, depth: 0}}
	var lastFetch time.Time

	// queued tracks URLs already fetched/enqueued for expansion — distinct from the
	// results dedup (visited), so a page first seen in the sitemap is still crawled
	// into (its links discover URLs the sitemap omits).
	queued := map[string]bool{}
	if seed.FinalURL != nil {
		if n := normalizeMapURL(seed.FinalURL.String()); n != "" {
			queued[n] = true
		}
	}

	for len(frontier) > 0 {
		if time.Now().After(deadline) {
			res.Truncated, res.Note = true, "reached wall-clock budget"
			return
		}
		if len(res.URLs) >= maxURLs {
			res.Truncated, res.Note = true, "reached max_urls"
			return
		}
		item := frontier[0]
		frontier = frontier[1:]

		p := item.page
		if p == nil {
			if crawlDelay > 0 {
				wait := time.Duration(crawlDelay*float64(time.Second)) - time.Since(lastFetch)
				if wait > 0 {
					select {
					case <-time.After(wait):
					case <-ctx.Done():
						res.Truncated, res.Note = true, "cancelled"
						return
					}
				}
			}
			notify(fmt.Sprintf("crawling depth %d: %s", item.depth, item.url))
			fetched, err := b.fetchPage(ctx, b.client, item.url, renderOpts{})
			lastFetch = time.Now()
			if err != nil {
				continue // best-effort; skip an unreachable page
			}
			p = fetched
			// Redirect-drift guard: if this page redirected off-origin, keep the
			// recorded entry but do not harvest its (off-origin) links.
			if p.FinalURL != nil && p.FinalURL.Host != "" &&
				p.FinalURL.Scheme+"://"+p.FinalURL.Host != origin {
				continue
			}
		}

		for _, l := range p.Meta.Links {
			if originOfURL(l.Href) != origin {
				continue // strict same-origin (scheme+host)
			}
			norm := normalizeMapURL(l.Href)
			if norm == "" {
				continue
			}
			if !record(norm, "crawl", item.depth+1) {
				return // hit max_urls
			}
			// Enqueue for expansion (dedup by queued) while within maxDepth — even
			// if it was already recorded via the sitemap.
			if item.depth+1 < maxDepth && !queued[norm] {
				queued[norm] = true
				frontier = append(frontier, crawlItem{url: norm, depth: item.depth + 1})
			}
		}
	}
}

// normalizeMapURL canonicalizes a URL for dedup/membership: http(s) only, host
// required, fragment dropped. Returns "" for anything else.
func normalizeMapURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// --- search ---

// ErrSearchDisabled is returned by Search when no provider has been configured.
var ErrSearchDisabled = errors.New("search is not configured (start unblink with --search-provider)")

// Search count bounds.
const (
	DefaultSearchCount = 10
	MaxSearchCount     = 20
)

// SearchResult is the outcome of a web search via the configured provider.
type SearchResult struct {
	Provider string          `json:"provider"`
	Query    string          `json:"query"`
	Count    int             `json:"count"`
	Results  []search.Result `json:"results"`
}

// SearchEnabled reports whether a search provider is configured.
func (b *Browser) SearchEnabled() bool { return b.search != nil }

// Search runs query against the configured provider. It returns ErrSearchDisabled
// when none is set. count is clamped to [1, MaxSearchCount] (default
// DefaultSearchCount); site optionally restricts results to one domain.
func (b *Browser) Search(ctx context.Context, query string, count int, site string) (*SearchResult, error) {
	if b.search == nil {
		return nil, ErrSearchDisabled
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errf(ErrBadInput, "query is required")
	}
	if count <= 0 {
		count = DefaultSearchCount
	}
	if count > MaxSearchCount {
		count = MaxSearchCount
	}
	results, err := b.search.Search(ctx, query, search.Options{Count: count, Site: strings.TrimSpace(site)})
	if err != nil {
		return nil, err
	}
	return &SearchResult{
		Provider: b.search.Name(),
		Query:    query,
		Count:    len(results),
		Results:  results,
	}, nil
}
