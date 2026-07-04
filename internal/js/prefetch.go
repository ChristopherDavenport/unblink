package js

import (
	"context"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// prefetchWorkers bounds how many asset bodies are fetched at once. A code-split
// SPA's modulepreload graph is hundreds of small hashed chunks over an HTTP/2
// connection, so a wide fan-out is what a real browser does; the shared per-host
// rate limiter (when configured) and the per-render download budget still apply to
// every prefetch through the counting/guarded transport, and the SSRF guard is
// unaffected.
const prefetchWorkers = 16

// prefetchEntry is one in-flight (or finished) script-body fetch. done is
// closed exactly once when body/ok are final; readers block on it.
type prefetchEntry struct {
	done chan struct{}
	body []byte
	ok   bool
}

// startPrefetch warms the bodies of the collected external classic <script src>
// tags concurrently, so the strictly document-ordered execute pass in runScripts
// waits on max(RTT) instead of sum(RTT). runScripts executes the same collected
// snapshot, so it fetches exactly the set that will run — no speculative over-fetch.
// See warmConcurrent for the settle-safety rationale.
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
	b.warmConcurrent(urls)
}

// warmJob pairs a URL with its prefetch entry so warmer goroutines never read the
// b.prefetch map. Only the on-loop synchronous portion of warmConcurrent writes
// that map, which keeps it single-writer and race-free against a second
// warmConcurrent call (startPrefetch then startModulePreload) and against the
// off-loop prefetched() readers in esbuild's OnLoad.
type warmJob struct {
	url string
	e   *prefetchEntry
}

// warmConcurrent fetches urls concurrently into the asset cache and the per-render
// prefetch map, so a later synchronous consumer — runScripts, or the module OnLoad
// on a code-split import() — hits a warm entry instead of blocking on a fresh round
// trip. It is engine plumbing, not page-observable async: consumers block on the
// entry via prefetched() (which runs before the settle poll starts), so the requests
// need no pending bracket and adding no new async primitive keeps ADR 0004's settle
// proof intact. Safe to call more than once per render; entries already warm or
// already queued are skipped. Must run on the loop goroutine (it writes b.prefetch).
func (b *bridge) warmConcurrent(urls []string) {
	if b.transport == nil || len(urls) == 0 {
		return
	}
	if b.prefetch == nil {
		b.prefetch = make(map[string]*prefetchEntry, len(urls))
	}
	var jobs []warmJob
	for _, u := range urls {
		if _, dup := b.prefetch[u]; dup {
			continue
		}
		if _, warm := b.assets.get(assetKey(u)); warm {
			continue
		}
		e := &prefetchEntry{done: make(chan struct{})}
		b.prefetch[u] = e
		jobs = append(jobs, warmJob{url: u, e: e})
	}
	if len(jobs) == 0 {
		return
	}
	queue := make(chan warmJob)
	var wg sync.WaitGroup
	workers := prefetchWorkers
	if len(jobs) < workers {
		workers = len(jobs)
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				ctx, cancel := context.WithTimeout(scriptCtx(b.ctx), b.reqTimeout)
				res, err := b.transport.Do(ctx, "GET", j.url, nil, nil)
				cancel()
				if err == nil && res != nil && res.Status < 400 {
					j.e.body = res.Body
					j.e.ok = true
					b.assets.put(assetKey(j.url), res.Body)
				}
				close(j.e.done)
			}
		}()
	}
	go func() {
		for _, j := range jobs {
			queue <- j
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
