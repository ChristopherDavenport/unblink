//go:build wpt

package wpt

import (
	"regexp"
	"strings"

	"github.com/christopherdavenport/unblink/internal/js"
)

// bucket is where a subtest (or a whole non-running test) lands. Only bucketFix is
// in the conformance denominator; the rest are excluded and itemized.
type bucket string

const (
	bucketPass     bucket = "pass"     // subtest passed
	bucketFix      bucket = "fix"      // (a) real in-scope gap — the to-do list
	bucketByDesign bucket = "bydesign" // (b) failure caused by a declared non-goal
	bucketCeiling  bucket = "ceiling"  // (c) goja parse/compile ceiling
	bucketHarness  bucket = "harness"  // (d) runner can't provide iframes/workers/handlers
	bucketTimeout  bucket = "timeout"  // never completed within budget
)

// subResult is one subtest's classified outcome.
type subResult struct {
	Name   string `json:"name"`
	Bucket bucket `json:"bucket"`
	Status int    `json:"status"` // testharness subtest status (0..4), -1 for synthetic
	Msg    string `json:"message,omitempty"`
}

// testResult is one descriptor's outcome: its subtests, or a test-level bucket when
// it could not run (pre-skipped, engine ceiling, or timeout with no results).
type testResult struct {
	Dir     string      `json:"dir"`
	RelPath string      `json:"path"`
	Variant string      `json:"variant,omitempty"`
	Ran     bool        `json:"ran"`
	Subs    []subResult `json:"subs,omitempty"`
	Bucket  bucket      `json:"test_bucket,omitempty"` // set when !Ran
	Msg     string      `json:"message,omitempty"`
}

// wptResults is the JSON our reporter serialized into #__wpt_results.
type wptResults struct {
	HarnessStatus  int    `json:"harness_status"`
	HarnessMessage string `json:"harness_message"`
	Tests          []struct {
		Name    string `json:"name"`
		Status  int    `json:"status"`
		Message string `json:"message"`
	} `json:"tests"`
}

// parseErrSig matches engine diagnostics / harness messages that mean the script
// tripped goja's parser or an unsupported syntax feature — bucket (c), not a gap.
var parseErrSig = regexp.MustCompile(`(?i)(SyntaxError|Unexpected (token|reserved|identifier|end)|Invalid or unexpected|Line \d+:\d+|Compile|parse error|unsupported)`)

// byDesignGlobal are failure signatures whose cause is a documented permanent
// non-goal regardless of directory (asymmetric crypto, layout/CSSOM, CSS-level
// shadow styling). Matched against subtest name+message.
var byDesignGlobal = []string{
	// WebCrypto asymmetric + wrapping (ADR 0006: reject, not implement).
	"RSASSA", "RSA-PSS", "RSA-OAEP", "ECDSA", "ECDH", "Ed25519", "Ed448",
	"X25519", "X448", "wrapKey", "unwrapKey", "NotSupportedError",
	// Layout / geometry / CSSOM (permanent non-goal — constant stubs).
	"getBoundingClientRect", "getClientRects", "offsetWidth", "offsetHeight",
	"getComputedStyle", "clientWidth", "clientHeight", "scrollWidth",
	// Shadow-DOM CSS scoping / slot styling (ADR 0005 non-goal).
	"::slotted", ":host", "::part", "slotchange", "adoptedStyleSheets",
}

// byDesignByDir are per-directory by-design signatures.
var byDesignByDir = map[string][]string{
	// TextDecoder is UTF-8-first; legacy encodings are not implemented.
	"encoding": {},
}

// legacyEncodingRe matches a non-UTF-8 encoding label in a subtest name/message.
// A decode/encode failure naming one of these is a by-design gap in `encoding/`.
var legacyEncodingRe = regexp.MustCompile(`(?i)(shift_?jis|sjis|euc[-_]?jp|iso[-_]?2022[-_]?jp|gb18030|gbk|gb2312|big5|euc[-_]?kr|hz-gb|windows-?12(4[0-9]|5[0-8])|windows-?874|x-mac|macintosh|ibm8|koi8|iso-8859-(?:[2-9]|1[0-6])|x-user-defined|replacement|utf-16)`)

// classify turns a rendered descriptor into a testResult. found/payload come from
// the results node; diag is the engine's render diagnostics.
func classify(d testDesc, found bool, payload *wptResults, diag *js.RenderResult) testResult {
	tr := testResult{Dir: d.Dir, RelPath: d.RelPath, Variant: d.Variant}

	if d.Preskip == "bydesign" {
		tr.Bucket = bucketByDesign
		tr.Msg = d.SkipMsg
		return tr
	}
	if d.Preskip != "" {
		tr.Bucket = bucketHarness
		tr.Msg = d.SkipMsg
		return tr
	}

	if !found || payload == nil {
		// No results node: either the script never parsed (ceiling) or the run
		// never reached completion within budget (timeout).
		tr.Bucket = bucketTimeout
		tr.Msg = "no results node"
		if diag != nil {
			for _, e := range diag.Errors {
				if parseErrSig.MatchString(e) {
					tr.Bucket = bucketCeiling
					tr.Msg = firstLine(e)
					break
				}
			}
		}
		return tr
	}

	tr.Ran = true

	// Harness-level ERROR with a parse signature ⇒ the whole file hit the ceiling.
	if payload.HarnessStatus == 1 && parseErrSig.MatchString(payload.HarnessMessage) {
		tr.Ran = false
		tr.Bucket = bucketCeiling
		tr.Msg = firstLine(payload.HarnessMessage)
		return tr
	}

	for _, t := range payload.Tests {
		sr := subResult{Name: t.Name, Status: t.Status, Msg: firstLine(t.Message)}
		switch t.Status {
		case 0: // PASS
			sr.Bucket = bucketPass
		case 2: // TIMEOUT
			sr.Bucket = bucketTimeout
		default: // 1 FAIL, 3 NOTRUN, 4 PRECONDITION_FAILED
			sr.Bucket = classifyFail(d, t.Name+" "+t.Message)
		}
		tr.Subs = append(tr.Subs, sr)
	}
	return tr
}

// classifyFail decides whether a failing subtest is by-design (b) or a real
// in-scope gap (a). stub-only dirs are by-design wholesale; otherwise a match
// against the by-design tables demotes it out of the denominator.
func classifyFail(d testDesc, text string) bucket {
	if d.Bucket == stubOnly {
		return bucketByDesign
	}
	for _, sig := range byDesignGlobal {
		if strings.Contains(text, sig) {
			return bucketByDesign
		}
	}
	for _, sig := range byDesignByDir[d.Dir] {
		if strings.Contains(text, sig) {
			return bucketByDesign
		}
	}
	if d.Dir == "encoding" && legacyEncodingRe.MatchString(text) {
		return bucketByDesign
	}
	return bucketFix
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return strings.TrimSpace(s)
}
