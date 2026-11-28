//go:build corpus

package corpus

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cellRow aggregates the pages that claim one feature-matrix cell.
type cellRow struct {
	Cell  string `json:"cell"`
	Pages int    `json:"pages"`
	Pass  int    `json:"pass"`
	Gap   int    `json:"a"`
	ByDes int    `json:"b"`
	Ceil  int    `json:"c"`
	Harn  int    `json:"d"`
	Uncl  int    `json:"unclassified"`
	// Scored excludes bucket (d): a page whose comparison is unsound says nothing
	// about the engine. PassRate = Pass / Scored; the means are over Scored too.
	Scored      int     `json:"scored"`
	PassRate    float64 `json:"pass_rate"`
	MeanRecall  float64 `json:"mean_recall"`
	MeanStatic  float64 `json:"mean_static_recall"`
	MeanArticle float64 `json:"mean_article_recall"`
	MeanJunk    float64 `json:"mean_junk"`
	// MedianLinkRate is the median extracted/visible link ratio over LinkN pages
	// — those where Chromium saw at least one visible link, so a ratio exists.
	// The median, not the mean: one page with a near-zero denominator (115 links
	// extracted where Chromium showed 2) would otherwise set a small cell's value.
	MedianLinkRate float64 `json:"median_link_ratio"`
	LinkN          int     `json:"link_ratio_pages"`
	linkRatios     []float64
	// PassWithLoss counts passes reached although replay lost consequential
	// requests, and RenderGain the scored pages where running JavaScript raised
	// recall by more than 0.05. A cell whose passes are all "with loss" and whose
	// RenderGain is 0 has measured the served HTML, not the engine.
	PassWithLoss int `json:"pass_with_loss"`
	RenderGain   int `json:"render_gain"`
}

// manifestIssue is one claimed cell the capture does not bear out.
type manifestIssue struct {
	ID       string `json:"id"`
	Cell     string `json:"cell"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence,omitempty"`
}

// recapture is one page whose capture should be redone, and why.
type recapture struct {
	ID      string   `json:"id"`
	Reasons []string `json:"reasons"`
}

type report struct {
	Generated  string      `json:"generated"`
	Threshold  float64     `json:"pass_recall"`
	Overall    cellRow     `json:"overall"`
	Cells      []cellRow   `json:"cells"`
	Replay     replayStats `json:"replay"`
	NotCapture []string    `json:"not_captured,omitempty"`
	// ManifestChecks counts cell-label verdicts; ManifestIssues lists the labels
	// the captures contradict or show no evidence for. Nothing rewrites the
	// manifest: these are for a human to act on.
	ManifestChecks map[string]int  `json:"manifest_checks,omitempty"`
	ManifestIssues []manifestIssue `json:"manifest_issues,omitempty"`
	// Recapture lists pages whose capture is unusable or doubtful.
	Recapture []recapture  `json:"recapture,omitempty"`
	Pages     []pageResult `json:"pages"`
}

func (c *cellRow) add(r pageResult) {
	c.Pages++
	switch r.Bucket {
	case bucketPass:
		c.Pass++
	case bucketGap:
		c.Gap++
	case bucketByDesign:
		c.ByDes++
	case bucketCeiling:
		c.Ceil++
	case bucketHarness:
		c.Harn++
		return
	default:
		c.Uncl++
	}
	c.Scored++
	c.MeanRecall += r.Recall
	c.MeanStatic += r.StaticRecall
	c.MeanArticle += r.ArticleRecall
	c.MeanJunk += 1 - r.Precision
	if r.LinkRatioOK {
		c.linkRatios = append(c.linkRatios, r.LinkRatio)
		c.LinkN++
	}
	if r.PassWithLoss {
		c.PassWithLoss++
	}
	if r.Recall-r.StaticRecall > renderGainMin {
		c.RenderGain++
	}
}

// renderGainMin is how much recall rendering must add over render=false for a
// page to count as evidence that its JavaScript did something.
const renderGainMin = 0.05

func (c *cellRow) finish() {
	c.MedianLinkRate = median(c.linkRatios)
	if c.Scored == 0 {
		return
	}
	n := float64(c.Scored)
	c.PassRate = float64(c.Pass) / n
	c.MeanRecall /= n
	c.MeanStatic /= n
	c.MeanArticle /= n
	c.MeanJunk /= n
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func aggregate(results []pageResult, missing []string) report {
	rep := report{
		Generated: time.Now().UTC().Format(time.RFC3339), Threshold: passRecall,
		Overall: cellRow{Cell: "overall"}, NotCapture: missing, Pages: results,
	}
	cells := map[string]*cellRow{}
	for _, r := range results {
		rep.Overall.add(r)
		for _, c := range r.Cells {
			if cells[c] == nil {
				cells[c] = &cellRow{Cell: c}
			}
			cells[c].add(r)
		}
		rep.Replay.Requests += r.Replay.Requests
		rep.Replay.Exact += r.Replay.Exact
		rep.Replay.CacheBust += r.Replay.CacheBust
		rep.Replay.PathOnly += r.Replay.PathOnly
		rep.Replay.CaptureFailed += r.Replay.CaptureFailed
		rep.Replay.CaptureNoBody += r.Replay.CaptureNoBody
		rep.Replay.Unmatched += r.Replay.Unmatched
		rep.Replay.NonHTTP += r.Replay.NonHTTP
		rep.Replay.MissConsequential += r.Replay.MissConsequential
		rep.Replay.MissHarmless += r.Replay.MissHarmless
		rep.Replay.MissFirstParty += r.Replay.MissFirstParty
		addCounts(&rep.Replay.MissKinds, r.Replay.MissKinds)
		addCounts(&rep.Replay.MissMethods, r.Replay.MissMethods)
		addCounts(&rep.Replay.MissNear, r.Replay.MissNear)
		for _, c := range r.CellChecks {
			bump(&rep.ManifestChecks, c.Verdict)
			if c.Verdict == verdictContradicted || c.Verdict == verdictNoEvidence {
				rep.ManifestIssues = append(rep.ManifestIssues, manifestIssue{ID: r.ID, Cell: c.Cell, Verdict: c.Verdict, Evidence: c.Evidence})
			}
		}
		if rc := recaptureReasons(r); len(rc) > 0 {
			rep.Recapture = append(rep.Recapture, recapture{ID: r.ID, Reasons: rc})
		}
	}
	rep.Overall.finish()
	for _, c := range cells {
		c.finish()
		rep.Cells = append(rep.Cells, *c)
	}
	sort.Slice(rep.Cells, func(i, j int) bool { return rep.Cells[i].Cell < rep.Cells[j].Cell })
	return rep
}

// recaptureReasons says why a page's capture should be redone: its oracle is
// unusable or doubtful. A page that is merely missing requests the engine asked
// for is not listed — recapturing would not make Chromium fetch them.
func recaptureReasons(r pageResult) []string {
	var out []string
	if r.Bucket == bucketHarness && (strings.HasPrefix(r.Reason, "oracle ") || strings.HasPrefix(r.Reason, "unreadable capture")) {
		out = append(out, r.Reason)
	}
	return append(out, r.OracleWarnings...)
}

func writeScorecard(w io.Writer, rep report) {
	fmt.Fprintf(w, "\n== unblink real-page corpus: replay vs Chromium oracle (pass = main-content recall >= %.2f) ==\n", rep.Threshold)
	fmt.Fprintln(w, "buckets: pass | a engine gap (candidate) | b non-goal | c syntax ceiling | d harness-unsound (excluded from rates) | ? unclassified")
	const hdr = "%-30s %5s %5s %3s %3s %3s %3s %3s %6s %7s %7s %7s %6s %6s %5s %5s\n"
	const row = "%-30s %5d %5d %3d %3d %3d %3d %3d %6s %7s %7s %7s %6s %6s %5d %5d\n"
	fmt.Fprintf(w, hdr, "cell", "pages", "pass", "a", "b", "c", "d", "?", "rate", "recall", "static", "article", "junk", "links", "lossy", "jsgain")
	line := func(c cellRow) {
		f := func(v float64) string {
			if c.Scored == 0 {
				return "-"
			}
			return fmt.Sprintf("%.3f", v)
		}
		links := "-"
		if c.LinkN > 0 {
			links = fmt.Sprintf("%.3f", c.MedianLinkRate)
		}
		fmt.Fprintf(w, row, c.Cell, c.Pages, c.Pass, c.Gap, c.ByDes, c.Ceil, c.Harn, c.Uncl,
			f(c.PassRate), f(c.MeanRecall), f(c.MeanStatic), f(c.MeanArticle), f(c.MeanJunk), links, c.PassWithLoss, c.RenderGain)
	}
	group := ""
	for _, c := range rep.Cells {
		if g, _, _ := strings.Cut(c.Cell, ":"); g != group {
			group = g
			fmt.Fprintln(w)
		}
		line(c)
	}
	fmt.Fprintln(w)
	line(rep.Overall)

	writeLatency(w, rep.Pages)

	rs := rep.Replay
	fmt.Fprintln(w, "lossy = passes reached although replay lost consequential requests; jsgain = scored pages where rendering raised recall by > 0.05.")
	fmt.Fprintln(w, "A cell with lossy passes and jsgain 0 measured the served HTML, not the engine.")
	fmt.Fprintf(w, "\nreplay: %d requests — %d exact, %d cache-bust fallback, %d path fallback, %d failed at capture too, %d with no body at capture, %d unmatched; %d non-http URL(s) sent to the HTTP transport\n",
		rs.Requests, rs.Exact, rs.CacheBust, rs.PathOnly, rs.CaptureFailed, rs.CaptureNoBody, rs.Unmatched, rs.NonHTTP)
	if lost := rs.MissConsequential + rs.MissHarmless; lost > 0 {
		fmt.Fprintf(w, "lost requests: %d — %d consequential, %d harmless; %d first-party, %d third-party\n",
			lost, rs.MissConsequential, rs.MissHarmless, rs.MissFirstParty, lost-rs.MissFirstParty)
		fmt.Fprintf(w, "  by kind:   %s\n  by method: %s\n  by nearest archive entry: %s\n",
			countsString(rs.MissKinds), countsString(rs.MissMethods), countsString(rs.MissNear))
	}

	// Every page that did not pass, worst first, with the evidence for its bucket.
	var bad []pageResult
	for _, r := range rep.Pages {
		if r.Bucket != bucketPass {
			bad = append(bad, r)
		}
	}
	sort.SliceStable(bad, func(i, j int) bool {
		if (bad[i].Bucket == bucketHarness) != (bad[j].Bucket == bucketHarness) {
			return bad[j].Bucket == bucketHarness // engine-attributable first
		}
		return bad[i].Recall < bad[j].Recall
	})
	if len(bad) > 0 {
		fmt.Fprintln(w, "\npages below threshold (engine-attributable first, worst first):")
	}
	for _, r := range bad {
		b := r.Bucket
		if b == bucketUnclassified {
			b = "?"
		}
		if r.Scored {
			fmt.Fprintf(w, "  [%s] %-26s recall=%.3f static=%.3f article=%.3f  %s\n", b, r.ID, r.Recall, r.StaticRecall, r.ArticleRecall, r.Reason)
		} else {
			fmt.Fprintf(w, "  [%s] %-26s unscored  %s\n", b, r.ID, r.Reason)
		}
		var notes []string
		if r.RenderBudgetHit {
			notes = append(notes, "render budget hit")
		}
		if r.NetPending > 0 {
			notes = append(notes, fmt.Sprintf("%d requests pending at snapshot", r.NetPending))
		}
		if n := r.Replay.MissConsequential + r.Replay.MissHarmless; n > 0 {
			notes = append(notes, fmt.Sprintf("%d lost request(s): %s", n, countsString(r.Replay.MissKinds)))
		}
		if r.Replay.NonHTTP > 0 {
			notes = append(notes, fmt.Sprintf("%d non-http URL(s) sent to the HTTP transport", r.Replay.NonHTTP))
		}
		if r.Scored && r.StaticRecall >= passRecall && r.Recall < passRecall {
			notes = append(notes, "render=false passes: rendering LOST content")
		}
		if len(notes) > 0 {
			fmt.Fprintf(w, "        %s\n", strings.Join(notes, "; "))
		}
	}

	var lossy []pageResult
	for _, r := range rep.Pages {
		if r.PassWithLoss {
			lossy = append(lossy, r)
		}
	}
	if len(lossy) > 0 {
		fmt.Fprintln(w, "\npasses with consequential replay losses (a pass with gain 0 says nothing about the page's JavaScript):")
		for _, r := range lossy {
			fmt.Fprintf(w, "  %-26s recall=%.3f static=%.3f gain=%+.3f  lost %d (%s)\n",
				r.ID, r.Recall, r.StaticRecall, r.Recall-r.StaticRecall, r.Replay.MissConsequential, countsString(r.Replay.MissKinds))
		}
	}

	first := true
	for _, r := range rep.Pages {
		sp := r.Spread
		if sp == nil || (!sp.Unstable && sp.RequestsMin == sp.RequestsMax) {
			continue
		}
		if first {
			fmt.Fprintln(w, "\nresults that moved between repeats of the same capture on the same build (the worst run is the one scored):")
			first = false
		}
		tag := "requests only"
		if sp.Unstable {
			tag = "UNSTABLE"
		}
		fmt.Fprintf(w, "  %-26s %-13s recall %.3f..%.3f  requests %d..%d  buckets %s  (%d runs)\n",
			r.ID, tag, sp.RecallMin, sp.RecallMax, sp.RequestsMin, sp.RequestsMax, countsString(sp.Buckets), sp.Runs)
	}

	if len(rep.Recapture) > 0 {
		fmt.Fprintln(w, "\ncaptures to redo or inspect (oracle unusable or doubtful):")
		for _, rc := range rep.Recapture {
			fmt.Fprintf(w, "  %-26s %s\n", rc.ID, strings.Join(rc.Reasons, "; "))
		}
	}

	if len(rep.ManifestChecks) > 0 {
		fmt.Fprintf(w, "\nmanifest labels vs. captures: %s\n", countsString(rep.ManifestChecks))
		for _, verdict := range []string{verdictContradicted, verdictNoEvidence} {
			for _, mi := range rep.ManifestIssues {
				if mi.Verdict == verdict {
					fmt.Fprintf(w, "  %-12s %-24s %-26s %s\n", mi.Verdict, mi.ID, mi.Cell, mi.Evidence)
				}
			}
		}
		for _, r := range rep.Pages {
			if len(r.Unclaimed) > 0 {
				fmt.Fprintf(w, "  %-12s %-24s %s\n", "unclaimed", r.ID, strings.Join(r.Unclaimed, ", "))
			}
		}
	}
	if len(rep.NotCapture) > 0 {
		fmt.Fprintf(w, "\nnot captured (%d): %s\n", len(rep.NotCapture), strings.Join(rep.NotCapture, ", "))
	}
	fmt.Fprintln(w)
}

// writeJSON writes the full per-page report next to the package (gitignored),
// or to CORPUS_OUT — so a one-page run need not overwrite a full run's report.
func writeJSON(rep report) (string, error) {
	path := os.Getenv("CORPUS_OUT")
	if path == "" {
		path = filepath.Join(repoRoot(), "corpus", "last-run.json")
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return path, err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}

// writeLatency reports where the first full read of each page spent its time.
// Replay answers from disk, so this is the engine's own latency: script
// execution and the wait for the page to go quiet.
func writeLatency(w io.Writer, pages []pageResult) {
	var read, exec, settle []float64
	var sumRead, sumExec, sumSettle int64
	slow := 0
	for _, p := range pages {
		if p.ReadMS == 0 && p.PipelineErr != "" {
			continue
		}
		read, exec, settle = append(read, float64(p.ReadMS)), append(exec, float64(p.ExecMS)), append(settle, float64(p.SettleMS))
		sumRead, sumExec, sumSettle = sumRead+p.ReadMS, sumExec+p.ExecMS, sumSettle+p.SettleMS
		if p.ReadMS > 1000 {
			slow++
		}
	}
	if len(read) == 0 {
		return
	}
	fmt.Fprintf(w, "\nlatency (first full read, ms): median %.0f, p90 %.0f, total %d — script execution median %.0f / total %d, settle median %.0f / total %d; %d page(s) over 1s\n",
		median(read), percentile(read, 0.9), sumRead, median(exec), sumExec, median(settle), sumSettle, slow)
}

// percentile returns the p-quantile of v by nearest rank (0 for no values).
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(p * float64(len(s)))
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}
