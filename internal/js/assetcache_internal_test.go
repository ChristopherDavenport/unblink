package js

// In-package tests for the asset cache's byte-based eviction (unexported).

import (
	"fmt"
	"testing"
	"time"
)

// TestAssetCacheByteEvictionNotCount proves eviction is by summed bytes, not a
// fixed entry count: far more than the old 256-entry cap survive when their bytes
// stay under the budget, and a single generation over the byte cap triggers the
// two-generation swap.
func TestAssetCacheByteEvictionNotCount(t *testing.T) {
	a := newAssetCache(time.Minute)

	// 1000 tiny entries (well past the retired 256-entry cap) fit under the byte
	// budget, so none are evicted.
	const n = 1000
	for i := 0; i < n; i++ {
		a.put(fmt.Sprintf("k%d", i), []byte("x"))
	}
	if len(a.cur) != n {
		t.Fatalf("byte-based cache evicted under the byte cap: len(cur)=%d, want %d", len(a.cur), n)
	}

	// Two half-cap bodies overflow one generation and force a swap.
	big := make([]byte, assetCacheMaxBytes/2+1)
	b := newAssetCache(time.Minute)
	b.put("a", big)
	if len(b.prev) != 0 {
		t.Fatalf("no swap expected after one entry, prev=%d", len(b.prev))
	}
	b.put("c", big) // pushes curBytes over the cap → swap
	if len(b.cur) != 1 {
		t.Fatalf("after swap cur should hold only the newest entry, got %d", len(b.cur))
	}
	if _, ok := b.prev["a"]; !ok {
		t.Fatal("evicted-generation entry should be retained in prev")
	}
	if _, ok := b.get("a"); !ok {
		t.Error("prev-generation entry should still be retrievable within TTL")
	}
	if _, ok := b.get("c"); !ok {
		t.Error("current-generation entry should be retrievable")
	}
}
