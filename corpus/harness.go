//go:build corpus

package corpus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/fetch"
)

// defaultDir is where `make corpus-capture` writes; override with CORPUS_DIR.
const defaultDir = "testdata/corpus"

// errNoCaptures tells the test to skip: nothing has been captured yet.
var errNoCaptures = errors.New("no captures")

// pageResult is one replayed page's scores and evidence.
type pageResult struct {
	ID    string   `json:"id"`
	URL   string   `json:"url"`
	Cells []string `json:"cells"`

	Bucket string `json:"bucket"`
	Reason string `json:"reason,omitempty"`

	// Recall is the headline: shingle recall of the oracle's main-content text in
	// the rendered mode=full Markdown. The others locate a loss: ArticleRecall
	// (default mode=article — did reduction keep it), StaticRecall (render=false —
	// what the JS engine contributed), TokenRecall (order-free upper bound).
	Recall        float64 `json:"recall"`
	TokenRecall   float64 `json:"token_recall"`
	ArticleRecall float64 `json:"article_recall"`
	StaticRecall  float64 `json:"static_recall"`
	// Precision is the share of the article-mode output the browser also showed
	// anywhere on the page; 1-Precision is junk.
	Precision float64 `json:"precision"`

	OracleSource string `json:"oracle_source"` // "main", "article" or "body"
	OracleTokens int    `json:"oracle_tokens"`
	OutputTokens int    `json:"output_tokens"`

	// Scored is false when nothing was scored (unusable or too-thin oracle,
	// unreadable capture): every score field above is then meaningless zero.
	Scored bool `json:"scored"`
	// PassWithLoss marks a pass reached although replay lost consequential
	// requests. The output matched Chromium's, but if StaticRecall equals Recall
	// it matched because the served HTML already had the content — such a pass is
	// no evidence that the page's JavaScript ran.
	PassWithLoss bool `json:"pass_with_loss,omitempty"`
	// OracleWarnings are reasons to read the score with care (see checkOracle).
	OracleWarnings []string `json:"oracle_warnings,omitempty"`
	// CellChecks is the manifest verifier's verdict on each claimed cell, and
	// Unclaimed the detected stacks no claimed cell accounts for.
	CellChecks []cellCheck `json:"cell_checks,omitempty"`
	Detected   []string    `json:"detected,omitempty"`
	Unclaimed  []string    `json:"unclaimed,omitempty"`

	Links       int `json:"links"`
	OracleLinks int `json:"oracle_links"`
	// LinkRatio / FormRatio are extracted/oracle counts; the *OK flag is false
	// when the oracle count is zero and no ratio exists.
	LinkRatio    float64 `json:"link_ratio"`
	LinkRatioOK  bool    `json:"link_ratio_ok"`
	Forms        int     `json:"forms"`
	OracleForms  int     `json:"oracle_forms"`
	FormRatio    float64 `json:"form_ratio"`
	FormRatioOK  bool    `json:"form_ratio_ok"`
	Controls     int     `json:"controls"`
	OracleCtrls  int     `json:"oracle_controls"`
	ArticleFellB bool    `json:"article_fallback,omitempty"`

	Framework       string   `json:"framework,omitempty"`
	JSErrors        []string `json:"js_errors,omitempty"`
	ConsoleErrors   []string `json:"console_errors,omitempty"`
	RenderBudgetHit bool     `json:"render_budget_hit,omitempty"`
	DOMBusy         bool     `json:"dom_busy,omitempty"`
	NetPending      int      `json:"net_pending,omitempty"`
	NetFailed       int      `json:"net_failed,omitempty"`
	NetDenied       int      `json:"net_denied,omitempty"`

	Replay      replayStats `json:"replay"`
	Spread      *runSpread  `json:"spread,omitempty"` // set under CORPUS_REPEAT
	PipelineErr string      `json:"pipeline_error,omitempty"`
	Chromium    string      `json:"chromium,omitempty"`
	CapturedAt  string      `json:"captured_at,omitempty"`
	DurMS       int64       `json:"dur_ms"`
}

// run replays every captured manifest page. It returns the results, the ids of
// manifest pages with no capture, and errNoCaptures when there is nothing to do.
func run(ctx context.Context) ([]pageResult, []string, error) {
	pages, err := loadManifest()
	if err != nil {
		return nil, nil, err
	}
	root := os.Getenv("CORPUS_DIR")
	if root == "" {
		root = filepath.Join(repoRoot(), defaultDir)
	}
	only := splitList(os.Getenv("CORPUS_ONLY"))

	var results []pageResult
	var missing []string
	for _, p := range pages {
		if len(only) > 0 && !selected(p, only) {
			continue
		}
		dir := filepath.Join(root, p.ID)
		if _, err := os.Stat(filepath.Join(dir, "oracle.json")); err != nil {
			missing = append(missing, p.ID)
			continue
		}
		if ctx.Err() != nil {
			return results, missing, ctx.Err()
		}
		results = append(results, runRepeated(ctx, p, dir, repeats()))
	}
	if len(results) == 0 {
		return nil, missing, fmt.Errorf("%w under %s", errNoCaptures, root)
	}
	return results, missing, nil
}

// repeats is how many times each page is replayed (CORPUS_REPEAT, default 1).
func repeats() int {
	if n, err := strconv.Atoi(os.Getenv("CORPUS_REPEAT")); err == nil && n > 1 && n <= 50 {
		return n
	}
	return 1
}

// unstableRecall is the recall spread across repeats past which a page's score
// is not a property of the engine build but of the run.
const unstableRecall = 0.02

// runRepeated replays a page n times and reports the WORST run, with the spread
// across runs. The capture and the engine build are the same for every repeat,
// so a spread is not something the page or the harness did: a page that passes
// on some runs and not others has not passed.
func runRepeated(ctx context.Context, p manifestPage, dir string, n int) pageResult {
	worst := runPage(ctx, p, dir)
	if n <= 1 {
		return worst
	}
	sp := &runSpread{Runs: 1, RecallMin: worst.Recall, RecallMax: worst.Recall,
		RequestsMin: worst.Replay.Requests, RequestsMax: worst.Replay.Requests, Buckets: map[string]int{worst.Bucket: 1}}
	for i := 1; i < n && ctx.Err() == nil; i++ {
		r := runPage(ctx, p, dir)
		sp.Runs++
		sp.Buckets[r.Bucket]++
		sp.RecallMin, sp.RecallMax = min(sp.RecallMin, r.Recall), max(sp.RecallMax, r.Recall)
		sp.RequestsMin, sp.RequestsMax = min(sp.RequestsMin, r.Replay.Requests), max(sp.RequestsMax, r.Replay.Requests)
		if r.Recall < worst.Recall {
			worst = r
		}
	}
	sp.Unstable = len(sp.Buckets) > 1 || sp.RecallMax-sp.RecallMin > unstableRecall
	worst.Spread = sp
	return worst
}

// runSpread is how much a page's result moved across CORPUS_REPEAT runs.
type runSpread struct {
	Runs        int            `json:"runs"`
	Unstable    bool           `json:"unstable"`
	RecallMin   float64        `json:"recall_min"`
	RecallMax   float64        `json:"recall_max"`
	RequestsMin int            `json:"requests_min"`
	RequestsMax int            `json:"requests_max"`
	Buckets     map[string]int `json:"buckets"`
}

// selected reports whether p matches a CORPUS_ONLY term: an id, a cell, or a
// cell group prefix such as "hydration".
func selected(p manifestPage, only []string) bool {
	for _, o := range only {
		if o == p.ID {
			return true
		}
		for _, c := range p.Cells {
			if c == o || strings.HasPrefix(c, o+":") {
				return true
			}
		}
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// jsBudget is the per-render wall-clock budget: the production default unless
// CORPUS_JS_TIMEOUT overrides it (to tell "too slow" from "cannot").
func jsBudget() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("CORPUS_JS_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return browser.DefaultJSTimeout
}

// newBrowser builds a production-configured Browser whose every request — page
// fetches and page-JS subrequests alike — is answered by rt. Retries are off so
// a replay miss is counted once, and site hints are off because their
// robots.txt/llms.txt probes are requests Chromium never makes.
func newBrowser(rt *replay, js bool) (*browser.Browser, error) {
	opts := []browser.Option{
		browser.WithFetchOptions(fetch.WithSharedTransport(rt)),
		browser.WithJSRoundTripper(rt),
		browser.WithRetries(0),
		browser.WithSiteHints(false),
		browser.WithJSPrewarm(0),
	}
	if js {
		opts = append(opts, browser.WithJS(jsBudget()))
	}
	return browser.New(opts...)
}

func runPage(ctx context.Context, p manifestPage, dir string) (res pageResult) {
	start := time.Now()
	res = pageResult{ID: p.ID, URL: p.URL, Cells: p.Cells}
	defer func() { res.DurMS = time.Since(start).Milliseconds() }()

	harnessFail := func(err error) pageResult {
		res.Bucket, res.Reason = bucketHarness, "unreadable capture: "+err.Error()
		return res
	}
	o, err := loadOracle(dir)
	if err != nil {
		return harnessFail(err)
	}
	a, err := loadArchive(dir)
	if err != nil {
		return harnessFail(err)
	}
	res.Chromium, res.CapturedAt = o.Chromium, o.CapturedAt
	unusable, warnings := checkOracle(o, a)
	res.OracleWarnings = warnings
	ev := gatherEvidence(dir, a, o)
	res.Detected, res.Unclaimed = ev.detected(), unclaimedStacks(p.Cells, ev)
	dbg := newPageDebug(p.ID)

	// The oracle text is the page's main landmark when Chromium found a
	// substantial one, else everything visible.
	want, source, wantText := tokenize(o.MainText), o.MainSelector, o.MainText
	if len(want) < minOracleTokens {
		want, source, wantText = tokenize(o.BodyText), "body", o.BodyText
	}
	bodyToks := tokenize(o.BodyText)
	// The pipeline still runs against an oracle that cannot be scored — its
	// diagnostics and replay statistics are real — but no score is computed.
	res.Scored = unusable == "" && len(want) >= minOracleTokens
	dbg.text("oracle.txt", wantText)
	res.OracleSource, res.OracleTokens = source, len(want)
	res.OracleLinks, res.OracleForms, res.OracleCtrls = o.LinksVisible, o.Forms, o.Controls

	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Rendered pass: one session so the page is fetched and rendered once and
	// every tool reads that same page, exactly as an agent's session would.
	rt := newReplay(dir, a)
	b, err := newBrowser(rt, true)
	if err != nil {
		return harnessFail(err)
	}
	const sid = "corpus"
	full, rr, err := readAll(pctx, b, browser.Request{SessionID: sid, URL: p.URL, Render: true}, "full")
	if err != nil {
		res.PipelineErr = err.Error()
	} else {
		cur := browser.Request{SessionID: sid, UseCurrent: true}
		got := tokenize(stripMarkdown(full))
		res.OutputTokens = len(got)
		if res.Scored {
			res.Recall, res.TokenRecall = shingleRecall(want, got), tokenRecall(want, got)
		}
		dbg.text("full.md", full)
		dbg.text("missing.txt", strings.Join(missingShingles(want, got, 2000), "\n"))
		res.Framework, res.JSErrors = rr.Framework, rr.JSErrors
		res.RenderBudgetHit, res.DOMBusy = rr.RenderBudgetHit, rr.DOMBusy
		res.NetPending, res.NetFailed, res.NetDenied = rr.NetPending, rr.NetFailed, rr.NetDenied

		if art, ar, err := readAll(pctx, b, cur, "article"); err == nil {
			at := tokenize(stripMarkdown(art))
			if res.Scored {
				res.ArticleRecall, res.Precision = shingleRecall(want, at), tokenPrecision(at, bodyToks)
			}
			res.ArticleFellB = ar.ArticleFallback
			dbg.text("article.md", art)
		}
		if links, err := b.Links(pctx, cur, "", false); err == nil {
			res.Links = len(links)
		}
		if forms, err := b.Forms(pctx, cur); err == nil {
			res.Forms = len(forms)
		}
		if ctrls, err := b.Controls(pctx, cur); err == nil {
			res.Controls = len(ctrls)
		}
		if msgs, _, err := b.Console(pctx, cur); err == nil {
			maxErrs, maxLen := 10, 300
			if dbg.on {
				maxErrs, maxLen = 200, 2000
			}
			for _, m := range msgs {
				if m.Level == "error" && len(res.ConsoleErrors) < maxErrs {
					res.ConsoleErrors = append(res.ConsoleErrors, head(m.Text, maxLen))
				}
			}
		}
		if res.Scored {
			res.LinkRatio, res.LinkRatioOK = ratio(res.Links, o.LinksVisible)
			res.FormRatio, res.FormRatioOK = ratio(res.Forms, o.Forms)
		}
	}
	res.Replay = rt.snapshot()
	b.Close()

	// Static pass (render=false) on a separate browser and replay, so its
	// requests never blur the rendered pass's replay statistics.
	if sb, err := newBrowser(newReplay(dir, a), false); err == nil {
		if md, _, err := readAll(pctx, sb, browser.Request{URL: p.URL}, "full"); err == nil {
			if res.Scored {
				res.StaticRecall = shingleRecall(want, tokenize(stripMarkdown(md)))
			}
			dbg.text("static.md", md)
		}
		sb.Close()
	}

	res.Bucket, res.Reason = classify(classifyInput{
		Recall: res.Recall, OracleTokens: len(want), Oracle: o, OracleUnusable: unusable, Replay: res.Replay,
		PipelineErr: res.PipelineErr, Errors: append(append([]string{}, res.JSErrors...), res.ConsoleErrors...),
	})
	res.PassWithLoss = res.Bucket == bucketPass && res.Replay.MissConsequential > 0
	res.CellChecks = verifyCells(p.Cells, ev, res.StaticRecall, res.Scored && res.PipelineErr == "")
	dbg.report(os.Stderr, res, o)
	return res
}

// readAll walks Read's cursor to the end and returns the whole document plus the
// first page's result (which carries the render diagnostics).
func readAll(ctx context.Context, b *browser.Browser, req browser.Request, mode string) (string, *browser.ReadResult, error) {
	var sb strings.Builder
	var first *browser.ReadResult
	cursor := ""
	for i := 0; i < 400; i++ {
		r, err := b.Read(ctx, req, mode, browser.MaxReadTokens, cursor)
		if err != nil {
			return "", nil, err
		}
		if first == nil {
			first = r
			// Later pages re-read the page the first call resolved.
			if req.SessionID != "" {
				req.URL, req.UseCurrent = "", true
			}
		}
		sb.WriteString(r.Markdown)
		sb.WriteString("\n\n")
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
	}
	return sb.String(), first, nil
}

// repoRoot walks up from the working directory to the module root.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
