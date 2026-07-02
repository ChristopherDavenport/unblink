package js

// In-package tests for the cross-render program/selector caches (unexported).

import (
	"fmt"
	"sync"
	"testing"
)

func TestCompileCachedReusesProgram(t *testing.T) {
	p1, err := compileCached("cache-test.js", "1 + 1")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	p2, err := compileCached("cache-test.js", "1 + 1")
	if err != nil {
		t.Fatalf("compile (cached): %v", err)
	}
	if p1 != p2 {
		t.Error("identical (name, src) did not return the cached Program")
	}
	p3, err := compileCached("cache-test-other.js", "1 + 1")
	if err != nil {
		t.Fatalf("compile (other name): %v", err)
	}
	if p3 == p1 {
		t.Error("different name must not share a Program (stack-trace names differ)")
	}
}

func TestCompileCachedCachesErrors(t *testing.T) {
	const bad = "function ((("
	if _, err := compileCached("cache-bad.js", bad); err == nil {
		t.Fatal("expected compile error")
	}
	if _, err := compileCached("cache-bad.js", bad); err == nil {
		t.Fatal("expected cached compile error")
	}
}

// classicProgram caches the whole fallback outcome: a classic script goja can't
// parse (dynamic import) is lowered via esbuild once, then served from cache.
func TestClassicProgramCachesLoweredOutcome(t *testing.T) {
	const src = "import('./chunk.js').then(function (m) { globalThis.__x = m; });"
	p1, err := classicProgram("lower-test.js", src)
	if err != nil {
		t.Fatalf("lowering fallback failed: %v", err)
	}
	p2, err := classicProgram("lower-test.js", src)
	if err != nil {
		t.Fatalf("cached lowering failed: %v", err)
	}
	if p1 != p2 {
		t.Error("lowered outcome was not cached")
	}
}

func TestCompileSelectorCachesOutcomes(t *testing.T) {
	if _, err := compileSelector(".item a"); err != nil {
		t.Fatalf("valid selector: %v", err)
	}
	if _, err := compileSelector(".item a"); err != nil {
		t.Fatalf("valid selector (cached): %v", err)
	}
	if _, err := compileSelector("[[["); err == nil {
		t.Fatal("expected selector compile error")
	}
	if _, err := compileSelector("[[["); err == nil {
		t.Fatal("expected cached selector compile error")
	}
}

// The generation swap must stay correct under overflow and concurrent use.
func TestSelectorCacheOverflowAndConcurrency(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < selCacheMax; i++ {
				sel, err := compileSelector(fmt.Sprintf("#id-%d-%d", g, i))
				if err != nil || sel == nil {
					t.Errorf("selector %d-%d: %v", g, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	// An early key still resolves correctly after the swaps (recompiled or cached).
	if _, err := compileSelector("#id-0-0"); err != nil {
		t.Fatalf("post-overflow lookup: %v", err)
	}
}
