package browser_test

import (
	"runtime"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// The auto value for --js-concurrency scales with cores but stays inside the
// [4, 16] band: the floor preserves the historical default on small machines,
// the ceiling bounds worst-case transient render heap.
func TestDefaultJSConcurrencyBounds(t *testing.T) {
	got := browser.DefaultJSConcurrency()
	if got < 4 || got > 16 {
		t.Fatalf("DefaultJSConcurrency() = %d, want within [4, 16]", got)
	}
	if procs := runtime.GOMAXPROCS(0); procs >= 4 && procs <= 16 && got != procs {
		t.Fatalf("DefaultJSConcurrency() = %d, want GOMAXPROCS %d (in band)", got, procs)
	}
}
