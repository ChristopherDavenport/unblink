//go:build corpus

package corpus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/mcpserver"
)

// This file is the package's only exported surface. It exists for the
// cross-tool comparison (scripts/membench -corpus-compare, `make
// corpus-compare`), which scores another tool's output on the same captures. That
// harness lives in another module — it carries the other tools' adapters and the
// BPE token meter, neither of which belongs in the root go.mod — so the oracle,
// the scorer, the replay matcher and the exclusion rules are exported from here
// rather than copied: both tools are scored by this code and no other.

// PassRecall is the main-content shingle recall at or above which a page passes.
const PassRecall = passRecall

// ReplayStats is what one replay asked of its archive.
type ReplayStats = replayStats

// ComparePage is one captured page as an external harness sees it.
type ComparePage struct {
	ID    string
	URL   string
	Cells []string

	// Unusable is non-empty when the capture's oracle is not what a user saw
	// (a block page, a document that was not laid out): the page is then not
	// scored for any tool. Warnings are weaker caveats carried beside a score.
	Unusable string
	Warnings []string

	OracleSource string // "main"-like selector, or "body"
	OracleTokens int
	OracleLinks  int // links visible at the snapshot
	OracleForms  int
	Chromium     string
	CapturedAt   string

	dir  string
	arch *archive
	want []string // oracle main-content tokens
	body []string // oracle whole-page tokens, the precision reference
}

// Scorable reports whether outputs for this page can be scored at all.
func (p *ComparePage) Scorable() bool {
	return p.Unusable == "" && len(p.want) >= minOracleTokens
}

// ComparePages loads every captured manifest page under root (default:
// $CORPUS_DIR, then testdata/corpus in the repo). only filters by page id or
// cell, as CORPUS_ONLY does. missing lists manifest pages with no capture.
func ComparePages(root string, only []string) (pages []*ComparePage, missing []string, err error) {
	manifest, err := loadManifest()
	if err != nil {
		return nil, nil, err
	}
	if root == "" {
		root = os.Getenv("CORPUS_DIR")
	}
	if root == "" {
		root = filepath.Join(repoRoot(), defaultDir)
	}
	for _, mp := range manifest {
		if len(only) > 0 && !selected(mp, only) {
			continue
		}
		dir := filepath.Join(root, mp.ID)
		if _, err := os.Stat(filepath.Join(dir, "oracle.json")); err != nil {
			missing = append(missing, mp.ID)
			continue
		}
		o, err := loadOracle(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", mp.ID, err)
		}
		a, err := loadArchive(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", mp.ID, err)
		}
		p := &ComparePage{
			ID: mp.ID, URL: mp.URL, Cells: mp.Cells, dir: dir, arch: a,
			OracleLinks: o.LinksVisible, OracleForms: o.Forms, Chromium: o.Chromium, CapturedAt: o.CapturedAt,
		}
		p.Unusable, p.Warnings = checkOracle(o, a)
		// The same choice of reference text runPage makes.
		p.want, p.OracleSource = tokenize(o.MainText), o.MainSelector
		if len(p.want) < minOracleTokens {
			p.want, p.OracleSource = tokenize(o.BodyText), "body"
		}
		p.body = tokenize(o.BodyText)
		p.OracleTokens = len(p.want)
		pages = append(pages, p)
	}
	if len(pages) == 0 {
		return nil, missing, fmt.Errorf("%w under %s", errNoCaptures, root)
	}
	return pages, missing, nil
}

// TextScore is one output scored against a page's oracle.
type TextScore struct {
	Scored      bool    `json:"scored"`
	Recall      float64 `json:"recall"`       // main-content 3-shingle recall
	TokenRecall float64 `json:"token_recall"` // main-content token-multiset recall
	Precision   float64 `json:"precision"`    // share of the output's words the browser displayed anywhere on the page
	Words       int     `json:"words"`        // word tokens in the output after Markdown targets are stripped
	Blocked     bool    `json:"blocked"`      // the output reads like a bot challenge or block page
}

// Score scores text — whatever a tool put in front of the model — against the
// page's oracle, with the normalisation runPage applies to unblink's own
// Markdown: link/image targets and bare URLs are dropped (a browser's innerText
// never shows them), then both sides are tokenised identically.
func (p *ComparePage) Score(text string) TextScore {
	got := tokenize(stripMarkdown(text))
	s := TextScore{Words: len(got), Blocked: challengeRE.MatchString(head(text, 4000))}
	if !p.Scorable() {
		return s
	}
	s.Scored = true
	s.Recall, s.TokenRecall = shingleRecall(p.want, got), tokenRecall(p.want, got)
	s.Precision = tokenPrecision(got, p.body)
	return s
}

// Excused applies the harness's consequential-loss rule to one tool's result: a
// score below PassRecall is not evidence about the tool when its replay lost a
// script, document, data or style request the archive could not answer.
func Excused(recall float64, s ReplayStats) bool {
	return recall < passRecall && s.MissConsequential > 0
}

// CompareReplay is a page's archive as an http.RoundTripper, for a tool that is
// not unblink (via Proxy) or for unblink itself.
type CompareReplay struct{ r *replay }

// NewReplay returns a fresh replay of the page's capture. Repeated requests are
// answered in capture order, so every run of every tool takes its own.
func (p *ComparePage) NewReplay() *CompareReplay {
	return &CompareReplay{r: newReplay(p.dir, p.arch)}
}

// RoundTrip answers from the archive and never touches the network.
func (c *CompareReplay) RoundTrip(req *http.Request) (*http.Response, error) {
	return c.r.RoundTrip(req)
}

// Stats is what has been asked of the archive so far.
func (c *CompareReplay) Stats() ReplayStats { return c.r.snapshot() }

// UnblinkRun is one unblink read of a page, as an MCP client receives it.
type UnblinkRun struct {
	Text            string // the tool result's text content, every page of it
	Calls           int
	Dur             time.Duration
	Err             string
	Mode            string // the mode the server reports having used
	RenderDiscarded bool
	Links           int
	Forms           int
	Replay          ReplayStats
}

// RunUnblink reads the page through a real, fresh unblink MCP server — the
// production tool surface with untrusted-content framing on — whose network is
// the page's replay. mode is "default" (one `read` call with no arguments but the
// URL: article mode, the default token budget), "article" (the same call,
// following next_cursor until the document is exhausted) or "full" (`read` with
// mode=full, likewise followed to the end).
//
// The browser is configured as runPage configures it: no retries, no
// robots/llms probes, no prewarm pool, the default JavaScript budget.
func RunUnblink(ctx context.Context, p *ComparePage, mode string) UnblinkRun {
	rp := p.NewReplay()
	out := UnblinkRun{}
	fail := func(err error) UnblinkRun {
		out.Err = err.Error()
		out.Replay = rp.Stats()
		return out
	}
	b, err := browser.New(
		browser.WithFetchOptions(fetch.WithSharedTransport(rp)),
		browser.WithJSRoundTripper(rp),
		browser.WithRetries(0),
		browser.WithSiteHints(false),
		browser.WithJSPrewarm(0),
		browser.WithSafeOutput(true),
		browser.WithJS(jsBudget()),
	)
	if err != nil {
		return fail(err)
	}
	defer b.Close()
	srv := mcpserver.New(b, mcpserver.Config{SafeOutput: true, JSEnabled: true})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st)
	if err != nil {
		return fail(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "corpus-compare", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return fail(err)
	}
	defer cs.Close()

	args := map[string]any{"url": p.URL}
	if mode == "full" {
		args["mode"] = "full"
	}
	var sb strings.Builder
	start := time.Now()
	for {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: args})
		if err != nil {
			out.Dur = time.Since(start)
			return fail(err)
		}
		out.Calls++
		text := resultText(res)
		if res.IsError {
			out.Dur = time.Since(start)
			return fail(fmt.Errorf("read returned an error result: %s", head(text, 200)))
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(text)
		var meta struct {
			Mode            string `json:"mode"`
			NextCursor      string `json:"next_cursor"`
			RenderDiscarded bool   `json:"render_discarded"`
		}
		_ = structured(res, &meta)
		if out.Calls == 1 {
			out.Mode, out.RenderDiscarded = meta.Mode, meta.RenderDiscarded
		}
		if mode == "default" || meta.NextCursor == "" || out.Calls >= 400 {
			break
		}
		args = map[string]any{"url": p.URL, "cursor": meta.NextCursor}
		if mode == "full" {
			args["mode"] = "full"
		}
	}
	out.Dur = time.Since(start)
	out.Text = sb.String()
	out.Replay = rp.Stats() // before the count calls below, which may fetch again

	// Counts for the link/form columns; not part of the timed or token-counted read.
	var n struct {
		Total int `json:"total"`
	}
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "links", Arguments: map[string]any{"url": p.URL}}); err == nil && !res.IsError {
		if structured(res, &n) == nil {
			out.Links = n.Total
		}
	}
	n.Total = 0
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "forms", Arguments: map[string]any{"url": p.URL}}); err == nil && !res.IsError {
		if structured(res, &n) == nil {
			out.Forms = n.Total
		}
	}
	return out
}

func resultText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok && tc.Text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func structured(r *mcp.CallToolResult, v any) error {
	if r == nil || r.StructuredContent == nil {
		return fmt.Errorf("no structured content")
	}
	data, err := json.Marshal(r.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
