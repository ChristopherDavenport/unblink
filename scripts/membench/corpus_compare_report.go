//go:build corpus

package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

func gitDescribe(root string) string {
	out, err := exec.Command("git", "-C", root, "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// ccAgg is one configuration aggregated over a set of pages.
type ccAgg struct {
	Pages, Failed, Pass, Excused, Lossy, Blocked int
	Recall, TokenRecall, Precision, PerK         float64 // means
	TokMedian, TokMean, LatMedian                int
	TokRawMedian                                 int // payload as returned, replay diagnostics included
	TokTotal                                     int
	Spread                                       float64 // mean run-to-run recall spread
	SpreadMax                                    float64
}

func aggregateConfig(pages []*ccPage, key string) ccAgg {
	var a ccAgg
	var toks, lats, raws []int
	for _, p := range pages {
		c := p.Results[key]
		if c == nil || len(c.Runs) == 0 {
			continue
		}
		m := c.Median
		a.Pages++
		if c.Failed {
			a.Failed++
		}
		if m.Recall >= 0.85 {
			a.Pass++
		}
		if m.Excused {
			a.Excused++
		}
		if m.Consequential > 0 {
			a.Lossy++
		}
		if m.Blocked {
			a.Blocked++
		}
		a.Recall += m.Recall
		a.TokenRecall += m.TokenRecall
		a.Precision += m.Precision
		if m.Tokens > 0 {
			a.PerK += m.Recall / (float64(m.Tokens) / 1000)
		}
		a.TokTotal += m.Tokens
		toks = append(toks, m.Tokens)
		if m.TokensRaw > 0 {
			raws = append(raws, m.TokensRaw)
		} else {
			raws = append(raws, m.Tokens)
		}
		if m.LatencyMS > 0 {
			lats = append(lats, int(m.LatencyMS))
		}
		a.Spread += c.RecallSpread
		if c.RecallSpread > a.SpreadMax {
			a.SpreadMax = c.RecallSpread
		}
	}
	if a.Pages > 0 {
		n := float64(a.Pages)
		a.Recall, a.TokenRecall, a.Precision, a.PerK, a.Spread = a.Recall/n, a.TokenRecall/n, a.Precision/n, a.PerK/n, a.Spread/n
		a.TokMean = a.TokTotal / a.Pages
	}
	a.TokMedian, a.LatMedian, a.TokRawMedian = medianI(toks), medianI(lats), medianI(raws)
	return a
}

// soundFor reports whether the page is replay-sound for every tool behind the
// listed configurations. The harness's rule — a score below the pass line is
// about the replay, not the tool, when the replay lost a script, document, data
// or style request the archive could not answer — is applied per tool, on that
// tool's *full* configuration: every configuration of a tool loads the page
// the same way, and only the full rendition's recall can say whether a loss
// mattered (Charlotte's minimal level is below the pass line by design).
func soundFor(p *ccPage, keys ...string) bool {
	for _, k := range keys {
		tool, _, _ := strings.Cut(k, "/")
		if c := p.Results[tool+"/full"]; c != nil && len(c.Runs) > 0 && c.Median.Excused {
			return false
		}
	}
	return true
}

type ccDuel struct {
	A, B            string
	N, AW, Tie, BW  int
	AWins, BWins    []ccDiff
	MeanA, MeanB    float64
	TokA, TokB      int // medians over the duel's pages
	Unsound, NoData int
}

type ccDiff struct {
	ID         string
	RA, RB     float64
	TA, TB     int
	Unmatched  [2]int
	FailedSide string
}

func duel(pages []*ccPage, a, b string, margin float64, soundOnly bool) ccDuel {
	d := ccDuel{A: a, B: b}
	var ta, tb []int
	for _, p := range pages {
		ca, cb := p.Results[a], p.Results[b]
		if ca == nil || cb == nil || len(ca.Runs) == 0 || len(cb.Runs) == 0 {
			d.NoData++
			continue
		}
		if soundOnly && !soundFor(p, a, b) {
			d.Unsound++
			continue
		}
		d.N++
		ra, rb := ca.Median.Recall, cb.Median.Recall
		d.MeanA += ra
		d.MeanB += rb
		ta, tb = append(ta, ca.Median.Tokens), append(tb, cb.Median.Tokens)
		diff := ccDiff{ID: p.ID, RA: ra, RB: rb, TA: ca.Median.Tokens, TB: cb.Median.Tokens,
			Unmatched: [2]int{ca.Median.Consequential, cb.Median.Consequential}}
		switch {
		case ra-rb > margin:
			d.AW++
			d.AWins = append(d.AWins, diff)
		case rb-ra > margin:
			d.BW++
			d.BWins = append(d.BWins, diff)
		default:
			d.Tie++
		}
	}
	if d.N > 0 {
		d.MeanA /= float64(d.N)
		d.MeanB /= float64(d.N)
	}
	d.TokA, d.TokB = medianI(ta), medianI(tb)
	sort.Slice(d.AWins, func(i, j int) bool { return d.AWins[i].RA-d.AWins[i].RB > d.AWins[j].RA-d.AWins[j].RB })
	sort.Slice(d.BWins, func(i, j int) bool { return d.BWins[i].RB-d.BWins[i].RA > d.BWins[j].RB-d.BWins[j].RA })
	return d
}

func renderCompare(rep *ccReport) string {
	var b strings.Builder
	has := map[string]bool{}
	for _, c := range rep.Configs {
		has[c.Key] = true
	}
	var scorable, unusable []*ccPage
	for _, p := range rep.Pages {
		if p.Scorable {
			scorable = append(scorable, p)
		} else {
			unusable = append(unusable, p)
		}
	}
	keys := make([]string, len(rep.Configs))
	for i, c := range rep.Configs {
		keys[i] = c.Key
	}
	var sound []*ccPage
	for _, p := range scorable {
		if soundFor(p, keys...) {
			sound = append(sound, p)
		}
	}

	fmt.Fprintf(&b, "## Real-page corpus: unblink vs Charlotte (%s)\n\n", rep.Date)
	var vs []string
	for _, t := range []string{"unblink", "charlotte"} {
		if v := rep.Versions[t]; v != "" {
			vs = append(vs, t+" "+v)
		}
	}
	fmt.Fprintf(&b, "%s; %s; %d captured pages, %d with a usable oracle, %d replay-sound for both tools; %d runs per page per configuration, medians reported; tokens are %s.\n\n",
		strings.Join(vs, ", "), orDash(rep.Chromium), len(rep.Pages), len(scorable), len(sound), rep.Runs, rep.Tokenizer)
	fmt.Fprintf(&b, "Configurations:\n\n")
	for _, c := range rep.Configs {
		fmt.Fprintf(&b, "- `%s` — %s\n", c.Key, c.Note)
	}
	b.WriteString("\n")

	table := func(title string, pages []*ccPage) {
		fmt.Fprintf(&b, "### %s (%d pages)\n\n", title, len(pages))
		b.WriteString("| configuration | recall | token recall | precision | pass ≥0.85 | tokens (median) | tokens (mean) | tokens as returned (median) | recall per 1k tokens | latency ms (median) | failed | lossy replay | run spread (mean / max) |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, c := range rep.Configs {
			a := aggregateConfig(pages, c.Key)
			lat := "—"
			if a.LatMedian > 0 {
				lat = comma(a.LatMedian)
			}
			fmt.Fprintf(&b, "| `%s` | %.3f | %.3f | %.3f | %d | %s | %s | %s | %.3f | %s | %d | %d | %.3f / %.3f |\n",
				c.Key, a.Recall, a.TokenRecall, a.Precision, a.Pass, comma(a.TokMedian), comma(a.TokMean), comma(a.TokRawMedian), a.PerK, lat, a.Failed, a.Lossy, a.Spread, a.SpreadMax)
		}
		b.WriteString("\n")
	}
	table("All pages with a usable oracle", scorable)
	table("Replay-sound for both tools", sound)

	if has["unblink/full"] && has["charlotte/full"] {
		fmt.Fprintf(&b, "### Head to head (recall margin %.2f)\n\n", rep.Margin)
		b.WriteString("| A | B | pages | A wins | tie | B wins | mean recall A | mean recall B | median tokens A | median tokens B | set |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
		pairs := [][2]string{{"unblink/full", "charlotte/full"}, {"unblink/article", "charlotte/full"}, {"unblink/default", "charlotte/full"}, {"unblink/default", "charlotte/default"}}
		for _, pr := range pairs {
			for _, snd := range []bool{true, false} {
				d := duel(scorable, pr[0], pr[1], rep.Margin, snd)
				set := "all usable-oracle"
				if snd {
					set = "replay-sound for both"
				}
				fmt.Fprintf(&b, "| `%s` | `%s` | %d | %d | %d | %d | %.3f | %.3f | %s | %s | %s |\n",
					d.A, d.B, d.N, d.AW, d.Tie, d.BW, d.MeanA, d.MeanB, comma(d.TokA), comma(d.TokB), set)
			}
		}
		b.WriteString("\n")

		d := duel(scorable, "unblink/full", "charlotte/full", rep.Margin, false)
		list := func(title string, ds []ccDiff) {
			fmt.Fprintf(&b, "**%s** (%d)\n\n", title, len(ds))
			if len(ds) == 0 {
				b.WriteString("none\n\n")
				return
			}
			b.WriteString("| page | unblink/full recall | charlotte/full recall | unblink tokens | charlotte tokens | consequential replay losses (u / c) |\n|---|---|---|---|---|---|\n")
			for _, x := range ds {
				fmt.Fprintf(&b, "| %s | %.3f | %.3f | %s | %s | %d / %d |\n", x.ID, x.RA, x.RB, comma(x.TA), comma(x.TB), x.Unmatched[0], x.Unmatched[1])
			}
			b.WriteString("\n")
		}
		list("Pages where unblink/full beats charlotte/full", d.AWins)
		list("Pages where charlotte/full beats unblink/full", d.BWins)
	}

	// Per-cell view.
	cells := map[string][]*ccPage{}
	for _, p := range scorable {
		for _, c := range p.Cells {
			cells[c] = append(cells[c], p)
		}
	}
	names := make([]string, 0, len(cells))
	for c := range cells {
		names = append(names, c)
	}
	sort.Strings(names)
	cellKeys := []string{}
	for _, k := range []string{"unblink/default", "unblink/article", "unblink/full", "charlotte/full"} {
		if has[k] {
			cellKeys = append(cellKeys, k)
		}
	}
	b.WriteString("### Per feature-matrix cell (all usable-oracle pages; mean recall / median tokens)\n\n")
	b.WriteString("| cell | pages |")
	for _, k := range cellKeys {
		fmt.Fprintf(&b, " `%s` |", k)
	}
	b.WriteString("\n|---|---|")
	for range cellKeys {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, n := range names {
		fmt.Fprintf(&b, "| %s | %d |", n, len(cells[n]))
		for _, k := range cellKeys {
			a := aggregateConfig(cells[n], k)
			fmt.Fprintf(&b, " %.3f / %s |", a.Recall, comma(a.TokMedian))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Link and form counts against the oracle.
	b.WriteString("### Link and form counts against the oracle\n\n")
	b.WriteString("| configuration | pages with visible links | median links ÷ oracle visible links | pages with forms in the oracle | pages where the tool reports ≥1 form |\n|---|---|---|---|---|\n")
	for _, k := range []string{"unblink/full", "charlotte/full"} {
		if !has[k] {
			continue
		}
		var ratios []float64
		formPages, formHit := 0, 0
		for _, p := range scorable {
			c := p.Results[k]
			if c == nil || len(c.Runs) == 0 {
				continue
			}
			if p.OracleLinks > 0 {
				ratios = append(ratios, float64(c.Median.Links)/float64(p.OracleLinks))
			}
			if p.OracleForms > 0 {
				formPages++
				if c.Median.Forms > 0 {
					formHit++
				}
			}
		}
		fmt.Fprintf(&b, "| `%s` | %d | %.2f | %d | %d |\n", k, len(ratios), medianF(ratios), formPages, formHit)
	}
	b.WriteString("\n")

	// Failures and exclusions.
	b.WriteString("### Failures, blocks and exclusions\n\n")
	any := false
	for _, p := range rep.Pages {
		for _, c := range rep.Configs {
			r := p.Results[c.Key]
			if r == nil || len(r.Runs) == 0 {
				continue
			}
			if r.Failed {
				any = true
				fmt.Fprintf(&b, "- %s `%s`: failed — %s\n", p.ID, c.Key, oneLine(r.Median.Err, 160))
			} else if r.Median.Blocked {
				any = true
				fmt.Fprintf(&b, "- %s `%s`: output reads like a block or challenge page\n", p.ID, c.Key)
			}
		}
	}
	for _, p := range unusable {
		any = true
		why := p.Unusable
		if why == "" {
			why = fmt.Sprintf("oracle too thin to score (%d tokens)", p.OracleTokens)
		}
		fmt.Fprintf(&b, "- %s: not scored for either tool — %s\n", p.ID, why)
	}
	for _, p := range scorable {
		var ex []string
		for _, k := range []string{"unblink/full", "charlotte/full"} {
			if r := p.Results[k]; r != nil && len(r.Runs) > 0 && r.Median.Excused {
				ex = append(ex, fmt.Sprintf("`%s` scored %.3f after losing %d", k, r.Median.Recall, r.Median.Consequential))
			}
		}
		if len(ex) > 0 {
			any = true
			fmt.Fprintf(&b, "- %s: not replay-sound — %s consequential request(s) the archive could not answer\n", p.ID, strings.Join(ex, ", "))
		}
	}
	if !any {
		b.WriteString("none\n")
	}
	if len(rep.Missing) > 0 {
		fmt.Fprintf(&b, "\nNot captured: %s\n", strings.Join(rep.Missing, ", "))
	}
	b.WriteString("\n")
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "no Chromium"
	}
	return s
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
