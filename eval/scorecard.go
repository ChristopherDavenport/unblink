//go:build eval

package eval

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"
)

// scoredScorer is one scorer's outcome within a case.
type scoredScorer struct {
	Name   string
	Axis   Axis
	Score  float64
	Detail string
}

// stepTiming is one tool call's wall-clock time, for the informational
// latency report (never scored or gated).
type stepTiming struct {
	Case string
	Tool string
	Dur  time.Duration
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

	Dur      time.Duration // total tool-call time across the case's steps
	StepDurs []stepTiming
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
	for i, st := range tr.Steps {
		cr.Dur += st.Dur
		cr.StepDurs = append(cr.StepDurs, stepTiming{Case: c.Name, Tool: fmt.Sprintf("%s#%d", st.Tool, i+1), Dur: st.Dur})
	}
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
	AxisContent, AxisAuth, AxisDiscovery, AxisSafety,
}

// writeReport prints the per-case table, per-axis rollup, and overall line.
func writeReport(w io.Writer, results []caseResult) {
	fmt.Fprintln(w, "\n=== unblink MCP eval ===")

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CASE\tSCORE\tFLOOR\tRESULT\tDUR")
	for _, cr := range results {
		result := "PASS"
		switch {
		case cr.RunErr != nil:
			result = "ERROR"
		case !cr.Pass:
			result = "FAIL"
		}
		fmt.Fprintf(tw, "%s\t%.2f\t%.2f\t%s\t%s\n", cr.Name, cr.Mean, cr.Floor, result, cr.Dur.Round(time.Millisecond))
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

	// Slowest tool calls across all cases — a coarse latency trend on every run.
	// Informational only; timing never gates the eval.
	var steps []stepTiming
	for _, cr := range results {
		steps = append(steps, cr.StepDurs...)
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Dur > steps[j].Dur })
	if len(steps) > 10 {
		steps = steps[:10]
	}
	if len(steps) > 0 {
		fmt.Fprintln(w, "\nSLOWEST STEPS (informational)")
		stw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, st := range steps {
			fmt.Fprintf(stw, "  %s\t%s\t%s\n", st.Case, st.Tool, st.Dur.Round(time.Millisecond))
		}
		stw.Flush()
	}
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
