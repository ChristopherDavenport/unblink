//go:build corpus

package corpus

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Buckets mirror the WPT runner's (ADR 0016) so a miss means the same thing in
// both reports. A page lands in a lettered bucket only when a recorded
// diagnostic supports it; a miss with no such evidence is "unclassified" and
// needs a human — the harness never guesses a cause.
const (
	bucketPass         = "pass"
	bucketGap          = "a" // engine diverged from Chromium with no known excuse: candidate in-scope gap
	bucketByDesign     = "b" // a declared non-goal API was hit (Workers, WebSocket, canvas, IndexedDB, cross-document)
	bucketCeiling      = "c" // the engine rejected script syntax Chromium ran
	bucketHarness      = "d" // the comparison itself is unsound for this page
	bucketUnclassified = "unclassified"
)

// passRecall is the main-content shingle recall at or above which a page passes.
const passRecall = 0.85

// minOracleTokens is the least oracle text a page needs to be scored at all.
const minOracleTokens = 40

var (
	challengeRE = regexp.MustCompile(`(?i)just a moment|verify(ing)? (that )?you are (a )?human|are you a robot|` +
		`attention required|unusual traffic|enable javascript and cookies|complete the captcha|access denied|` +
		`checking your browser`)
	syntaxRE = regexp.MustCompile(`(?i)syntaxerror|unexpected token|unexpected identifier|unexpected reserved word|` +
		`unexpected end of input|invalid regular expression`)
	// nonGoalRE matches an error that names an API unblink declares out of scope
	// (docs/architecture.md, "permanent non-goals").
	nonGoalRE = regexp.MustCompile(`\b(Worker|SharedWorker|ServiceWorker|serviceWorker|WebSocket|OffscreenCanvas|` +
		`WebGLRenderingContext|WebGL2RenderingContext|getContext|indexedDB|IDBKeyRange|importScripts|` +
		`contentWindow|contentDocument)\b`)
)

// classifyInput is everything bucketing may look at.
type classifyInput struct {
	Recall       float64
	OracleTokens int
	Oracle       *oracle
	// OracleUnusable is checkOracle's verdict: non-empty means the oracle is not
	// what a user saw and nothing may be scored against it.
	OracleUnusable string
	Replay         replayStats
	PipelineErr    string
	Errors         []string // uncaught JS errors + console errors from the replay
}

// classify buckets one page and says why. Checks run from "the comparison is
// unsound" outward, so an engine bucket is only reached on a clean comparison.
func classify(in classifyInput) (bucket, reason string) {
	o := in.Oracle
	switch {
	case in.OracleUnusable != "":
		return bucketHarness, "oracle unusable: " + in.OracleUnusable
	case in.OracleTokens < minOracleTokens:
		return bucketHarness, fmt.Sprintf("oracle too thin to score (%d tokens)", in.OracleTokens)
	}
	// Only a lost response that could change the rendered page makes the
	// comparison unsound. A page that merely missed beacons, ad calls or images is
	// a clean comparison and is judged on its score like any other.
	lost := in.Replay.MissConsequential
	if in.PipelineErr != "" {
		if lost > 0 {
			return bucketHarness, "replay incomplete and the pipeline errored: " + in.PipelineErr
		}
		return bucketUnclassified, "pipeline error: " + in.PipelineErr
	}
	if in.Recall >= passRecall {
		return bucketPass, ""
	}
	if lost > 0 {
		// The engine asked for something Chromium never fetched (or fetched under a
		// URL no tier could reconcile, or whose body the capture lacks). The miss
		// cannot be attributed either way.
		return bucketHarness, fmt.Sprintf("replay incomplete: %d consequential request(s) not answerable from the archive (%s)",
			lost, countsString(in.Replay.MissKinds))
	}
	errs := engineOnlyErrors(in.Errors, o.Exceptions)
	for _, e := range errs {
		if syntaxRE.MatchString(e) {
			return bucketCeiling, "script syntax rejected: " + head(e, 160)
		}
	}
	for _, e := range errs {
		if nonGoalRE.MatchString(e) {
			return bucketByDesign, "non-goal API: " + head(e, 160)
		}
	}
	if len(errs) > 0 {
		return bucketGap, "script error Chromium did not raise: " + head(errs[0], 160)
	}
	return bucketUnclassified, "below threshold with no diagnostic"
}

// engineOnlyErrors drops errors Chromium raised too: those are the page's own
// bugs, not a divergence.
func engineOnlyErrors(errs, chromium []string) []string {
	var out []string
	for _, e := range errs {
		ne := normErr(e)
		shared := false
		for _, c := range chromium {
			nc := normErr(c)
			if nc != "" && (strings.Contains(ne, head(nc, 60)) || strings.Contains(nc, head(ne, 60))) {
				shared = true
				break
			}
		}
		if !shared && ne != "" {
			out = append(out, e)
		}
	}
	return out
}

// countsString renders a small count map as "k=n, k=n", largest first.
func countsString(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

func normErr(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "Uncaught ")
	s = strings.TrimPrefix(s, "(in promise) ")
	return strings.ToLower(s)
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // don't cut a UTF-8 sequence
		n--
	}
	return s[:n]
}
