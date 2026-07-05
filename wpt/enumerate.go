//go:build wpt

package wpt

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// readHeaders reads a WPT `.headers` sidecar for the test at rel (e.g.
// "csp/foo.html" -> "csp/foo.html.headers") and parses its "Name: Value" lines
// into an http.Header. Returns nil when there is no sidecar. The document's real
// response headers are what the engine's CSP/COOP/COEP enforcement reads.
func readHeaders(root, rel string) http.Header {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)+".headers"))
	if err != nil {
		return nil
	}
	h := http.Header{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		h.Add(strings.TrimSpace(name), strings.TrimSpace(val))
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

// budgetOverride returns the per-test budget, honoring a WPT_BUDGET env override
// (e.g. "2s") used for fast diagnostic scans; long is scaled proportionally.
func budgets() (def, long time.Duration) {
	def, long = defaultBudget, longBudget
	if v := os.Getenv("WPT_BUDGET"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			def = d
			long = 4 * d
		}
	}
	return def, long
}

// testDesc is one runnable test variant: the page HTML to render (read for .html,
// synthesized for .window.js/.any.js), its synthetic URL, and any static pre-skip.
type testDesc struct {
	Dir     string     // scope rule key, e.g. "encoding" or "html/dom"
	Bucket  bucketKind // the dir's scope bucket
	RelPath string     // wpt-root-relative test path, e.g. "encoding/api-basics.any.js"
	URLPath string     // synthetic page path incl. variant, e.g. "/encoding/api-basics.any.html?x"
	Variant string     // variant query incl. leading "?", "" when none
	HTML    string     // page HTML fed to the engine
	Budget  time.Duration
	Headers http.Header // document response headers from a .headers sidecar (CSP etc.); nil when none
	Preskip string      // "" runnable; else a bucket reason: "harness" (d) — statically excluded
	SkipMsg string      // human note for the pre-skip
}

// nonWindowSkip counts .any.js tests whose global excludes window (worker-only) —
// out of a window-only engine's reach, tallied per dir but not run.

const (
	defaultBudget = 5 * time.Second
	// longBudget caps `<meta timeout=long>` tests. WPT's "long" is 60s, but this is
	// an analysis tool over content-extraction APIs where a test needing >8s to
	// settle is almost always hung on an unsupported capability, not slow — and the
	// injected testharness self-timeout (see testharnessreport.js) fires first
	// anyway. Capping keeps a hung long-test from burning a full minute.
	longBudget = 8 * time.Second
)

// Signals that a test needs something the single-document, single-thread,
// single-origin engine cannot provide. A hit pre-skips the whole test to bucket
// (d), so it neither passes nor pollutes the conformance denominator.
var harnessLimitSignals = []string{
	"new Worker(", "new SharedWorker(", "SharedWorker(",
	"fetch_tests_from_worker", "fetch_tests_from_window", "test_worker",
	"<iframe", "<frame", "window.open(", ".contentWindow", "window.frames", ".contentDocument",
	`createElement('iframe'`, `createElement("iframe"`, `createElement(\"iframe\"`,
	"self.frames", ".frames[", "frames.length",
	"get-host-info", "{{hosts[", "{{domains[", "{{host}}", // multi-origin / server substitution
	"createServiceWorker", "navigator.serviceWorker.register", "sharedWorker",
	"/resources/idlharness.js", "idlharness.js",
	"importScripts(",
	// testdriver.js drives real browser automation over WebDriver; with no driver
	// its actions (click/key/bless/gc) return promises that never resolve, so the
	// test hangs the full budget. These need a real browser — genuinely (d).
	"testdriver.js", "test_driver.", "/common/gc.js",
	// subset-tests.js is WPT's variant-split FORM-SUBMISSION harness (legacy encoding
	// tests): it submits real forms into iframes to observe wire-encoding — needs a
	// browsing context the runner can't provide. NOTE: subset-tests-by-key.js is a
	// different, pure-JS case filter (used by url-constructor) and must NOT match.
	"/common/subset-tests.js",
}

var variantMetaRe = regexp.MustCompile(`(?i)<meta\s+name=["']?variant["']?\s+content=["']?([^"'>]*)["']?`)

// enumerate walks the vendored corpus and returns every runnable test variant.
func enumerate(root string, s *scope) ([]testDesc, map[string]int, error) {
	var out []testDesc
	nonWindow := map[string]int{} // per-dir count of skipped worker-only .any.js

	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := filepath.ToSlash(mustRel(root, p))
		dir, bucket, ok := s.dirFor(rel)
		if !ok || !runnable(bucket) || s.skipByPath(rel) {
			return nil
		}
		base := path.Base(rel)
		// Subtrees declared out-of-scope-by-design (e.g. legacy encodings) are
		// counted as by-design, not run — one descriptor per test file, no variant
		// expansion, so they cost nothing and don't hang the budget.
		if s.byDesignPath(rel) {
			if isTestFile(base) {
				out = append(out, testDesc{
					Dir: dir, Bucket: bucket, RelPath: rel, URLPath: "/" + rel,
					Preskip: "bydesign", SkipMsg: "declared by-design non-goal (see scope.json)",
				})
			}
			return nil
		}
		// Suites that assert on server-received headers are not measurable by a
		// client-side runner — one (d) descriptor per test, not run.
		if s.harnessLimitedPath(rel) {
			if isTestFile(base) {
				out = append(out, testDesc{
					Dir: dir, Bucket: bucket, RelPath: rel, URLPath: "/" + rel,
					Preskip: "harness", SkipMsg: "server-side header observation not measurable (see scope.json)",
				})
			}
			return nil
		}
		var descs []testDesc
		switch {
		case strings.HasSuffix(base, ".any.js"):
			src, _ := os.ReadFile(p)
			m := parseMeta(string(src))
			if !m.hasWindow() {
				nonWindow[dir]++
				return nil
			}
			descs = synthDescs(rel, dir, bucket, string(src), m)
		case strings.HasSuffix(base, ".window.js"):
			src, _ := os.ReadFile(p)
			m := parseMeta(string(src))
			descs = synthDescs(rel, dir, bucket, string(src), m)
		case strings.HasSuffix(base, ".html"), strings.HasSuffix(base, ".htm"), strings.HasSuffix(base, ".xhtml"):
			src, _ := os.ReadFile(p)
			txt := string(src)
			if !strings.Contains(txt, "testharness.js") {
				return nil // a support/ref page, not a testharness test
			}
			descs = htmlDescs(rel, dir, bucket, txt)
		default:
			return nil
		}
		// A .headers sidecar (WPT's wptserve convention) carries the document's
		// response headers — most importantly Content-Security-Policy, which the
		// engine enforces from Env.ResponseHeaders. Without this, header-based CSP
		// tests would all fall to (d); with it they exercise the real enforcement.
		if hdr := readHeaders(root, rel); hdr != nil {
			for i := range descs {
				descs[i].Headers = hdr
			}
		}
		out = append(out, descs...)
		return nil
	})
	return out, nonWindow, err
}

// synthDescs builds descriptors for a .any.js/.window.js test: one per variant,
// each a synthesized HTML wrapper loading testharness + our reporter + META
// script= includes + the test file.
func synthDescs(rel, dir string, bucket bucketKind, src string, m meta) []testDesc {
	// The synthetic page path mirrors WPT's own generated wrapper name
	// (foo.any.js -> foo.any.html) so relative resource resolution matches.
	pagePath := "/" + strings.TrimSuffix(rel, ".js") + ".html"
	testSrcName := path.Base(rel)
	html := synthWrapper(m.scripts, testSrcName)
	def, long := budgets()
	budget := def
	if m.longTimeout {
		budget = long
	}
	variants := m.variants
	if len(variants) == 0 {
		variants = []string{""}
	}
	var descs []testDesc
	for _, v := range variants {
		d := testDesc{
			Dir: dir, Bucket: bucket, RelPath: rel,
			URLPath: pagePath + v, Variant: v, HTML: html, Budget: budget,
		}
		applyPreskip(&d, src)
		descs = append(descs, d)
	}
	return descs
}

// htmlDescs builds descriptors for a .html test: one per <meta name=variant>.
func htmlDescs(rel, dir string, bucket bucketKind, src string) []testDesc {
	def, long := budgets()
	budget := def
	if strings.Contains(src, "timeout=long") || strings.Contains(src, `content="long"`) {
		budget = long
	}
	var variants []string
	for _, mm := range variantMetaRe.FindAllStringSubmatch(src, -1) {
		variants = append(variants, mm[1])
	}
	if len(variants) == 0 {
		variants = []string{""}
	}
	var descs []testDesc
	for _, v := range variants {
		d := testDesc{
			Dir: dir, Bucket: bucket, RelPath: rel,
			URLPath: "/" + rel + v, Variant: v, HTML: src, Budget: budget,
		}
		applyPreskip(&d, src)
		descs = append(descs, d)
	}
	return descs
}

// applyPreskip flags a descriptor as harness-limited (d) when its source trips a
// capability the runner can't provide.
func applyPreskip(d *testDesc, src string) {
	for _, sig := range harnessLimitSignals {
		if strings.Contains(src, sig) {
			d.Preskip = "harness"
			d.SkipMsg = "needs " + sig
			return
		}
	}
}

// synthWrapper renders the HTML page for a .js test: charset, testharness, our
// reporter, a #log sink, then the META script= includes and the test file. Include
// and test src are bare/relative and resolve against the page's directory.
func synthWrapper(includes []string, testSrc string) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<meta charset=\"utf-8\">\n")
	b.WriteString(`<script src="/resources/testharness.js"></script>` + "\n")
	b.WriteString(`<script src="/resources/testharnessreport.js"></script>` + "\n")
	b.WriteString(`<div id="log"></div>` + "\n")
	for _, inc := range includes {
		b.WriteString(`<script src="` + inc + `"></script>` + "\n")
	}
	b.WriteString(`<script src="` + testSrc + `"></script>` + "\n")
	return b.String()
}

// meta holds the parsed `// META:` directives from a .js test's leading comments.
type meta struct {
	globals     []string
	scripts     []string
	variants    []string
	longTimeout bool
}

func (m meta) hasWindow() bool {
	if len(m.globals) == 0 {
		return true // default global set is window,dedicatedworker
	}
	for _, g := range m.globals {
		if g == "window" || g == "default" {
			return true
		}
	}
	return false
}

var metaLineRe = regexp.MustCompile(`^//\s*META:\s*([a-zA-Z]+)=(.*)$`)

// parseMeta reads the contiguous leading `// META:` comment block of a .js test.
func parseMeta(src string) meta {
	var m meta
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		mm := metaLineRe.FindStringSubmatch(t)
		if mm == nil {
			if strings.HasPrefix(t, "//") {
				continue // a plain comment; keep scanning the header block
			}
			break // first real code line ends the META block
		}
		key, val := mm[1], strings.TrimSpace(mm[2])
		switch key {
		case "global":
			for _, g := range strings.Split(val, ",") {
				m.globals = append(m.globals, strings.TrimSpace(g))
			}
		case "script":
			m.scripts = append(m.scripts, val)
		case "variant":
			m.variants = append(m.variants, val)
		case "timeout":
			if val == "long" {
				m.longTimeout = true
			}
		}
	}
	return m
}

// isTestFile reports whether a basename is a runnable testharness test form.
func isTestFile(base string) bool {
	return strings.HasSuffix(base, ".any.js") || strings.HasSuffix(base, ".window.js") ||
		strings.HasSuffix(base, ".html") || strings.HasSuffix(base, ".htm") || strings.HasSuffix(base, ".xhtml")
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}
