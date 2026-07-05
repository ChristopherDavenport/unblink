//go:build wpt

package wpt

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestWPT is the `make wpt` entry point: it runs the in-scope WPT corpus through
// the JS engine and prints a bucketed conformance scorecard to stderr + writes
// last-run.json. It is an on-demand analysis tool, not a gate — it fails only on
// an infrastructure error (missing corpus, enumeration failure), never on the
// conformance rate itself (see docs/decisions/0016-wpt-conformance.md).
func TestWPT(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	start := time.Now()
	results, nonWindow, version, err := run(ctx)
	if err != nil {
		t.Fatalf("wpt run: %v", err)
	}
	rep := aggregate(results, nonWindow, version)

	writeScorecard(os.Stderr, rep)
	path, werr := writeJSON(rep)
	if werr != nil {
		t.Errorf("write %s: %v", path, werr)
	} else {
		t.Logf("wrote %s (%d fixable-gap entries)", path, len(rep.Failures))
	}
	t.Logf("ran %d test variants in %s (wpt %s)", len(results), time.Since(start).Round(time.Millisecond), version)
}
