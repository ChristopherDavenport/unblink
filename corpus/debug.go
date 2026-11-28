//go:build corpus

package corpus

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// pageDebug is the one-page deep dive: CORPUS_DEBUG=1 prints every diagnostic a
// page produced (all lost requests, all script errors, the oracle's health, the
// manifest checks) to stderr as the page finishes, and CORPUS_DUMP=<dir> writes
// the texts being compared so they can be diffed:
//
//	<id>.oracle.txt   what Chromium showed (the text recall is measured against)
//	<id>.full.md      rendered mode=full Markdown
//	<id>.article.md   rendered mode=article Markdown
//	<id>.static.md    render=false mode=full Markdown
//	<id>.missing.txt  the oracle's 3-token shingles absent from full.md, in order
//
// Both are meant to be combined with CORPUS_ONLY=<id>.
type pageDebug struct {
	on   bool
	dump string
	id   string
}

func newPageDebug(id string) *pageDebug {
	return &pageDebug{on: debugOn(), dump: os.Getenv("CORPUS_DUMP"), id: id}
}

// text writes one dump file; a failure is reported, never fatal.
func (d *pageDebug) text(name, content string) {
	if d.dump == "" {
		return
	}
	if err := os.MkdirAll(d.dump, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "corpus: CORPUS_DUMP: %v\n", err)
		return
	}
	// d.id is a validated manifest slug and name a constant: no path is built
	// from captured data.
	if err := os.WriteFile(filepath.Join(d.dump, d.id+"."+name), []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "corpus: CORPUS_DUMP: %v\n", err)
	}
}

func (d *pageDebug) report(w io.Writer, r pageResult, o *oracle) {
	if !d.on {
		return
	}
	fmt.Fprintf(w, "\n---- %s  %s\n", r.ID, r.URL)
	fmt.Fprintf(w, "bucket %s  %s\n", r.Bucket, r.Reason)
	if r.Scored {
		fmt.Fprintf(w, "recall %.3f  token %.3f  static %.3f  article %.3f  precision %.3f  (oracle %q, %d tokens; output %d tokens)\n",
			r.Recall, r.TokenRecall, r.StaticRecall, r.ArticleRecall, r.Precision, r.OracleSource, r.OracleTokens, r.OutputTokens)
	} else {
		fmt.Fprintf(w, "unscored  (oracle %q, %d tokens; output %d tokens)\n", r.OracleSource, r.OracleTokens, r.OutputTokens)
	}
	fmt.Fprintf(w, "oracle: status %d, settled %v, %d elements, %d shadow roots, %d custom elements, links %d visible of %d, charset %s, rule %q, captured %s\n",
		o.DocStatus, o.Settled, o.DOMNodes, o.ShadowRoots, o.CustomElements, o.LinksVisible, o.LinksTotal, o.Charset, o.SettleRule, o.CapturedAt)
	for _, x := range r.OracleWarnings {
		fmt.Fprintf(w, "  oracle warning: %s\n", x)
	}
	fmt.Fprintf(w, "extracted: %d links, %d forms, %d controls (oracle %d / %d / %d)\n",
		r.Links, r.Forms, r.Controls, r.OracleLinks, r.OracleForms, r.OracleCtrls)
	fmt.Fprintf(w, "render: framework %q, budget hit %v, dom busy %v, net pending %d, failed %d, denied %d, pipeline error %q\n",
		r.Framework, r.RenderBudgetHit, r.DOMBusy, r.NetPending, r.NetFailed, r.NetDenied, r.PipelineErr)
	rs := r.Replay
	fmt.Fprintf(w, "replay: %d requests — %d exact, %d cache-bust, %d path, %d failed at capture, %d no body at capture, %d unmatched; %d non-http\n",
		rs.Requests, rs.Exact, rs.CacheBust, rs.PathOnly, rs.CaptureFailed, rs.CaptureNoBody, rs.Unmatched, rs.NonHTTP)
	if rs.MissConsequential+rs.MissHarmless > 0 {
		fmt.Fprintf(w, "lost: %d consequential, %d harmless, %d first-party; kinds %s; methods %s; near %s\n",
			rs.MissConsequential, rs.MissHarmless, rs.MissFirstParty,
			countsString(rs.MissKinds), countsString(rs.MissMethods), countsString(rs.MissNear))
	}
	for _, m := range rs.Misses {
		party := "3p"
		if m.FirstParty {
			party = "1p"
		}
		fmt.Fprintf(w, "  lost %-13s %s %-6s near=%-6s %s %s\n", m.Kind, party, m.Dest, m.Near, m.Method, m.URL)
	}
	if rs.Unrequested > 0 {
		fmt.Fprintf(w, "never requested: %d document/script/fetch response(s) Chromium loaded\n", rs.Unrequested)
	}
	for _, u := range rs.UnrequestedSample {
		fmt.Fprintf(w, "  never requested %s\n", u)
	}
	for _, u := range rs.NonHTTPSample {
		fmt.Fprintf(w, "  non-http URL sent to the transport: %s\n", u)
	}
	for _, e := range r.JSErrors {
		fmt.Fprintf(w, "  js error: %s\n", e)
	}
	for _, e := range r.ConsoleErrors {
		fmt.Fprintf(w, "  console error: %s\n", e)
	}
	for _, e := range o.Exceptions {
		fmt.Fprintf(w, "  chromium exception (the page's own): %s\n", e)
	}
	fmt.Fprintf(w, "capture shows: %s\n", strings.Join(r.Detected, ", "))
	for _, c := range r.CellChecks {
		fmt.Fprintf(w, "  cell %-28s %-12s %s\n", c.Cell, c.Verdict, c.Evidence)
	}
	if len(r.Unclaimed) > 0 {
		fmt.Fprintf(w, "  detected but not claimed by any cell: %s\n", strings.Join(r.Unclaimed, ", "))
	}
	if d.dump != "" {
		fmt.Fprintf(w, "texts written to %s/%s.{oracle.txt,full.md,article.md,static.md,missing.txt}\n", d.dump, d.id)
	}
}
