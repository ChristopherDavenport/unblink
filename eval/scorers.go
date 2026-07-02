//go:build eval

package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/tokens"
)

// This file is the reusable scorer library. Every scorer returns a score in
// [0,1] with a one-line detail; scores are partial-credit (a fraction of checks
// passed) rather than binary, so a regression reveals its magnitude.

// --- small helpers ---

// resultAt fetches the result for a step, failing closed (score 0) when the
// step is missing, errored at the transport, or returned no result.
func resultAt(tr *Transcript, at int) (*mcpResult, string, bool) {
	sr, ok := tr.at(at)
	if !ok {
		return nil, fmt.Sprintf("no step at index %d", at), false
	}
	if sr.Err != nil {
		return nil, fmt.Sprintf("step %d transport error: %v", at, sr.Err), false
	}
	if sr.Result == nil {
		return nil, fmt.Sprintf("step %d returned no result", at), false
	}
	return &mcpResult{sr: sr}, "", true
}

// mcpResult wraps a step so scorers can pull text or decode structured content.
type mcpResult struct{ sr StepResult }

func (m *mcpResult) text() string { return textOf(m.sr.Result) }

func frac(matched, total int) float64 {
	if total == 0 {
		return 1
	}
	return float64(matched) / float64(total)
}

func boolScore(ok bool) float64 {
	if ok {
		return 1
	}
	return 0
}

// stripPageFooter removes read's pagination footer so token/dedup checks see the
// page body, not the "Page N of M …" trailer the read tool appends when
// truncated.
func stripPageFooter(text string) string {
	if i := strings.Index(text, "\n\n---\n_Page "); i >= 0 {
		return text[:i]
	}
	return text
}

// --- registration ---

// ToolsRegistered checks ListTools: the expected tool count, that every named
// tool is present, and that each tool carries a description and an input schema.
func ToolsRegistered(want int, names ...string) Scorer {
	return Scorer{Name: "tools-registered", Axis: AxisRegistration, Fn: func(tr *Transcript) (float64, string) {
		if tr.Tools == nil {
			return 0, "ListTools returned nothing"
		}
		got := tr.Tools.Tools
		have := map[string]bool{}
		describedOK, schemaOK := 0, 0
		for _, t := range got {
			have[t.Name] = true
			if strings.TrimSpace(t.Description) != "" {
				describedOK++
			}
			if t.InputSchema != nil {
				schemaOK++
			}
		}
		var missing []string
		for _, n := range names {
			if !have[n] {
				missing = append(missing, n)
			}
		}
		countOK := boolScore(len(got) == want)
		namesOK := frac(len(names)-len(missing), len(names))
		descOK := frac(describedOK, len(got))
		schOK := frac(schemaOK, len(got))
		score := (countOK + namesOK + descOK + schOK) / 4
		detail := fmt.Sprintf("%d tools (want %d), %d described, %d with schema", len(got), want, describedOK, schemaOK)
		if len(missing) > 0 {
			detail += "; missing " + strings.Join(missing, ",")
		}
		return score, detail
	}}
}

// --- junk-stripping / recall / token budget (read, find) ---

// NoJunk scores the absence of visual-only junk in a step's text: 1 minus the
// fraction of probe strings that leaked through.
func NoJunk(at int, probes ...string) Scorer {
	return Scorer{Name: "no-junk", Axis: AxisJunk, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		text := r.text()
		var leaked []string
		for _, p := range probes {
			if strings.Contains(text, p) {
				leaked = append(leaked, p)
			}
		}
		score := 1 - frac(len(leaked), len(probes))
		if len(leaked) == 0 {
			return score, fmt.Sprintf("all %d junk probes stripped", len(probes))
		}
		return score, "leaked: " + strings.Join(leaked, ", ")
	}}
}

// Recall scores the fraction of expected phrases present in a step's text.
func Recall(at int, phrases ...string) Scorer {
	return Scorer{Name: "recall", Axis: AxisRecall, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		text := r.text()
		var missing []string
		for _, p := range phrases {
			if !strings.Contains(text, p) {
				missing = append(missing, p)
			}
		}
		score := frac(len(phrases)-len(missing), len(phrases))
		if len(missing) == 0 {
			return score, fmt.Sprintf("all %d phrases recalled", len(phrases))
		}
		return score, "missing: " + strings.Join(missing, ", ")
	}}
}

// NotRecall scores 1 only when NONE of the given phrases appear in a step's text
// — the inverse of Recall. Used for the security case: a leaked credential must
// never appear in a cross-origin response body.
func NotRecall(at int, phrases ...string) Scorer {
	return Scorer{Name: "not-recall", Axis: AxisAuth, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		text := r.text()
		var leaked []string
		for _, p := range phrases {
			if strings.Contains(text, p) {
				leaked = append(leaked, p)
			}
		}
		if len(leaked) > 0 {
			return 0, "LEAKED: " + strings.Join(leaked, ", ")
		}
		return 1, fmt.Sprintf("none of %d phrases leaked", len(phrases))
	}}
}

// TokenBudget scores whether a read step's body fits the requested budget,
// allowing ~10% slack and degrading proportionally past it.
func TokenBudget(at int, max int) Scorer {
	return Scorer{Name: "token-budget", Axis: AxisToken, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		est := tokens.Estimate(stripPageFooter(r.text()))
		slack := float64(max) * 1.1
		score := 1.0
		if float64(est) > slack {
			score = slack / float64(est)
		}
		return score, fmt.Sprintf("≈%d tokens (budget %d, slack %.0f)", est, max, slack)
	}}
}

// PaginationRoundTrip re-drives read from the page at `at`, following NextCursor
// to the end, and scores continuity: ≥2 pages, each within budget, no duplicate
// bodies, and (if sentinels are given) the first sentinel on page 1 and the last
// sentinel on the final page — proving the whole document is reachable in order.
func PaginationRoundTrip(at int, max int, sentinels ...string) Scorer {
	return Scorer{Name: "pagination-roundtrip", Axis: AxisPagination, Fn: func(tr *Transcript) (float64, string) {
		sr, ok := tr.at(at)
		if !ok {
			return 0, fmt.Sprintf("no step at index %d", at)
		}
		if sr.Err != nil || sr.Result == nil {
			return 0, fmt.Sprintf("step %d unusable", at)
		}

		type pageView struct {
			body   string
			cursor string
		}
		var pages []pageView

		var rr browser.ReadResult
		if err := into(sr.Result, &rr); err != nil {
			return 0, "decode page 1: " + err.Error()
		}
		pages = append(pages, pageView{body: stripPageFooter(textOf(sr.Result)), cursor: rr.NextCursor})

		// Follow cursors to the end, bounded by TotalPages to avoid a runaway.
		limit := rr.TotalPages + 2
		cursor := rr.NextCursor
		for cursor != "" && len(pages) < limit {
			args := map[string]any{}
			for k, v := range sr.Args {
				args[k] = v
			}
			args["cursor"] = cursor
			res, err := tr.replay(sr.Tool, args)
			if err != nil || res == nil {
				return 0, fmt.Sprintf("replay page %d failed: %v", len(pages)+1, err)
			}
			var next browser.ReadResult
			if err := into(res, &next); err != nil {
				return 0, "decode page: " + err.Error()
			}
			pages = append(pages, pageView{body: stripPageFooter(textOf(res)), cursor: next.NextCursor})
			cursor = next.NextCursor
		}

		// Component 1: more than one page (otherwise pagination is untested).
		multiPage := boolScore(len(pages) >= 2)

		// Component 2: every page within the token budget (+slack).
		slack := float64(max) * 1.1
		within := 0
		for _, p := range pages {
			if float64(tokens.Estimate(p.body)) <= slack {
				within++
			}
		}
		budgetOK := frac(within, len(pages))

		// Component 3: no duplicate page bodies.
		seen := map[string]bool{}
		dups := 0
		for _, p := range pages {
			if seen[p.body] {
				dups++
			}
			seen[p.body] = true
		}
		uniqueOK := 1 - frac(dups, len(pages))

		// Component 4: sentinel reachability (first on page 1, last on last page).
		reach := 1.0
		if len(sentinels) > 0 {
			checks, hit := 0, 0
			checks++
			if strings.Contains(pages[0].body, sentinels[0]) {
				hit++
			}
			if len(sentinels) > 1 {
				checks++
				if strings.Contains(pages[len(pages)-1].body, sentinels[len(sentinels)-1]) {
					hit++
				}
			}
			reach = frac(hit, checks)
		}

		score := (multiPage + budgetOK + uniqueOK + reach) / 4
		return score, fmt.Sprintf("%d pages, %d within budget, %d dup, reach=%.2f", len(pages), within, dups, reach)
	}}
}

// --- structured data (data tool) ---

// DataContains decodes a data result and scores the fraction of expected
// substrings present in its marshaled JSON — works uniformly across the
// jsonld/tables/microdata payloads.
func DataContains(at int, subs ...string) Scorer {
	return Scorer{Name: "data-contains", Axis: AxisStructure, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var dr browser.DataResult
		if err := into(r.sr.Result, &dr); err != nil {
			return 0, "decode data: " + err.Error()
		}
		blob, err := json.Marshal(dr)
		if err != nil {
			return 0, "marshal data: " + err.Error()
		}
		text := string(blob)
		var missing []string
		for _, s := range subs {
			if !strings.Contains(text, s) {
				missing = append(missing, s)
			}
		}
		score := frac(len(subs)-len(missing), len(subs))
		if len(missing) == 0 {
			return score, fmt.Sprintf("all %d fields present", len(subs))
		}
		return score, "missing: " + strings.Join(missing, ", ")
	}}
}

// DataCounts checks a data result's per-kind item counts (jsonld/tables/microdata).
func DataCounts(at, jsonld, tables, microdata int) Scorer {
	return Scorer{Name: "data-counts", Axis: AxisStructure, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var dr browser.DataResult
		if err := into(r.sr.Result, &dr); err != nil {
			return 0, "decode data: " + err.Error()
		}
		got := dr.Counts
		hits := boolScore(got.JSONLD == jsonld) + boolScore(got.Tables == tables) + boolScore(got.Microdata == microdata)
		return hits / 3, fmt.Sprintf("counts jsonld=%d/%d tables=%d/%d microdata=%d/%d",
			got.JSONLD, jsonld, got.Tables, tables, got.Microdata, microdata)
	}}
}

// --- non-HTML content (read of pdf/feed/json/text/image) ---

// KindIs checks that a read result classified the page as the expected Kind
// (html|pdf|json|feed|text|image), proving the content router dispatched right.
func KindIs(at int, want string) Scorer {
	return Scorer{Name: "content-kind", Axis: AxisContent, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var rr browser.ReadResult
		if err := into(r.sr.Result, &rr); err != nil {
			return 0, "decode read: " + err.Error()
		}
		return boolScore(rr.Kind == want), fmt.Sprintf("kind=%q (want %q)", rr.Kind, want)
	}}
}

// BinaryIntegrity proves that binary content survives the pipeline uncorrupted:
// the image bytes returned by read(include_bytes=true) must be byte-identical to
// the on-disk fixture. This is the headline "no charset corruption" check — the
// old unconditional charset decode would have mangled these bytes.
func BinaryIntegrity(at int, fixturePath string) Scorer {
	return Scorer{Name: "binary-integrity", Axis: AxisContent, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		ic, ok := imageOf(r.sr.Result)
		if !ok {
			return 0, "no image content in result (include_bytes?)"
		}
		want, err := os.ReadFile(fixturePath)
		if err != nil {
			return 0, "read fixture: " + err.Error()
		}
		if bytes.Equal(ic.Data, want) {
			return 1, fmt.Sprintf("image bytes byte-identical (%d bytes)", len(want))
		}
		return 0, fmt.Sprintf("image bytes differ: got %d, want %d", len(ic.Data), len(want))
	}}
}

// --- structured extraction (browse, links, forms, find) ---

// BrowseTitle checks a browse/click/submit result's title.
func BrowseTitle(at int, want string) Scorer {
	return titleScorer("browse-title", AxisStructure, at, want)
}

// SessionTitle checks a click/submit result's title, proving the session cookie
// was carried (it lands on the gated page).
func SessionTitle(at int, want string) Scorer {
	return titleScorer("session-title", AxisSession, at, want)
}

func titleScorer(name string, axis Axis, at int, want string) Scorer {
	return Scorer{Name: name, Axis: axis, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var b browser.BrowseResult
		if err := into(r.sr.Result, &b); err != nil {
			return 0, "decode browse: " + err.Error()
		}
		return boolScore(b.Title == want), fmt.Sprintf("title=%q (want %q)", b.Title, want)
	}}
}

// SessionCurrentTitle checks the title of the page a session back/forward/state
// result landed on (its `current` BrowseResult), proving navigation moved as
// expected.
func SessionCurrentTitle(at int, want string) Scorer {
	return Scorer{Name: "session-current-title", Axis: AxisSession, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var so sessionOut
		if err := into(r.sr.Result, &so); err != nil {
			return 0, "decode session: " + err.Error()
		}
		if so.Current == nil {
			return 0, "no current page in result"
		}
		return boolScore(so.Current.Title == want), fmt.Sprintf("title=%q (want %q)", so.Current.Title, want)
	}}
}

// BrowseCounts checks the structural element counts in a browse result.
func BrowseCounts(at, wantForms, wantHeadings int) Scorer {
	return Scorer{Name: "browse-counts", Axis: AxisStructure, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var b browser.BrowseResult
		if err := into(r.sr.Result, &b); err != nil {
			return 0, "decode browse: " + err.Error()
		}
		matched := boolScore(b.Counts.Forms == wantForms) + boolScore(b.Counts.Headings == wantHeadings)
		return matched / 2, fmt.Sprintf("forms=%d (want %d), headings=%d (want %d)",
			b.Counts.Forms, wantForms, b.Counts.Headings, wantHeadings)
	}}
}

// LinksInternal validates internal/external classification on a links result
// taken with internal_only=true: every internal substring present, every
// external substring absent, and every returned link flagged internal.
func LinksInternal(at int, internalSubs, externalSubs []string) Scorer {
	return Scorer{Name: "links-internal", Axis: AxisStructure, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var lo linksOut
		if err := into(r.sr.Result, &lo); err != nil {
			return 0, "decode links: " + err.Error()
		}
		hrefHas := func(sub string) bool {
			for _, l := range lo.Links {
				if strings.Contains(l.Href, sub) {
					return true
				}
			}
			return false
		}
		internalHit := 0
		for _, s := range internalSubs {
			if hrefHas(s) {
				internalHit++
			}
		}
		externalAbsent := 0
		for _, s := range externalSubs {
			if !hrefHas(s) {
				externalAbsent++
			}
		}
		allInternal := true
		for _, l := range lo.Links {
			if !l.Internal {
				allInternal = false
				break
			}
		}
		a := frac(internalHit, len(internalSubs))
		b := frac(externalAbsent, len(externalSubs))
		c := boolScore(allInternal)
		return (a + b + c) / 3, fmt.Sprintf("%d/%d internal present, %d/%d external excluded, all-internal=%v",
			internalHit, len(internalSubs), externalAbsent, len(externalSubs), allInternal)
	}}
}

// LinksCount checks the filtered link count returned by a links step.
func LinksCount(at, want int) Scorer {
	return Scorer{Name: "links-count", Axis: AxisStructure, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var lo linksOut
		if err := into(r.sr.Result, &lo); err != nil {
			return 0, "decode links: " + err.Error()
		}
		return boolScore(lo.Count == want), fmt.Sprintf("count=%d (want %d)", lo.Count, want)
	}}
}

// FieldSpec describes one expected form field for FormShape.
type FieldSpec struct {
	Name     string
	Type     string
	Required bool
	Options  []string
}

// FormShape checks a forms result against an expected method and field set
// (name/type/required/options), scoring the fraction of checks that pass.
func FormShape(at int, method string, fields []FieldSpec) Scorer {
	return Scorer{Name: "form-shape", Axis: AxisStructure, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var fo formsOut
		if err := into(r.sr.Result, &fo); err != nil {
			return 0, "decode forms: " + err.Error()
		}
		if len(fo.Forms) == 0 {
			return 0, "no forms extracted"
		}
		form := fo.Forms[0]
		byName := map[string]struct {
			typ      string
			required bool
			options  []string
		}{}
		for _, fl := range form.Fields {
			byName[fl.Name] = struct {
				typ      string
				required bool
				options  []string
			}{fl.Type, fl.Required, fl.Options}
		}

		checks, passed := 0, 0
		checks++
		if strings.EqualFold(form.Method, method) {
			passed++
		}
		var problems []string
		for _, spec := range fields {
			got, found := byName[spec.Name]
			checks += 3 // present, type, required
			if !found {
				problems = append(problems, spec.Name+":absent")
				continue
			}
			passed++ // present
			if got.typ == spec.Type {
				passed++
			} else {
				problems = append(problems, fmt.Sprintf("%s:type=%s≠%s", spec.Name, got.typ, spec.Type))
			}
			if got.required == spec.Required {
				passed++
			} else {
				problems = append(problems, fmt.Sprintf("%s:required=%v", spec.Name, got.required))
			}
			if len(spec.Options) > 0 {
				checks++
				if containsAll(got.options, spec.Options) {
					passed++
				} else {
					problems = append(problems, spec.Name+":options")
				}
			}
		}
		detail := fmt.Sprintf("method=%s, %d/%d field checks", form.Method, passed, checks)
		if len(problems) > 0 {
			detail += "; " + strings.Join(problems, ", ")
		}
		return frac(passed, checks), detail
	}}
}

func containsAll(have, want []string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// FindHit checks a find result: the match count, a snippet substring, and the
// heading path that should locate the match.
func FindHit(at int, snippetSub, pathSub string, count int) Scorer {
	return Scorer{Name: "find-hit", Axis: AxisFind, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var fo findOut
		if err := into(r.sr.Result, &fo); err != nil {
			return 0, "decode find: " + err.Error()
		}
		countOK := boolScore(fo.Count == count)
		snippetOK, pathOK := 0.0, 0.0
		for _, h := range fo.Hits {
			if strings.Contains(strings.ToLower(h.Snippet), strings.ToLower(snippetSub)) {
				snippetOK = 1
				if strings.Contains(h.HeadingPath, pathSub) {
					pathOK = 1
				}
			}
		}
		return (countOK + snippetOK + pathOK) / 3,
			fmt.Sprintf("count=%d (want %d), snippet=%v, path=%v", fo.Count, count, snippetOK == 1, pathOK == 1)
	}}
}

// --- session state ---

// HistoryShape checks a session history result's length and current position.
func HistoryShape(at, length, position int) Scorer {
	return Scorer{Name: "history-shape", Axis: AxisSession, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var so sessionOut
		if err := into(r.sr.Result, &so); err != nil {
			return 0, "decode session: " + err.Error()
		}
		if so.History == nil {
			return 0, "no history in result"
		}
		matched := boolScore(len(so.History.URLs) == length) + boolScore(so.History.Position == position)
		return matched / 2, fmt.Sprintf("len=%d (want %d), pos=%d (want %d)",
			len(so.History.URLs), length, so.History.Position, position)
	}}
}

// StateURLContains checks the current URL reported by a session state result.
func StateURLContains(at int, sub string) Scorer {
	return Scorer{Name: "state-url", Axis: AxisSession, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var so sessionOut
		if err := into(r.sr.Result, &so); err != nil {
			return 0, "decode session: " + err.Error()
		}
		if so.State == nil {
			return 0, "no state in result"
		}
		return boolScore(strings.Contains(so.State.CurrentURL, sub)),
			fmt.Sprintf("current_url=%q (want ~%q)", so.State.CurrentURL, sub)
	}}
}

// --- site metadata ---

// SiteRobots checks a site result's robots.txt summary: present, the expected
// disallow patterns, and (optionally) a positive crawl-delay.
func SiteRobots(at int, disallow []string, crawlDelayPositive bool) Scorer {
	return Scorer{Name: "site-robots", Axis: AxisSite, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var sr browser.SiteResult
		if err := into(r.sr.Result, &sr); err != nil {
			return 0, "decode site: " + err.Error()
		}
		if sr.Robots == nil {
			return 0, "no robots summary"
		}
		checks, passed := 0, 0
		checks++
		if sr.Robots.Present {
			passed++
		}
		for _, d := range disallow {
			checks++
			if containsString(sr.Robots.Disallow, d) {
				passed++
			}
		}
		if crawlDelayPositive {
			checks++
			if sr.Robots.CrawlDelay > 0 {
				passed++
			}
		}
		return frac(passed, checks), fmt.Sprintf("present=%v, disallow=%v, crawl_delay=%.1f",
			sr.Robots.Present, sr.Robots.Disallow, sr.Robots.CrawlDelay)
	}}
}

// LLMsInline checks a site result's inline llms.txt content and the presence of
// llms-full.txt.
func LLMsInline(at int, contentSub string, fullAvail bool) Scorer {
	return Scorer{Name: "llms-inline", Axis: AxisSite, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var sr browser.SiteResult
		if err := into(r.sr.Result, &sr); err != nil {
			return 0, "decode site: " + err.Error()
		}
		contentOK := 0.0
		if sr.LLMsTxt != nil && strings.Contains(sr.LLMsTxt.Content, contentSub) {
			contentOK = 1
		}
		fullOK := boolScore(sr.LLMsFullAvailable == fullAvail)
		return (contentOK + fullOK) / 2, fmt.Sprintf("llms_inline=%v, full_available=%v (want %v)",
			contentOK == 1, sr.LLMsFullAvailable, fullAvail)
	}}
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// --- js render ---

// RenderPresence checks that content absent without rendering appears once the
// page's JavaScript runs: every `absent` string missing at plainAt, every
// `present` string found at renderAt.
func RenderPresence(plainAt int, absent []string, renderAt int, present []string) Scorer {
	return Scorer{Name: "render-presence", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		plain, why, ok := resultAt(tr, plainAt)
		if !ok {
			return 0, why
		}
		rendered, why, ok := resultAt(tr, renderAt)
		if !ok {
			return 0, why
		}
		plainText, renderedText := plain.text(), rendered.text()
		absentOK := 0
		for _, a := range absent {
			if !strings.Contains(plainText, a) {
				absentOK++
			}
		}
		presentOK := 0
		for _, p := range present {
			if strings.Contains(renderedText, p) {
				presentOK++
			}
		}
		return frac(absentOK+presentOK, len(absent)+len(present)),
			fmt.Sprintf("%d/%d absent-before, %d/%d present-after", absentOK, len(absent), presentOK, len(present))
	}}
}

// WaitMet asserts a read's wait_met flag — the outcome of a wait_for/wait_text gate.
func WaitMet(at int, want bool) Scorer {
	return Scorer{Name: "wait-met", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var rr browser.ReadResult
		if err := into(r.sr.Result, &rr); err != nil {
			return 0, "decode read: " + err.Error()
		}
		if rr.WaitMet == nil {
			return 0, "wait_met absent (no wait condition recorded)"
		}
		if *rr.WaitMet != want {
			return 0, fmt.Sprintf("wait_met=%v, want %v", *rr.WaitMet, want)
		}
		return 1, fmt.Sprintf("wait_met=%v", *rr.WaitMet)
	}}
}

// PendingNavigation asserts an interact surfaced the expected JS-requested navigation.
func PendingNavigation(at int, wantURL string) Scorer {
	return Scorer{Name: "pending-navigation", Axis: AxisRender, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var ir browser.InteractResult
		if err := into(r.sr.Result, &ir); err != nil {
			return 0, "decode interact: " + err.Error()
		}
		if ir.PendingNavigation != wantURL {
			return 0, fmt.Sprintf("pending_navigation=%q, want %q", ir.PendingNavigation, wantURL)
		}
		return 1, "pending_navigation=" + ir.PendingNavigation
	}}
}

// --- safe output ---

// FramedUntrusted checks the production untrusted-content framing on a step:
// the provenance banner is present and the per-call fence token brackets the
// content (opens and closes — exactly two occurrences).
func FramedUntrusted(at int) Scorer {
	return Scorer{Name: "framed-untrusted", Axis: AxisSafety, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		text := r.text()
		bannerOK := strings.Contains(text, "[UNTRUSTED WEB CONTENT")
		fences := strings.Count(text, "«untrusted")
		// The banner names the token once and the fence brackets the content twice.
		fenceOK := fences == 3
		return (boolScore(bannerOK) + boolScore(fenceOK)) / 2,
			fmt.Sprintf("banner=%v, fence-token occurrences=%d (want 3)", bannerOK, fences)
	}}
}

// --- error path ---

// IsErrorIs asserts the IsError surfacing contract for a step.
func IsErrorIs(at int, want bool) Scorer {
	return Scorer{Name: "is-error", Axis: AxisError, Fn: func(tr *Transcript) (float64, string) {
		sr, ok := tr.at(at)
		if !ok {
			return 0, fmt.Sprintf("no step at index %d", at)
		}
		if sr.Err != nil {
			return 0, fmt.Sprintf("transport error (want IsError=%v): %v", want, sr.Err)
		}
		if sr.Result == nil {
			return 0, "nil result"
		}
		return boolScore(sr.Result.IsError == want), fmt.Sprintf("IsError=%v (want %v)", sr.Result.IsError, want)
	}}
}

// --- search & discovery ---

// MapSameOrigin checks every discovered URL is same-origin as the map's Origin
// and that Count matches the URL list (no cross-origin loc/link leaked in).
func MapSameOrigin(at int) Scorer {
	return Scorer{Name: "map-same-origin", Axis: AxisDiscovery, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var mr browser.MapResult
		if err := into(r.sr.Result, &mr); err != nil {
			return 0, "decode map: " + err.Error()
		}
		if len(mr.URLs) == 0 {
			return 0, "map returned no URLs"
		}
		same := 0
		for _, e := range mr.URLs {
			if strings.HasPrefix(e.URL, mr.Origin) {
				same++
			}
		}
		originFrac := frac(same, len(mr.URLs))
		countOK := boolScore(mr.Count == len(mr.URLs))
		return (originFrac + countOK) / 2, fmt.Sprintf("%d/%d same-origin, count=%d", same, len(mr.URLs), mr.Count)
	}}
}

// MapContains checks the map's URLs include every expected substring (e.g. a
// sitemap-declared path and a crawl-discovered one).
func MapContains(at int, subs ...string) Scorer {
	return Scorer{Name: "map-contains", Axis: AxisDiscovery, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var mr browser.MapResult
		if err := into(r.sr.Result, &mr); err != nil {
			return 0, "decode map: " + err.Error()
		}
		has := func(sub string) bool {
			for _, e := range mr.URLs {
				if strings.Contains(e.URL, sub) {
					return true
				}
			}
			return false
		}
		hit := 0
		for _, s := range subs {
			if has(s) {
				hit++
			}
		}
		return frac(hit, len(subs)), fmt.Sprintf("%d/%d expected URLs present", hit, len(subs))
	}}
}

// MapRespectsCap checks a bounded map: Count stays within capN and Truncated is
// flagged when the cap binds.
func MapRespectsCap(at, capN int) Scorer {
	return Scorer{Name: "map-respects-cap", Axis: AxisDiscovery, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var mr browser.MapResult
		if err := into(r.sr.Result, &mr); err != nil {
			return 0, "decode map: " + err.Error()
		}
		withinCap := boolScore(mr.Count <= capN)
		truncated := boolScore(mr.Truncated)
		return (withinCap + truncated) / 2, fmt.Sprintf("count=%d (cap %d), truncated=%v", mr.Count, capN, mr.Truncated)
	}}
}

// SearchReturns checks a configured search step returned the canned results
// (expected URLs present, Count consistent, provider name reported).
func SearchReturns(at int, wantURLs ...string) Scorer {
	return Scorer{Name: "search-returns", Axis: AxisDiscovery, Fn: func(tr *Transcript) (float64, string) {
		r, why, ok := resultAt(tr, at)
		if !ok {
			return 0, why
		}
		var sr browser.SearchResult
		if err := into(r.sr.Result, &sr); err != nil {
			return 0, "decode search: " + err.Error()
		}
		has := func(u string) bool {
			for _, res := range sr.Results {
				if res.URL == u {
					return true
				}
			}
			return false
		}
		hit := 0
		for _, u := range wantURLs {
			if has(u) {
				hit++
			}
		}
		urlFrac := frac(hit, len(wantURLs))
		countOK := boolScore(sr.Count == len(sr.Results))
		provOK := boolScore(sr.Provider == "fake")
		return (urlFrac + countOK + provOK) / 3, fmt.Sprintf("%d/%d urls, count=%d, provider=%s", hit, len(wantURLs), sr.Count, sr.Provider)
	}}
}

// SearchUnconfigured checks the search tool errors cleanly (IsError with a
// "not configured" message) when no provider is set.
func SearchUnconfigured(at int) Scorer {
	return Scorer{Name: "search-unconfigured", Axis: AxisDiscovery, Fn: func(tr *Transcript) (float64, string) {
		sr, ok := tr.at(at)
		if !ok {
			return 0, fmt.Sprintf("no step at index %d", at)
		}
		if sr.Result == nil {
			return 0, "nil result"
		}
		msgOK := strings.Contains(textOf(sr.Result), "not configured")
		return (boolScore(sr.Result.IsError) + boolScore(msgOK)) / 2,
			fmt.Sprintf("IsError=%v, msg=%v", sr.Result.IsError, msgOK)
	}}
}
