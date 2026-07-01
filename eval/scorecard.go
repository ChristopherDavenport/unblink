//go:build eval

package eval

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
)

// scoredScorer is one scorer's outcome within a case.
type scoredScorer struct {
	Name   string
	Axis   Axis
	Score  float64
	Detail string
}

// caseResult is the scored outcome of one case.
type caseResult struct {
	Name     string
	Floor    float64
	MustPass bool
	Scorers  []scoredScorer
	Mean     float64
	Pass     bool
	RunErr   error // non-nil if the world/host could not be built or run
}

// scoreCase runs a case against a fresh world and scores every scorer over the
// resulting transcript. A run error yields a zero-score, non-passing result so
// the gate treats infrastructure failures as regressions.
func scoreCase(ctx context.Context, c Case) caseResult {
	cr := caseResult{Name: c.Name, Floor: c.Floor, MustPass: c.MustPass}
	tr, cleanup, err := run(ctx, c)
	if err != nil {
		cr.RunErr = err
		return cr
	}
	defer cleanup()
	var sum float64
	for _, sc := range c.Scorers {
		score, detail := sc.Fn(tr)
		cr.Scorers = append(cr.Scorers, scoredScorer{Name: sc.Name, Axis: sc.Axis, Score: score, Detail: detail})
		sum += score
	}
	if len(c.Scorers) > 0 {
		cr.Mean = sum / float64(len(c.Scorers))
	} else {
		cr.Mean = 1 // a case with no scorers (shouldn't happen) is vacuously fine
	}
	cr.Pass = cr.Mean >= c.Floor
	return cr
}

// axisOrder fixes the rollup ordering so the report is deterministic.
var axisOrder = []Axis{
	AxisRegistration, AxisJunk, AxisRecall, AxisToken, AxisPagination,
	AxisStructure, AxisFind, AxisSession, AxisSite, AxisRender, AxisError,
	AxisContent, AxisAuth, AxisDiscovery,
}

// writeReport prints the per-case table, per-axis rollup, and overall line.
func writeReport(w io.Writer, results []caseResult) {
	fmt.Fprintln(w, "\n=== unblink MCP eval ===")

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CASE\tSCORE\tFLOOR\tRESULT")
	for _, cr := range results {
		result := "PASS"
		switch {
		case cr.RunErr != nil:
			result = "ERROR"
		case !cr.Pass:
			result = "FAIL"
		}
		fmt.Fprintf(tw, "%s\t%.2f\t%.2f\t%s\n", cr.Name, cr.Mean, cr.Floor, result)
	}
	tw.Flush()

	// Per-case scorer detail (only for non-perfect or errored cases, to keep the
	// happy path quiet but always explain a regression).
	for _, cr := range results {
		if cr.RunErr != nil {
			fmt.Fprintf(w, "\n%s: RUN ERROR: %v\n", cr.Name, cr.RunErr)
			continue
		}
		if cr.Mean >= 1.0 {
			continue
		}
		fmt.Fprintf(w, "\n%s (%.2f):\n", cr.Name, cr.Mean)
		dtw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, sc := range cr.Scorers {
			fmt.Fprintf(dtw, "  %s\t[%s]\t%.2f\t%s\n", sc.Name, sc.Axis, sc.Score, sc.Detail)
		}
		dtw.Flush()
	}

	// Per-axis rollup.
	sums := map[Axis]float64{}
	counts := map[Axis]int{}
	for _, cr := range results {
		for _, sc := range cr.Scorers {
			sums[sc.Axis] += sc.Score
			counts[sc.Axis]++
		}
	}
	fmt.Fprintln(w, "\nAXIS ROLLUP")
	atw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, ax := range axisOrder {
		if counts[ax] == 0 {
			continue
		}
		fmt.Fprintf(atw, "  %s\t%.2f\t(n=%d)\n", ax, sums[ax]/float64(counts[ax]), counts[ax])
	}
	atw.Flush()
}

// gate computes the overall score, pass-rate, and the list of hard failures
// (run errors, must-pass cases below floor, or overall below overallFloor). An
// empty failures slice means the gate passes.
func gate(results []caseResult, overallFloor float64) (overall float64, passed int, failures []string) {
	var sum float64
	for _, cr := range results {
		sum += cr.Mean
		if cr.Pass && cr.RunErr == nil {
			passed++
		}
		switch {
		case cr.RunErr != nil:
			failures = append(failures, fmt.Sprintf("%s: run error: %v", cr.Name, cr.RunErr))
		case cr.MustPass && !cr.Pass:
			failures = append(failures, fmt.Sprintf("%s: %.2f < floor %.2f", cr.Name, cr.Mean, cr.Floor))
		}
	}
	if len(results) > 0 {
		overall = sum / float64(len(results))
	}
	if overall < overallFloor {
		failures = append(failures, fmt.Sprintf("overall %.2f < %.2f", overall, overallFloor))
	}
	return overall, passed, failures
}
