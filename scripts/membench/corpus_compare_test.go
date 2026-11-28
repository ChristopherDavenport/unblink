//go:build corpus

package main

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/corpus"
)

// TestCharlotteReadable: fidelity is scored on the decoded text of a Charlotte
// payload — in document order, identifiers and diagnostics dropped — never on
// the raw JSON, where "\n" would tokenise as a stray "n" and every key would
// split the text.
func TestCharlotteReadable(t *testing.T) {
	payload := `{"url":"https://a.test/x","title":"Page Title","timestamp":"2026-11-27T00:00:00Z",` +
		`"structure":{"landmarks":[{"id":"rgn-1","role":"navigation","label":"Main menu","bounds":{"x":1,"y":2,"w":3,"h":4}}],` +
		`"headings":[{"level":1,"text":"Hello world","id":"hdg-1"}],"full_content":"First line\nSecond \"quoted\" line\nThird"},` +
		`"interactive":[{"id":"lnk-1","type":"link","label":"Read more","href":"https://a.test/more"}],"forms":[{"id":"frm-1"}],` +
		`"errors":{"console":[{"level":"error","text":"Failed to load resource"}],"network":[]}}`
	got := charlotteReadable(payload)
	want := "Page Title\nMain menu\nHello world\nFirst line\nSecond \"quoted\" line\nThird\nRead more\n"
	if got != want {
		t.Errorf("charlotteReadable:\n got %q\nwant %q", got, want)
	}
	for _, leak := range []string{"rgn-1", "lnk-1", "navigation", "a.test", "Failed to load", "2026-11-27"} {
		if strings.Contains(got, leak) {
			t.Errorf("identifier or diagnostic %q leaked into the scored text", leak)
		}
	}
	if links, forms := charlotteCounts(payload); links != 1 || forms != 1 {
		t.Errorf("charlotteCounts = %d links, %d forms; want 1, 1", links, forms)
	}
	minimal := `{"interactive_summary":{"total":5,"by_landmark":{"main":{"link":3,"button":1},"banner":{"link":1}}}}`
	if links, _ := charlotteCounts(minimal); links != 4 {
		t.Errorf("minimal-detail link tally = %d, want 4", links)
	}
	// Token cost is counted without the diagnostics block, which under replay
	// records the replay's own missing fonts and unanswerable requests.
	cost := charlotteWithoutDiagnostics(payload)
	if strings.Contains(cost, "Failed to load resource") || !strings.Contains(cost, `"errors":{"console":[],"network":[]}`) {
		t.Errorf("diagnostics not emptied: %s", cost)
	}
	for _, keep := range []string{`"full_content"`, `"lnk-1"`, `"bounds"`, "Hello world"} {
		if !strings.Contains(cost, keep) {
			t.Errorf("the cost payload lost %s: everything but diagnostics is charged", keep)
		}
	}
	if got := charlotteWithoutDiagnostics("not json"); got != "not json" {
		t.Errorf("a non-JSON payload must pass through, got %q", got)
	}
	if got := charlotteReadable("Navigation timed out"); got != "Navigation timed out" {
		t.Errorf("a non-JSON payload must pass through, got %q", got)
	}
}

func cell(recalls []float64, tokens []int, errs []string, consequential int) *ccCell {
	c := &ccCell{}
	for i := range recalls {
		r := ccRunRec{Tokens: tokens[i], Consequential: consequential, Calls: 1}
		r.Recall, r.Scored = recalls[i], true
		if errs != nil {
			r.Err = errs[i]
		}
		c.Runs = append(c.Runs, r)
	}
	c.summarise()
	return c
}

func TestSummariseMedianSpreadAndFailure(t *testing.T) {
	c := cell([]float64{0.90, 0.50, 0.80}, []int{300, 100, 200}, nil, 0)
	if c.Median.Recall != 0.80 || c.Median.Tokens != 200 {
		t.Errorf("median = %.2f / %d, want 0.80 / 200", c.Median.Recall, c.Median.Tokens)
	}
	if d := c.RecallSpread - 0.40; d > 1e-9 || d < -1e-9 || c.TokenSpread != 200 {
		t.Errorf("spread = %.3f / %d, want 0.400 / 200", c.RecallSpread, c.TokenSpread)
	}
	if c.Failed || c.Median.Excused {
		t.Errorf("a clean cell is neither failed nor excused: %+v", c)
	}
	// One failed run of three is noise; two are a failure, and score nothing.
	if c := cell([]float64{0.9, 0, 0.9}, []int{10, 0, 10}, []string{"", "timeout", ""}, 0); c.Failed || c.Median.Recall != 0.9 {
		t.Errorf("one failed run: %+v", c)
	}
	if c := cell([]float64{0, 0, 0.9}, []int{0, 0, 10}, []string{"timeout", "timeout", ""}, 0); !c.Failed || c.Median.Recall != 0 || c.Median.Err == "" {
		t.Errorf("two failed runs: %+v", c)
	}
	if c := cell([]float64{0.4, 0.4, 0.4}, []int{1, 1, 1}, nil, 3); !c.Median.Excused {
		t.Error("a low median with consequential replay losses is excused")
	}
	if c := cell([]float64{0.9, 0.9, 0.9}, []int{1, 1, 1}, nil, 3); c.Median.Excused {
		t.Error("a passing median is never excused")
	}
	if corpus.PassRecall != 0.85 {
		t.Errorf("the pass line moved to %.2f; the report's \"pass ≥0.85\" column and docs name it", corpus.PassRecall)
	}
}

func TestDuelMarginAndSoundness(t *testing.T) {
	page := func(id string, u, c float64, uLoss, cLoss int) *ccPage {
		return &ccPage{ID: id, Scorable: true, Results: map[string]*ccCell{
			"unblink/full":   cell([]float64{u}, []int{100}, nil, uLoss),
			"charlotte/full": cell([]float64{c}, []int{300}, nil, cLoss),
			// Charlotte's minimal level is always below the pass line; that must
			// not, on its own, make a page unsound.
			"charlotte/default": cell([]float64{0.01}, []int{50}, nil, cLoss),
		}}
	}
	pages := []*ccPage{
		page("u-wins", 0.90, 0.80, 0, 0),
		page("tie-inside-margin", 0.90, 0.86, 0, 0),
		page("c-wins", 0.50, 0.95, 0, 0),
		page("u-excused", 0.30, 0.95, 4, 0),         // unblink's low score follows a replay loss
		page("c-lossy-but-passing", 0.9, 0.9, 0, 2), // a loss that did not cost the score
	}
	all := duel(pages, "unblink/full", "charlotte/full", compareMargin, false)
	if all.N != 5 || all.AW != 1 || all.Tie != 2 || all.BW != 2 {
		t.Errorf("all pages: n=%d %d/%d/%d, want 5 1/2/2", all.N, all.AW, all.Tie, all.BW)
	}
	snd := duel(pages, "unblink/full", "charlotte/full", compareMargin, true)
	if snd.N != 4 || snd.AW != 1 || snd.Tie != 2 || snd.BW != 1 || snd.Unsound != 1 {
		t.Errorf("sound pages: n=%d %d/%d/%d unsound=%d, want 4 1/2/1 unsound=1", snd.N, snd.AW, snd.Tie, snd.BW, snd.Unsound)
	}
	if !soundFor(pages[4], "unblink/default", "charlotte/default") {
		t.Error("soundness is judged on a tool's full configuration, not on its minimal one")
	}
	if soundFor(pages[3], "unblink/default") {
		t.Error("a tool whose full configuration is excused is unsound in every configuration")
	}
	a := aggregateConfig(pages, "charlotte/full")
	if a.Pages != 5 || a.Pass != 4 || a.Lossy != 1 || a.TokMedian != 300 {
		t.Errorf("aggregate = %+v", a)
	}
}
