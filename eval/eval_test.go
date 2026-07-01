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

	all := cases()
	results := make([]caseResult, 0, len(all))
	for _, c := range all {
		results = append(results, scoreCase(ctx, c))
	}

	// The scorecard goes to stderr; the test framework owns stdout-adjacent output.
	writeReport(os.Stderr, results)

	overall, passed, failures := gate(results, overallFloor)
	fmt.Fprintf(os.Stderr, "\nOVERALL  %.2f   pass-rate %d/%d\n", overall, passed, len(results))

	if len(failures) > 0 {
		t.Fatalf("eval gate failed:\n  %s", strings.Join(failures, "\n  "))
	}
}
