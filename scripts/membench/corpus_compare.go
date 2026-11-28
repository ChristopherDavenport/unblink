//go:build corpus

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/christopherdavenport/unblink/corpus"
)

// The corpus comparison (`make corpus-compare`): unblink and Charlotte read the
// same captured real pages, offline, and both outputs are scored against the
// Chromium oracle saved at capture time by the corpus package's own scorer.
//
// Same input, two routes to it. Charlotte drives a real Chromium, so it is
// pointed (through a launcher wrapper that adds --proxy-server) at
// corpus.Proxy, which answers every request from the page's archive. unblink
// runs as a real MCP server in this process with the archive as its transport.
// Both go through the one replay matcher; neither can reach the network.
var (
	ccRun   = flag.Bool("corpus-compare", false, "compare tools on the real-page corpus (unblink, charlotte) and exit")
	ccDir   = flag.String("corpus-dir", "", "captured corpus directory (default $CORPUS_DIR, then <root>/testdata/corpus)")
	ccOnly  = flag.String("corpus-only", "", "comma-separated page ids or cells to run")
	ccRuns  = flag.Int("corpus-runs", 3, "runs per page per configuration; the median is reported")
	ccOut   = flag.String("corpus-out", "", "write the full per-run results as JSON here")
	ccDump  = flag.String("corpus-dump", "", "write each configuration's first-run output per page under this directory")
	ccTable = flag.String("corpus-md", "", "write the Markdown tables here as well as to stdout")
)

func init() { corpusHook = runCorpusCompare }

// compareMargin is the recall difference past which one tool is said to beat
// the other on a page. Fixed before any run.
const compareMargin = 0.05

// A configuration is one tool in one mode. "default" is what an agent gets
// from the tool's page-reading call with no arguments but the URL; "full" is
// its most complete rendition.
type ccConfig struct {
	Key  string // "unblink/default"
	Tool string
	Mode string
	Note string
}

var ccConfigs = []ccConfig{
	{"unblink/default", "unblink", "default", "`read(url)`: article mode, one call at the default token budget"},
	{"unblink/article", "unblink", "article", "`read(url)`, following `next_cursor` to the end"},
	{"unblink/full", "unblink", "full", "`read(url, mode=full)`, following `next_cursor` to the end"},
	{"charlotte/default", "charlotte", "default", "`charlotte_navigate(url)`: detail `minimal`"},
	{"charlotte/summary", "charlotte", "summary", "`charlotte_observe(detail=summary)` on the page the full run loaded (not timed)"},
	{"charlotte/full", "charlotte", "full", "`charlotte_navigate(url, detail=full)`"},
}

// ccRunRec is one run of one configuration on one page.
type ccRunRec struct {
	corpus.TextScore
	Tokens int `json:"tokens"` // o200k_base tokens of the whole payload the agent receives
	Bytes  int `json:"bytes"`
	// TokensRaw is the payload as returned, replay-artefact diagnostics
	// included (Charlotte only; see charlotteWithoutDiagnostics).
	TokensRaw     int    `json:"tokens_raw,omitempty"`
	LatencyMS     int64  `json:"latency_ms"` // the reading call(s) only; 0 when not timed
	Calls         int    `json:"calls"`
	Err           string `json:"err,omitempty"`
	Requests      int    `json:"requests"`
	Unmatched     int    `json:"unmatched"`
	Consequential int    `json:"consequential"`
	Background    int    `json:"background,omitempty"` // browser housekeeping refused by the proxy
	// MissKinds and MissSample say what the replay could not answer, by kind
	// (script, fetch/xhr, image, ...) and by example.
	MissKinds  map[string]int `json:"miss_kinds,omitempty"`
	MissSample []string       `json:"miss_sample,omitempty"`
	Excused    bool           `json:"excused,omitempty"`
	Links      int            `json:"links,omitempty"`
	Forms      int            `json:"forms,omitempty"`
	ModeUsed   string         `json:"mode_used,omitempty"`
	Discarded  bool           `json:"render_discarded,omitempty"`
}

// ccCell is a configuration's result on a page: the runs and their median.
type ccCell struct {
	Runs         []ccRunRec `json:"runs"`
	Median       ccRunRec   `json:"median"`
	RecallSpread float64    `json:"recall_spread"` // max - min over the runs
	TokenSpread  int        `json:"token_spread"`
	Failed       bool       `json:"failed"` // most runs errored or timed out
}

type ccPage struct {
	ID           string             `json:"id"`
	URL          string             `json:"url"`
	Cells        []string           `json:"cells"`
	Unusable     string             `json:"unusable,omitempty"`
	Warnings     []string           `json:"warnings,omitempty"`
	OracleSource string             `json:"oracle_source"`
	OracleTokens int                `json:"oracle_tokens"`
	OracleLinks  int                `json:"oracle_links"`
	OracleForms  int                `json:"oracle_forms"`
	Scorable     bool               `json:"scorable"`
	Results      map[string]*ccCell `json:"results"`
}

type ccReport struct {
	Date     string  `json:"date"`
	Runs     int     `json:"runs"`
	Margin   float64 `json:"margin"`
	Chromium string  `json:"chromium"`
	// CaptureChromium is the build that produced the oracle.
	CaptureChromium string            `json:"capture_chromium,omitempty"`
	Versions        map[string]string `json:"versions"`
	Tokenizer       string            `json:"tokenizer"`
	Configs         []ccConfig        `json:"configs"`
	Pages           []*ccPage         `json:"pages"`
	Missing         []string          `json:"missing,omitempty"`
}

func runCorpusCompare(cfg toolConfig, tools []string) bool {
	if !*ccRun {
		return false
	}
	// unblink runs in this process: its replay misses would log a warning each.
	// They are counted in the report, so the log is noise unless asked for.
	if !cfg.verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	want := map[string]bool{}
	for _, t := range tools {
		want[t] = true
	}
	// -tools defaults to "unblink,chrome"; the comparison is these two tools.
	if !want["unblink"] && !want["charlotte"] || (len(tools) == 2 && want["chrome"]) {
		want = map[string]bool{"unblink": true, "charlotte": true}
	}
	dir := *ccDir
	if dir == "" && os.Getenv("CORPUS_DIR") == "" {
		dir = filepath.Join(cfg.root, "testdata", "corpus")
	}
	pages, missing, err := corpus.ComparePages(dir, splitComma(*ccOnly))
	if err != nil {
		die("corpus-compare: %v (captures are local-only: run `make corpus-capture`, or pass -corpus-dir)", err)
	}
	rep := &ccReport{
		Date: time.Now().Format("2006-01-02"), Runs: *ccRuns, Margin: compareMargin,
		Versions: map[string]string{}, Tokenizer: "o200k_base", Missing: missing,
	}
	var configs []ccConfig
	for _, c := range ccConfigs {
		if want[c.Tool] {
			configs = append(configs, c)
		}
	}
	rep.Configs = configs

	var ch *charlotteRunner
	if want["charlotte"] {
		ch, err = newCharlotteRunner(cfg)
		if err != nil {
			die("corpus-compare: charlotte: %v", err)
		}
		defer ch.Close()
		rep.Chromium = chromeVersion(cfg.chromePath)
		// The oracle is what one Chromium build rendered; a different build
		// replaying the capture is a second variable in Charlotte's score.
		if cap := pages[0].Chromium; cap != "" && !strings.Contains(rep.Chromium, strings.TrimPrefix(cap, "Google Chrome for Testing ")) {
			fmt.Fprintf(os.Stderr, "corpus-compare: WARNING: Charlotte will drive %q but the corpus was captured with %q; pass -chrome (make CHROME=) to match\n", rep.Chromium, cap)
		}
		rep.CaptureChromium = pages[0].Chromium
		rep.Versions["charlotte"] = charlotteVersion
	}
	if want["unblink"] {
		rep.Versions["unblink"] = gitDescribe(cfg.root)
	}
	if *ccDump != "" {
		if err := os.MkdirAll(*ccDump, 0o755); err != nil {
			die("corpus-dump: %v", err)
		}
	}

	fmt.Fprintf(os.Stderr, "corpus-compare: %d pages x %d runs, configs: %s\n", len(pages), *ccRuns, configKeys(configs))
	start := time.Now()
	for i, p := range pages {
		pg := &ccPage{
			ID: p.ID, URL: p.URL, Cells: p.Cells, Unusable: p.Unusable, Warnings: p.Warnings,
			OracleSource: p.OracleSource, OracleTokens: p.OracleTokens, OracleLinks: p.OracleLinks,
			OracleForms: p.OracleForms, Scorable: p.Scorable(), Results: map[string]*ccCell{},
		}
		for _, c := range configs {
			pg.Results[c.Key] = &ccCell{}
		}
		for run := 0; run < *ccRuns; run++ {
			dump := func(key, text string) {
				if *ccDump == "" || run != 0 {
					return
				}
				name := p.ID + "." + strings.ReplaceAll(key, "/", "-") + ".txt"
				_ = os.WriteFile(filepath.Join(*ccDump, name), []byte(text), 0o644)
			}
			if want["unblink"] {
				for _, mode := range []string{"default", "article", "full"} {
					key := "unblink/" + mode
					rec, text := runUnblinkOnce(p, mode)
					pg.Results[key].Runs = append(pg.Results[key].Runs, rec)
					dump(key, text)
				}
			}
			if want["charlotte"] {
				rec, text := ch.run(p, "default")
				pg.Results["charlotte/default"].Runs = append(pg.Results["charlotte/default"].Runs, rec)
				dump("charlotte/default", text)
				full, fullText, sum, sumText := ch.runFull(p)
				pg.Results["charlotte/full"].Runs = append(pg.Results["charlotte/full"].Runs, full)
				pg.Results["charlotte/summary"].Runs = append(pg.Results["charlotte/summary"].Runs, sum)
				dump("charlotte/full", fullText)
				dump("charlotte/summary", sumText)
			}
		}
		for _, c := range configs {
			pg.Results[c.Key].summarise()
		}
		rep.Pages = append(rep.Pages, pg)
		fmt.Fprintf(os.Stderr, "corpus-compare: [%d/%d] %-22s %s  (%s elapsed)\n", i+1, len(pages), p.ID,
			progressLine(pg, configs), time.Since(start).Round(time.Second))
	}

	md := renderCompare(rep)
	fmt.Print(md)
	if *ccTable != "" {
		if err := os.WriteFile(*ccTable, []byte(md), 0o644); err != nil {
			die("corpus-md: %v", err)
		}
	}
	if *ccOut != "" {
		b, _ := json.MarshalIndent(rep, "", " ")
		if err := os.WriteFile(*ccOut, b, 0o644); err != nil {
			die("corpus-out: %v", err)
		}
	}
	return true
}

func runUnblinkOnce(p *corpus.ComparePage, mode string) (ccRunRec, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := corpus.RunUnblink(ctx, p, mode)
	rec := ccRunRec{
		TextScore: p.Score(r.Text), Tokens: countTokens(r.Text), Bytes: len(r.Text),
		LatencyMS: r.Dur.Milliseconds(), Calls: r.Calls, Err: r.Err,
		Requests: r.Replay.Requests, Unmatched: r.Replay.Unmatched + r.Replay.CaptureNoBody,
		Consequential: r.Replay.MissConsequential, Links: r.Links, Forms: r.Forms,
		ModeUsed: r.Mode, Discarded: r.RenderDiscarded,
	}
	rec.Excused = corpus.Excused(rec.Recall, r.Replay)
	rec.MissKinds, rec.MissSample = r.Replay.MissKinds, missSample(r.Replay)
	return rec, r.Text
}

// missSample is a few of the lost requests, for the JSON report.
func missSample(st corpus.ReplayStats) []string {
	var out []string
	for _, m := range st.Misses {
		if len(out) == 6 {
			break
		}
		u := m.URL
		if len(u) > 160 {
			u = u[:160]
		}
		out = append(out, m.Kind+" "+m.Method+" "+u)
	}
	return out
}

// summarise fills the median record and the run-to-run spread. A run that
// errored or timed out gave the agent nothing: it counts as recall 0 and 0
// tokens, and a configuration most of whose runs failed is marked Failed.
func (c *ccCell) summarise() {
	n := len(c.Runs)
	if n == 0 {
		return
	}
	failed := 0
	recalls, trecalls, precs := make([]float64, n), make([]float64, n), make([]float64, n)
	toks, bytes, lats, words, raws := make([]int, n), make([]int, n), make([]int, n), make([]int, n), make([]int, n)
	reqs, unm, cons, links, forms := make([]int, n), make([]int, n), make([]int, n), make([]int, n), make([]int, n)
	for i, r := range c.Runs {
		if r.Err != "" {
			failed++
		}
		recalls[i], trecalls[i], precs[i] = r.Recall, r.TokenRecall, r.Precision
		toks[i], bytes[i], lats[i], words[i] = r.Tokens, r.Bytes, int(r.LatencyMS), r.Words
		raws[i] = r.TokensRaw
		reqs[i], unm[i], cons[i], links[i], forms[i] = r.Requests, r.Unmatched, r.Consequential, r.Links, r.Forms
	}
	c.Failed = failed*2 > n
	m := ccRunRec{}
	m.Scored = c.Runs[0].Scored
	m.Recall, m.TokenRecall, m.Precision = medianF(recalls), medianF(trecalls), medianF(precs)
	m.Tokens, m.Bytes, m.LatencyMS, m.Words = medianI(toks), medianI(bytes), int64(medianI(lats)), medianI(words)
	m.Requests, m.Unmatched, m.Consequential = medianI(reqs), medianI(unm), medianI(cons)
	m.Links, m.Forms = medianI(links), medianI(forms)
	m.TokensRaw = medianI(raws)
	m.Calls = c.Runs[0].Calls
	m.ModeUsed = c.Runs[0].ModeUsed
	for _, r := range c.Runs {
		m.Blocked = m.Blocked || r.Blocked
		m.Discarded = m.Discarded || r.Discarded
		if r.Err != "" && m.Err == "" {
			m.Err = r.Err
		}
	}
	if !c.Failed {
		m.Err = ""
	}
	m.Excused = m.Recall < corpus.PassRecall && m.Consequential > 0
	c.Median = m
	sort.Float64s(recalls)
	sort.Ints(toks)
	c.RecallSpread = recalls[n-1] - recalls[0]
	c.TokenSpread = toks[n-1] - toks[0]
}

func medianF(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func medianI(v []int) int {
	s := append([]int(nil), v...)
	sort.Ints(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func splitComma(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func configKeys(cs []ccConfig) string {
	keys := make([]string, len(cs))
	for i, c := range cs {
		keys[i] = c.Key
	}
	return strings.Join(keys, ", ")
}

func progressLine(pg *ccPage, configs []ccConfig) string {
	var parts []string
	for _, c := range configs {
		r := pg.Results[c.Key]
		s := fmt.Sprintf("%s=%.2f/%dt", strings.Replace(strings.Replace(c.Key, "unblink", "u", 1), "charlotte", "c", 1), r.Median.Recall, r.Median.Tokens)
		if r.Failed {
			s += "!"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
