//go:build eval

package eval

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// overallFloor is the minimum mean score across all cases for the gate to pass.
const overallFloor = 0.9

// TestEval runs every seed case against the real MCP server in-process, prints a
// scorecard to stderr (stdout is reserved for MCP JSON-RPC), and fails the gate
// nonzero when any must-pass case is below its floor, any case errors, or the
// overall score drops below overallFloor.
func TestEval(t *testing.T) {
	ctx := context.Background()

	all := append(cases(), builtCases()...)
	// EVAL_ONLY narrows the run to cases whose name contains the given text
	// (comma-separated alternatives) — for iterating on one fixture or repeating
	// a few cases to look for flakiness. The gate then covers only those cases.
	if only := os.Getenv("EVAL_ONLY"); only != "" {
		var kept []Case
		for _, c := range all {
			for _, want := range strings.Split(only, ",") {
				if want = strings.TrimSpace(want); want != "" && strings.Contains(c.Name, want) {
					kept = append(kept, c)
					break
				}
			}
		}
		if len(kept) == 0 {
			t.Fatalf("EVAL_ONLY=%q matches no case", only)
		}
		all = kept
	}
	results := make([]caseResult, 0, len(all))
	for _, c := range all {
		results = append(results, scoreCase(ctx, c))
	}

	// The scorecard goes to stderr; the test framework owns stdout-adjacent output.
	writeReport(os.Stderr, results)

	overall, passed, failures := gate(results, overallFloor)
	gated := 0
	for _, r := range results {
		if r.KnownGap == "" {
			gated++
		}
	}
	fmt.Fprintf(os.Stderr, "\nOVERALL  %.2f   pass-rate %d/%d", overall, passed, gated)
	if gaps := len(results) - gated; gaps > 0 {
		fmt.Fprintf(os.Stderr, "   (+%d known gap(s), see above)", gaps)
	}
	fmt.Fprintln(os.Stderr)

	if len(failures) > 0 {
		t.Fatalf("eval gate failed:\n  %s", strings.Join(failures, "\n  "))
	}
}
