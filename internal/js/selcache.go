package js

import (
	"sync"

	"github.com/andybalholm/cascadia"
)

// selEntry caches a compile outcome — including failures, so a selector that
// doesn't parse (e.g. a bad wait_for gate re-checked every settle tick) isn't
// recompiled forever.
type selEntry struct {
	sel cascadia.Selector
	err error
}

// selCacheMax bounds each generation: page JS controls the key space, so a
// hostile page minting unique selectors must not grow the cache unboundedly.
const selCacheMax = 1024

// selCache shares compiled selectors across renders and runtimes. A compiled
// cascadia.Selector is a stateless matcher — this package already shares them
// concurrently at package level (selScript, selModuleScript, ...). Eviction is
// a two-generation swap: when the current generation fills it becomes the
// previous one, and hits there re-promote into the current generation.
var selCache = struct {
	sync.Mutex
	cur, prev map[string]selEntry
}{cur: make(map[string]selEntry)}

// compileSelector is the cached front door for every page-JS selector compile
// (querySelector/querySelectorAll/matches/closest and the settle wait gate).
func compileSelector(selector string) (cascadia.Selector, error) {
	selCache.Lock()
	if e, ok := selCache.cur[selector]; ok {
		selCache.Unlock()
		return e.sel, e.err
	}
	if e, ok := selCache.prev[selector]; ok {
		selCache.cur[selector] = e
		selCache.Unlock()
		return e.sel, e.err
	}
	selCache.Unlock()

	sel, err := cascadia.Compile(selector)

	selCache.Lock()
	if len(selCache.cur) >= selCacheMax {
		selCache.prev, selCache.cur = selCache.cur, make(map[string]selEntry, selCacheMax/4)
	}
	selCache.cur[selector] = selEntry{sel: sel, err: err}
	selCache.Unlock()
	return sel, err
}
