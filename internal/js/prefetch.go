package js

import (
	"context"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// prefetchWorkers bounds how many script bodies are fetched at once. Small on
// purpose: enough to overlap RTTs, not enough to stampede a host (the shared
// per-host rate limiter and the per-render request budget still apply to every
// prefetch through the counting/guarded transport).
const prefetchWorkers = 4

// prefetchEntry is one in-flight (or finished) script-body fetch. done is
// closed exactly once when body/ok are final; readers block on it.
type prefetchEntry struct {
	done chan struct{}
	body []byte
	ok   bool
}

// startPrefetch fetches the bodies of the collected external classic
// <script src> tags concurrently, so the strictly document-ordered execute pass
// in runScripts waits on max(RTT) instead of sum(RTT). runScripts executes the
// same collected snapshot, so prefetch fetches exactly the set that will run —
// no speculative over-fetch — and every request goes through the same counting,
// budgeted, SSRF-guarded transport as the synchronous path. Requests are
// deliberately not pending-bracketed: they are engine plumbing, not
// page-observable work, and runScripts consumes every entry before the settle
// starts. Identical srcs are fetched once (the synchronous path dedupes the
// same way whenever the asset cache is on).
func (b *bridge) startPrefetch(scripts []*html.Node) {
	if b.transport == nil {
		return
	}
	var urls []string
	seen := make(map[string]bool)
	for _, s := range scripts {
		srcAttr := strings.TrimSpace(getAttr(s, "src"))
		if srcAttr == "" {
			continue
		}
		abs, err := b.resolveURL(srcAttr)
		if err != nil || seen[abs] {
			continue
		}
		if _, ok := b.assets.get(assetKey(abs)); ok {
			continue // already warm; runScripts hits the cache directly
		}
		seen[abs] = true
		urls = append(urls, abs)
	}
	if len(urls) < 2 {
		return // nothing to overlap; keep the single-fetch path byte-identical
	}
	b.prefetch = make(map[string]*prefetchEntry, len(urls))
	queue := make(chan string)
	var wg sync.WaitGroup
	for _, u := range urls {
		b.prefetch[u] = &prefetchEntry{done: make(chan struct{})}
	}
	workers := prefetchWorkers
	if len(urls) < workers {
		workers = len(urls)
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range queue {
				e := b.prefetch[u]
				ctx, cancel := context.WithTimeout(b.ctx, b.reqTimeout)
				res, err := b.transport.Do(ctx, "GET", u, nil, nil)
				cancel()
				if err == nil && res != nil && res.Status < 400 {
					e.body = res.Body
					e.ok = true
					b.assets.put(assetKey(u), res.Body)
				}
				close(e.done)
			}
		}()
	}
	go func() {
		for _, u := range urls {
			queue <- u
		}
		close(queue)
		wg.Wait()
	}()
}

// prefetched returns the finished prefetch result for abs, blocking until the
// fetch completes (the same blocking the synchronous path would have paid, but
// overlapped with the other scripts' fetches). ok=false means abs was not
// prefetched; found failures mirror the synchronous path's skip semantics.
func (b *bridge) prefetched(abs string) (body []byte, ok, found bool) {
	e, hit := b.prefetch[abs]
	if !hit {
		return nil, false, false
	}
	<-e.done
	return e.body, e.ok, true
}
