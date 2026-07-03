package js

import (
	"crypto/sha256"
	"sync"

	"github.com/dop251/goja"
)

// The program cache shares compiled goja.Programs across renders and runtimes:
// a Program is immutable and documented safe to run in multiple runtimes
// concurrently, so repeat renders of the same site skip goja's parse+compile
// of its bundles entirely. Keys are content-only per compile pathway: the same
// bytes compile to the same Program regardless of which URL, page position, or
// query-busted variant delivered them (a CDN bundle re-served under a new URL
// must not re-pay a multi-MB parse). The Program embeds the *first-seen* name
// in stack traces/errors; identical bytes behave identically, so the name is
// diagnostic-only — and external scripts now compile under their absolute URL,
// which reads better in a trace than the old positional script-N.js anyway.
// Compile *outcomes* are cached — including failures and the esbuild
// dynamic-import-lowering fallback — so a broken or lowered script doesn't
// re-pay parse/Transform on every render.
//
// Bounded by entry count and summed source bytes per generation, evicted by
// two-generation swap (hits in the previous generation re-promote).

type progKey struct {
	kind string // compile pathway ("classic", "module.js", "dynimport.js"), never identity
	hash [32]byte
}

type progEntry struct {
	prog *goja.Program
	err  error
	size int64
}

const (
	progCacheMaxEntries = 256
	progCacheMaxBytes   = 16 << 20
)

var progCache = struct {
	sync.Mutex
	cur, prev map[progKey]progEntry
	curBytes  int64
}{cur: make(map[progKey]progEntry)}

func progCacheGet(key progKey) (progEntry, bool) {
	progCache.Lock()
	defer progCache.Unlock()
	if e, ok := progCache.cur[key]; ok {
		return e, true
	}
	if e, ok := progCache.prev[key]; ok {
		progCache.cur[key] = e
		progCache.curBytes += e.size
		return e, true
	}
	return progEntry{}, false
}

func progCachePut(key progKey, e progEntry) {
	progCache.Lock()
	defer progCache.Unlock()
	if len(progCache.cur) >= progCacheMaxEntries || progCache.curBytes >= progCacheMaxBytes {
		progCache.prev, progCache.cur = progCache.cur, make(map[progKey]progEntry)
		progCache.curBytes = 0
	}
	progCache.cur[key] = e
	progCache.curBytes += e.size
}

// compileCached compiles src under name with cross-render caching. Callers pass
// a constant pathway name ("module.js", "dynimport.js"), which doubles as the
// key's kind — the cache is content-only within each pathway. It caches errors
// too: identical source yields an identical compile outcome.
func compileCached(name, src string) (*goja.Program, error) {
	key := progKey{kind: name, hash: sha256.Sum256([]byte(src))}
	if e, ok := progCacheGet(key); ok {
		return e.prog, e.err
	}
	prog, err := goja.Compile(name, src, false)
	progCachePut(key, progEntry{prog: prog, err: err, size: int64(len(src))})
	return prog, err
}
