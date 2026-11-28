//go:build corpus

package corpus

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestCorpus is the `make corpus` entry point: it replays every captured page
// offline and prints the per-cell scorecard to stderr + writes last-run.json.
// It is a measurement, not a gate — it fails only on an infrastructure error and
// skips when nothing has been captured (see docs/decisions/0022-real-page-differential-corpus.md).
func TestCorpus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	// Replay misses make the fetch layer log a warning each; they are counted in
	// the scorecard, so the log is noise unless asked for.
	if os.Getenv("CORPUS_VERBOSE") == "" {
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		defer slog.SetDefault(prev)
	}

	start := time.Now()
	results, missing, err := run(ctx)
	if errors.Is(err, errNoCaptures) {
		t.Skipf("%v — captures are local-only and never committed; run `make corpus-capture` (needs network + Chromium) first", err)
	}
	if err != nil {
		t.Fatalf("corpus run: %v", err)
	}
	rep := aggregate(results, missing)
	writeScorecard(os.Stderr, rep)
	path, werr := writeJSON(rep)
	if werr != nil {
		t.Errorf("write %s: %v", path, werr)
	} else {
		t.Logf("wrote %s", path)
	}
	t.Logf("replayed %d pages in %s (%d manifest pages not captured)", len(results), time.Since(start).Round(time.Millisecond), len(missing))
}

// TestManifest keeps the checked-in manifest loadable without any captures.
func TestManifest(t *testing.T) {
	pages, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("manifest has no pages")
	}
}
