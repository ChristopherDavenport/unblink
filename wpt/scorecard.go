//go:build wpt

package wpt

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
)

// dirAgg is the per-directory rollup. Counts are at subtest granularity for tests
// that ran; a test that never ran contributes 1 to its test-level bucket column.
type dirAgg struct {
	Dir       string  `json:"dir"`
	Bucket    string  `json:"scope"`
	Tests     int     `json:"tests"`
	Subtests  int     `json:"subtests"`
	Pass      int     `json:"pass"`
	Fix       int     `json:"fix_inscope"`
	ByDesign  int     `json:"out_of_scope"`
	Ceiling   int     `json:"engine_ceiling"`
	Harness   int     `json:"harness_limited"`
	Timeout   int     `json:"timeout"`
	NonWindow int     `json:"skipped_nonwindow"`
	Conform   float64 `json:"conformance"` // pass/(pass+fix); -1 when denominator is 0
}

func (a *dirAgg) add(b bucket, n int) {
	switch b {
	case bucketPass:
		a.Pass += n
	case bucketFix:
		a.Fix += n
	case bucketByDesign:
		a.ByDesign += n
	case bucketCeiling:
		a.Ceiling += n
	case bucketHarness:
		a.Harness += n
	case bucketTimeout:
		a.Timeout += n
	}
}

func (a *dirAgg) finish() {
	if den := a.Pass + a.Fix; den > 0 {
		a.Conform = float64(a.Pass) / float64(den)
	} else {
		a.Conform = -1
	}
}

// report is the full machine-readable run, serialized to last-run.json.
type report struct {
	WPTVersion string    `json:"wpt_version"`
	Dirs       []*dirAgg `json:"dirs"`
	Totals     dirAgg    `json:"totals"`
	Failures   []failure `json:"failures"` // capped list of (a) gaps — the to-do list
	Timeouts   []string  `json:"timeouts"` // tests that produced no results within budget
	Ceilings   []string  `json:"ceilings"` // tests that hit the goja parse/compile ceiling
}

type failure struct {
	Dir     string `json:"dir"`
	Path    string `json:"path"`
	Variant string `json:"variant,omitempty"`
	Subtest string `json:"subtest"`
	Message string `json:"message,omitempty"`
}

const maxFailures = 4000 // cap the itemized (a) list so the JSON stays reviewable

// aggregate rolls per-test results into per-dir + total aggregates and the itemized
// fixable-gap list.
func aggregate(results []testResult, nonWindow map[string]int, version string) report {
	byDir := map[string]*dirAgg{}
	get := func(dir, scope string) *dirAgg {
		a := byDir[dir]
		if a == nil {
			a = &dirAgg{Dir: dir, Bucket: scope}
			byDir[dir] = a
		}
		return a
	}
	var rep report
	rep.WPTVersion = version

	for _, tr := range results {
		a := get(tr.Dir, "")
		a.Tests++
		if !tr.Ran {
			a.add(tr.Bucket, 1)
			switch tr.Bucket {
			case bucketTimeout:
				if len(rep.Timeouts) < maxFailures {
					rep.Timeouts = append(rep.Timeouts, tr.RelPath+tr.Variant)
				}
			case bucketCeiling:
				if len(rep.Ceilings) < maxFailures {
					rep.Ceilings = append(rep.Ceilings, tr.RelPath+tr.Variant+" — "+tr.Msg)
				}
			}
			continue
		}
		for _, s := range tr.Subs {
			a.Subtests++
			a.add(s.Bucket, 1)
			if s.Bucket == bucketFix && len(rep.Failures) < maxFailures {
				rep.Failures = append(rep.Failures, failure{
					Dir: tr.Dir, Path: tr.RelPath, Variant: tr.Variant,
					Subtest: s.Name, Message: s.Msg,
				})
			}
		}
	}
	for dir, n := range nonWindow {
		get(dir, "").NonWindow += n
	}

	for _, a := range byDir {
		a.finish()
		rep.Totals.Pass += a.Pass
		rep.Totals.Fix += a.Fix
		rep.Totals.ByDesign += a.ByDesign
		rep.Totals.Ceiling += a.Ceiling
		rep.Totals.Harness += a.Harness
		rep.Totals.Timeout += a.Timeout
		rep.Totals.NonWindow += a.NonWindow
		rep.Totals.Tests += a.Tests
		rep.Totals.Subtests += a.Subtests
		rep.Dirs = append(rep.Dirs, a)
	}
	rep.Totals.Dir = "TOTAL"
	rep.Totals.finish()
	sort.Slice(rep.Dirs, func(i, j int) bool { return rep.Dirs[i].Dir < rep.Dirs[j].Dir })
	return rep
}

// writeScorecard prints the per-directory table + totals to w (stderr).
func writeScorecard(w io.Writer, rep report) {
	fmt.Fprintf(w, "\n=== unblink WPT conformance (wpt %s) ===\n", rep.WPTVersion)
	fmt.Fprintln(w, "conformance = PASS / (PASS + FIX); FIX=(a) in-scope gaps, OOS=(b) by-design,")
	fmt.Fprintln(w, "CEIL=(c) goja ceiling, HARN=(d) runner limit, TMO=timeout, NW=skipped worker-only")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DIR\tTESTS\tSUB\tPASS\tFIX\tOOS\tCEIL\tHARN\tTMO\tNW\tCONFORM")
	for _, a := range rep.Dirs {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			a.Dir, a.Tests, a.Subtests, a.Pass, a.Fix, a.ByDesign, a.Ceiling, a.Harness, a.Timeout, a.NonWindow, conformStr(a.Conform))
	}
	t := rep.Totals
	fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
		t.Dir, t.Tests, t.Subtests, t.Pass, t.Fix, t.ByDesign, t.Ceiling, t.Harness, t.Timeout, t.NonWindow, conformStr(t.Conform))
	tw.Flush()
}

func conformStr(c float64) string {
	if c < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", c)
}

// writeJSON serializes the full report to path (WPT_JSON or wpt/last-run.json).
func writeJSON(rep report) (string, error) {
	path := os.Getenv("WPT_JSON")
	if path == "" {
		// The test's working dir is the wpt/ package dir, so this lands at
		// wpt/last-run.json from the repo root.
		path = "last-run.json"
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return path, err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}
