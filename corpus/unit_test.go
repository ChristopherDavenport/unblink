//go:build corpus

package corpus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCapture lays down a synthetic capture the way scripts/corpus does.
func writeCapture(t *testing.T, a archive, bodies map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bodies"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range a.Entries {
		if b, ok := bodies[e.URL]; ok {
			sum := sha256.Sum256([]byte(b))
			e.Body, e.Size = hex.EncodeToString(sum[:]), len(b)
			if err := os.WriteFile(filepath.Join(dir, "bodies", e.Body), []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, _ := json.Marshal(a)
	if err := os.WriteFile(filepath.Join(dir, "archive.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func get(t *testing.T, r *replay, method, u string) (*http.Response, string, error) {
	t.Helper()
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.RoundTrip(req)
	if err != nil {
		return nil, "", err
	}
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b), nil
}

func TestArchiveRoundTripAndReplayTiers(t *testing.T) {
	a := archive{Version: archiveVersion, ID: "x", URL: "https://a.test/", Entries: []*entry{
		{Method: "GET", URL: "https://a.test/", Status: 200, Text: true, MIME: "text/html",
			Headers: map[string][]string{"content-type": {"text/html; charset=shift_jis"}, "content-encoding": {"br"}, "content-length": {"3"}}},
		{Method: "GET", URL: "https://a.test/app.js?v=3&_=1700000000000", Status: 200},
		{Method: "GET", URL: "https://a.test/api?page=1&sid=aaa", Status: 200},
		{Method: "GET", URL: "https://a.test/poll", Status: 200},
		{Method: "GET", URL: "https://a.test/poll", Status: 201},
		{Method: "GET", URL: "https://a.test/dead", Failed: "net::ERR_FAILED"},
		{Method: "GET", URL: "https://a.test/redir", Status: 302, Headers: map[string][]string{"location": {"/"}}},
	}}
	dir := writeCapture(t, a, map[string]string{
		"https://a.test/": "<p>hi</p>",
		"https://a.test/app.js?v=3&_=1700000000000": "js",
		"https://a.test/api?page=1&sid=aaa":         "api",
	})
	loaded, err := loadArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := newReplay(dir, loaded)

	resp, body, err := get(t, r, "GET", "https://a.test/#frag")
	if err != nil || body != "<p>hi</p>" {
		t.Fatalf("exact: %q %v", body, err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "utf-8") || strings.Contains(ct, "shift_jis") {
		t.Errorf("text body must be presented as utf-8, got %q", ct)
	}
	if resp.Header.Get("Content-Encoding") != "" || resp.ContentLength != int64(len(body)) {
		t.Errorf("encoding/length headers must describe the stored body: %v %d", resp.Header, resp.ContentLength)
	}
	if _, body, err = get(t, r, "GET", "https://a.test/app.js?v=3&_=1800000000000"); err != nil || body != "js" {
		t.Fatalf("cachebust: %q %v", body, err)
	}
	if _, body, err = get(t, r, "GET", "https://a.test/api?page=1&sid=bbb"); err != nil || body != "api" {
		t.Fatalf("path fallback: %q %v", body, err)
	}
	// A different parameter SET is a different resource: stays a miss.
	if _, _, err = get(t, r, "GET", "https://a.test/api?page=1"); err == nil {
		t.Fatal("differing param names must miss")
	}
	if _, _, err = get(t, r, "GET", "data:text/javascript,1"); err == nil {
		t.Fatal("non-http URL must fail")
	}
	if _, _, err = get(t, r, "POST", "https://a.test/"); err == nil {
		t.Fatal("method must match")
	}
	if _, _, err = get(t, r, "GET", "https://a.test/dead"); err == nil {
		t.Fatal("capture-failed request must fail in replay")
	}
	// Repeats replay in capture order, the last response sticky.
	for i, want := range []int{200, 201, 201} {
		resp, _, err := get(t, r, "GET", "https://a.test/poll")
		if err != nil || resp.StatusCode != want {
			t.Fatalf("poll %d: got %v %v, want %d", i, resp, err, want)
		}
	}
	if resp, _, err := get(t, r, "GET", "https://a.test/redir"); err != nil || resp.StatusCode != 302 || resp.Header.Get("Location") != "/" {
		t.Fatalf("redirect hop: %v %v", resp, err)
	}

	s := r.snapshot()
	got := [6]int{s.Requests, s.Exact, s.CacheBust, s.PathOnly, s.CaptureFailed, s.Unmatched}
	if want := [6]int{10, 5, 1, 1, 1, 2}; got != want {
		t.Errorf("stats (requests, exact, cachebust, path, capture-failed, unmatched) = %v, want %v", got, want)
	}
	if s.NonHTTP != 1 {
		t.Errorf("a data: URL is the engine's request, not a replay miss: %+v", s)
	}
	if len(s.Misses) != 2 || s.Misses[0].URL != "https://a.test/api?page=1" {
		t.Errorf("misses = %+v", s.Misses)
	}
}

func TestArchiveRejectsHostileIndex(t *testing.T) {
	cases := map[string]archive{
		"traversal": {Version: archiveVersion, Entries: []*entry{{Method: "GET", URL: "https://a.test/", Body: "../../../etc/passwd"}}},
		"absolute":  {Version: archiveVersion, Entries: []*entry{{Method: "GET", URL: "https://a.test/", Body: "/etc/passwd"}}},
		"missing":   {Version: archiveVersion, Entries: []*entry{{Method: "GET", URL: "https://a.test/", Body: strings.Repeat("a", 64)}}},
		"version":   {Version: archiveVersion + 1},
		"null":      {Version: archiveVersion, Entries: []*entry{nil}},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.MkdirAll(filepath.Join(dir, "bodies"), 0o755)
			raw, _ := json.Marshal(a)
			_ = os.WriteFile(filepath.Join(dir, "archive.json"), raw, 0o644)
			if _, err := loadArchive(dir); err == nil {
				t.Fatal("hostile index accepted")
			}
		})
	}
	t.Run("symlink body", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dir, "bodies"), 0o755)
		name := strings.Repeat("b", 64)
		if err := os.Symlink("/etc/hostname", filepath.Join(dir, "bodies", name)); err != nil {
			t.Skip(err)
		}
		raw, _ := json.Marshal(archive{Version: archiveVersion, Entries: []*entry{{Method: "GET", URL: "https://a.test/", Body: name}}})
		_ = os.WriteFile(filepath.Join(dir, "archive.json"), raw, 0o644)
		if _, err := loadArchive(dir); err == nil {
			t.Fatal("symlinked body accepted")
		}
	})
	t.Run("oversize index", func(t *testing.T) {
		if _, err := readBounded(writeFile(t, strings.Repeat("x", 100)), 10); err == nil {
			t.Fatal("oversize file accepted")
		}
	})
}

func writeFile(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScoring(t *testing.T) {
	want := tokenize("The Quick, brown fox — jumps over the LAZY dog.")
	if got := strings.Join(want, " "); got != "the quick brown fox jumps over the lazy dog" {
		t.Fatalf("tokenize = %q", got)
	}
	if got := tokenize("日本語 text"); len(got) != 4 {
		t.Errorf("CJK must tokenize per rune: %q", got)
	}
	md := stripMarkdown("# The quick [brown fox](https://x.test/a?b=c \"t\") jumps ![over](i.png) the lazy dog https://junk.test/z")
	got := tokenize(md)
	if r := shingleRecall(want, got); r != 1 {
		t.Errorf("markdown syntax must not cost recall: %.3f (%q)", r, got)
	}
	if strings.Contains(md, "x.test") || strings.Contains(md, "junk.test") {
		t.Errorf("link targets must be stripped: %q", md)
	}
	// Right words, wrong places: token recall is fooled, shingle recall is not.
	shuffled := tokenize("dog lazy the over jumps fox brown quick the")
	if tokenRecall(want, shuffled) != 1 || shingleRecall(want, shuffled) != 0 {
		t.Errorf("shuffle: token=%.2f shingle=%.2f", tokenRecall(want, shuffled), shingleRecall(want, shuffled))
	}
	// A moved block costs only boundary shingles.
	a := tokenize("one two three four five six seven eight nine ten")
	moved := tokenize("six seven eight nine ten one two three four five")
	if r := shingleRecall(a, moved); r < 0.7 || r >= 1 {
		t.Errorf("moved block recall = %.3f", r)
	}
	if p := tokenPrecision(tokenize("alpha beta junk junk"), tokenize("alpha beta gamma")); p != 0.5 {
		t.Errorf("precision = %.3f", p)
	}
	if shingleRecall(nil, got) != 0 {
		t.Error("empty oracle must score 0")
	}
	if r, ok := ratio(3, 6); r != 0.5 || !ok {
		t.Errorf("ratio(3,6) = %v, %v", r, ok)
	}
	// A zero oracle count has no ratio: it must never enter a mean as a raw count.
	if _, ok := ratio(94, 0); ok {
		t.Error("ratio over a zero oracle count must not be ok")
	}
	if _, ok := ratio(0, 0); ok {
		t.Error("ratio(0,0) carries no information")
	}
	if m := missingShingles(tokenize("a b c d e"), tokenize("a b c x y"), 10); len(m) != 2 || m[0] != "b c d" {
		t.Errorf("missingShingles = %q", m)
	}
}

func TestClassify(t *testing.T) {
	ok := &oracle{DocStatus: 200, Title: "Docs", BodyText: "real content"}
	base := classifyInput{Recall: 0.2, OracleTokens: 500, Oracle: ok}
	with := func(f func(*classifyInput)) classifyInput { in := base; f(&in); return in }
	cases := []struct {
		name string
		in   classifyInput
		want string
	}{
		{"pass", with(func(in *classifyInput) { in.Recall = 0.9 }), bucketPass},
		{"pass despite errors", with(func(in *classifyInput) { in.Recall = 0.9; in.Errors = []string{"TypeError: x"} }), bucketPass},
		{"unusable oracle wins over a perfect score", with(func(in *classifyInput) { in.OracleUnusable = "capture got HTTP 403"; in.Recall = 1 }), bucketHarness},
		{"thin oracle", with(func(in *classifyInput) { in.OracleTokens = 5 }), bucketHarness},
		{"consequential loss wins over errors", with(func(in *classifyInput) {
			in.Replay.Unmatched, in.Replay.MissConsequential = 2, 2
			in.Errors = []string{"SyntaxError: bad"}
		}), bucketHarness},
		// Beacons and pixels cannot change the page: they must not excuse a miss.
		{"harmless loss does not excuse", with(func(in *classifyInput) {
			in.Replay.Unmatched, in.Replay.MissHarmless = 5, 5
			in.Errors = []string{"TypeError: el.animate is not a function"}
		}), bucketGap},
		{"harmless loss, no diagnostic", with(func(in *classifyInput) { in.Replay.Unmatched, in.Replay.MissHarmless = 5, 5 }), bucketUnclassified},
		{"pipeline error with consequential loss", with(func(in *classifyInput) {
			in.PipelineErr, in.Replay.MissConsequential = "boom", 1
		}), bucketHarness},
		{"syntax", with(func(in *classifyInput) { in.Errors = []string{"TypeError: a", "SyntaxError: Unexpected token at 3:1"} }), bucketCeiling},
		{"non-goal", with(func(in *classifyInput) { in.Errors = []string{"ReferenceError: WebSocket is not defined"} }), bucketByDesign},
		{"engine gap", with(func(in *classifyInput) { in.Errors = []string{"TypeError: el.animate is not a function"} }), bucketGap},
		{"shared with chromium", with(func(in *classifyInput) {
			in.Errors = []string{"TypeError: Cannot read properties of null (reading 'x')"}
			in.Oracle = &oracle{DocStatus: 200, Exceptions: []string{"Uncaught TypeError: Cannot read properties of null (reading 'x')"}}
		}), bucketUnclassified},
		{"silent miss", base, bucketUnclassified},
		{"pipeline error", with(func(in *classifyInput) { in.PipelineErr = "boom" }), bucketUnclassified},
	}
	for _, c := range cases {
		if got, reason := classify(c.in); got != c.want {
			t.Errorf("%s: bucket %q (%s), want %q", c.name, got, reason, c.want)
		}
	}
}

func TestAggregateExcludesHarnessPages(t *testing.T) {
	rep := aggregate([]pageResult{
		{ID: "a", Cells: []string{"framework:react"}, Bucket: bucketPass, Recall: 1, Precision: 1},
		{ID: "b", Cells: []string{"framework:react", "shape:docs"}, Bucket: bucketGap, Recall: 0.5, Precision: 1},
		{ID: "c", Cells: []string{"framework:react"}, Bucket: bucketHarness, Recall: 0},
	}, []string{"z"})
	var react cellRow
	for _, c := range rep.Cells {
		if c.Cell == "framework:react" {
			react = c
		}
	}
	if react.Pages != 3 || react.Scored != 2 || react.PassRate != 0.5 || react.MeanRecall != 0.75 || react.Harn != 1 {
		t.Errorf("react row = %+v", react)
	}
	if rep.Overall.Scored != 2 || len(rep.NotCapture) != 1 {
		t.Errorf("overall = %+v", rep.Overall)
	}
	var sb strings.Builder
	writeScorecard(&sb, rep)
	if !strings.Contains(sb.String(), "framework:react") || !strings.Contains(sb.String(), "[a] b") {
		t.Errorf("scorecard missing rows:\n%s", sb.String())
	}
}

func TestCheckOracle(t *testing.T) {
	long := strings.Repeat("word ", 400)
	yes, no := true, false
	cases := []struct {
		name     string
		o        oracle
		unusable bool
		warn     string // substring of an expected warning, "" for none
	}{
		{"healthy", oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: long, MainSelector: "main", LinksVisible: 9, LinksTotal: 9}, false, ""},
		{"http error", oracle{DocStatus: 403, BodyText: long}, true, ""},
		{"challenge", oracle{DocStatus: 200, Title: "Just a moment...", BodyText: long}, true, ""},
		// The solid-docs capture: body innerText empty, main non-empty, no visible link.
		{"body not rendered", oracle{DocStatus: 200, Settled: true, BodyText: "", MainText: long, LinksTotal: 124}, true, ""},
		{"body not rendered (recorded)", oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: long, BodyRendered: &no}, true, ""},
		{"body rendered (recorded)", oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: long, MainSelector: "main", BodyRendered: &yes}, false, ""},
		{"nothing visible", oracle{DocStatus: 200, Settled: true, BodyText: long, LinksTotal: 40}, true, ""},
		{"few links, none visible", oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: long, MainSelector: "main", LinksTotal: 3}, false, ""},
		{"unsettled", oracle{DocStatus: 200, BodyText: long, MainText: long, MainSelector: "main"}, false, "hard cap"},
		{"tiny main", oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: strings.Repeat("w ", 45), MainSelector: "main"}, false, "wrong element"},
		{"thin main falls back", oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: "hi", MainSelector: "main"}, false, "too thin"},
		{"left the site", oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: long, MainSelector: "main",
			URL: "https://a.example.com/x", FinalURL: "https://login.other.org/"}, false, "another site"},
	}
	for _, c := range cases {
		unusable, warnings := checkOracle(&c.o, nil)
		if (unusable != "") != c.unusable {
			t.Errorf("%s: unusable = %q, want unusable=%v", c.name, unusable, c.unusable)
		}
		joined := strings.Join(warnings, " | ")
		if c.warn == "" && !c.unusable && len(warnings) != 0 {
			t.Errorf("%s: unexpected warnings %q", c.name, joined)
		}
		if c.warn != "" && !strings.Contains(joined, c.warn) {
			t.Errorf("%s: warnings %q lack %q", c.name, joined, c.warn)
		}
	}
	a := &archive{Entries: []*entry{{Type: "Script", Failed: inFlight}, {Type: "Document", Failed: inFlight}}}
	ok := oracle{DocStatus: 200, Settled: true, BodyText: long, MainText: long, MainSelector: "main"}
	if _, w := checkOracle(&ok, a); len(w) != 1 || !strings.Contains(w[0], "1 script/data") {
		t.Errorf("in-flight script must warn once, iframes not at all: %q", w)
	}
}

func TestReplayLossAccounting(t *testing.T) {
	a := archive{Version: archiveVersion, ID: "x", URL: "https://www.site.example.com/", Entries: []*entry{
		{Method: "GET", URL: "https://www.site.example.com/", Status: 200, Text: true, MIME: "text/html"},
		{Method: "POST", URL: "https://api.site.example.com/q", Status: 200, ReqBody: sha("two")},
		{Method: "POST", URL: "https://api.site.example.com/q", Status: 200, ReqBody: sha("one")},
		// Chromium aborted these after the headers: the capture has no body.
		{Method: "GET", URL: "https://www.site.example.com/rsc", Status: 200, Type: "Fetch", Omitted: "load failed after headers: net::ERR_ABORTED"},
		{Method: "POST", URL: "https://www.google-analytics.com/g/collect", Status: 204, Type: "Fetch", Omitted: "load failed after headers: net::ERR_ABORTED"},
		{Method: "GET", URL: "https://www.site.example.com/logo.png", Status: 200, Type: "Image", Omitted: "resource type Image"},
		{Method: "OPTIONS", URL: "https://api.site.example.com/q", Status: 200, Type: "Preflight", Omitted: "body unavailable: x"},
		{Method: "GET", URL: "https://www.site.example.com/data?page=1", Status: 200},
	}}
	dir := writeCapture(t, a, map[string]string{
		"https://www.site.example.com/": "<p>hi</p>",
	})
	// Give the two POSTs distinguishable bodies.
	for i, body := range []string{"TWO", "ONE"} {
		sum := sha256.Sum256([]byte(body))
		a.Entries[1+i].Body = hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(dir, "bodies", a.Entries[1+i].Body), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := newReplay(dir, &a)
	do := func(method, u, body, dest string) (string, error) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, u, rd)
		if err != nil {
			t.Fatal(err)
		}
		if dest != "" {
			req.Header.Set("Sec-Fetch-Dest", dest)
		}
		resp, err := r.RoundTrip(req)
		if err != nil {
			return "", err
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}

	// Host and scheme case, and a default port, are not a different URL.
	if got, err := do("GET", "HTTPS://WWW.Site.Example.com:443/", "", "document"); err != nil || got != "<p>hi</p>" {
		t.Errorf("case-insensitive host: %q, %v", got, err)
	}
	// A recorded request-body hash picks the exchange, whatever the capture order.
	if got, _ := do("POST", "https://api.site.example.com/q", "one", "empty"); got != "ONE" {
		t.Errorf("request body must select its exchange: got %q", got)
	}
	if got, _ := do("POST", "https://api.site.example.com/q", "two", "empty"); got != "TWO" {
		t.Errorf("request body must select its exchange: got %q", got)
	}
	// A body the capture lacks is a failure, never an empty 200.
	if _, err := do("GET", "https://www.site.example.com/rsc", "", "empty"); err == nil || !strings.Contains(err.Error(), "no body") {
		t.Errorf("aborted-at-capture fetch must fail: %v", err)
	}
	// ...but a 204 beacon, a dropped image and a preflight have no body to lose.
	for _, c := range [][2]string{
		{"POST", "https://www.google-analytics.com/g/collect"},
		{"GET", "https://www.site.example.com/logo.png"},
		{"OPTIONS", "https://api.site.example.com/q"},
	} {
		if _, err := do(c[0], c[1], "", ""); err != nil {
			t.Errorf("%s %s must be answered bodiless: %v", c[0], c[1], err)
		}
	}
	_, _ = do("GET", "https://www.site.example.com/chunk-abc.js", "", "script")       // never requested by Chromium
	_, _ = do("GET", "https://www.site.example.com/assets/x.js", "", "empty")         // fetch()-ed code is still code
	_, _ = do("GET", "https://www.site.example.com/data?page=2&extra=1", "", "empty") // same path, unreconcilable query
	_, _ = do("POST", "https://www.site.example.com/data", "x", "empty")              // same path, other method
	_, _ = do("POST", "https://stats.g.doubleclick.net/j/collect", "x", "empty")      // beacon
	_, _ = do("GET", "https://cdn.other.net/pixel.gif", "", "image")                  // pixel
	_, _ = do("GET", "https://www.site.example.com/fonts/a.woff2", "", "")            // font by extension

	s := r.snapshot()
	if s.Unmatched != 7 || s.CaptureNoBody != 1 {
		t.Fatalf("unmatched=%d no-body=%d, want 7 and 1", s.Unmatched, s.CaptureNoBody)
	}
	if s.MissConsequential != 5 || s.MissHarmless != 3 {
		t.Errorf("consequential=%d harmless=%d, want 5 and 3 (%v)", s.MissConsequential, s.MissHarmless, s.MissKinds)
	}
	if s.MissFirstParty != 6 {
		t.Errorf("first-party = %d, want 6", s.MissFirstParty)
	}
	wantKinds := map[string]int{kindScript: 2, kindFetch: 3, kindAnalytics: 1, kindImage: 1, kindFont: 1}
	for k, n := range wantKinds {
		if s.MissKinds[k] != n {
			t.Errorf("kind %s = %d, want %d (%v)", k, s.MissKinds[k], n, s.MissKinds)
		}
	}
	if s.MissNear["never"] != 5 || s.MissNear["query"] != 1 || s.MissNear["method"] != 1 || s.MissNear["nobody"] != 1 {
		t.Errorf("near = %v", s.MissNear)
	}
	if s.MissMethods["GET"] != 6 || s.MissMethods["POST"] != 2 {
		t.Errorf("methods = %v", s.MissMethods)
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestVerifyCells(t *testing.T) {
	a := archive{Version: archiveVersion, ID: "x", URL: "https://docs.site.example.com/", Entries: []*entry{
		{Method: "GET", URL: "https://docs.site.example.com/", Status: 200, Type: "Document"},
		{Method: "GET", URL: "https://docs.site.example.com/_next/static/chunks/main.js", Status: 200, Type: "Script"},
		{Method: "GET", URL: "https://docs.site.example.com/api/items", Status: 200, Type: "Fetch"},
		{Method: "POST", URL: "https://www.google-analytics.com/g/collect", Status: 200, Type: "Fetch"},
		// An ad iframe's document must not count as the page's own HTML.
		{Method: "GET", URL: "https://ads.other.net/frame.html", Status: 200, Type: "Document"},
	}}
	dir := writeCapture(t, a, map[string]string{
		"https://docs.site.example.com/":                            `<html><body><div id="__next"><script id="__NEXT_DATA__">{}</script><noscript>x</noscript></div></body></html>`,
		"https://docs.site.example.com/_next/static/chunks/main.js": `self.webpackChunk_N_E=[];throw Error("https://react.dev/errors/"+e)`,
		"https://docs.site.example.com/api/items":                   `[1]`,
		"https://www.google-analytics.com/g/collect":                `ok`,
		"https://ads.other.net/frame.html":                          `<div x-data="{}"><astro-island></astro-island></div>`,
	})
	o := &oracle{CustomElements: 0, ShadowRoots: 2, Charset: "UTF-8", DOMNodes: 400, BodyText: "plain english text"}
	ev := gatherEvidence(dir, &a, o)
	if got := strings.Join(ev.detected(), ","); got != "next,noscript,react,webpack" {
		t.Errorf("detected = %s", got)
	}
	if ev.dataFetches != 1 {
		t.Errorf("dataFetches = %d, want 1 (analytics excluded)", ev.dataFetches)
	}
	cells := []string{
		"hydration:next", "framework:react", "bundler:webpack", "bundler:vite", "framework:alpine",
		"hydration:astro-islands", "hydration:client-only", "components:shadow-dom", "components:custom-elements",
		"arrival:fetch-after-load", "shape:non-utf8", "shape:large-dom", "shape:docs", "shape:noscript", "shape:cjk",
	}
	want := map[string]string{
		"hydration:next":             verdictSupported,
		"framework:react":            verdictSupported,
		"bundler:webpack":            verdictSupported,
		"bundler:vite":               verdictNoEvidence, // markers can be minified away
		"framework:alpine":           verdictContradicted,
		"hydration:astro-islands":    verdictContradicted,
		"hydration:client-only":      verdictContradicted, // static recall 0.95
		"components:shadow-dom":      verdictSupported,
		"components:custom-elements": verdictContradicted,
		"arrival:fetch-after-load":   verdictSupported,
		"shape:non-utf8":             verdictContradicted,
		"shape:large-dom":            verdictContradicted,
		"shape:docs":                 verdictUnchecked,
		"shape:noscript":             verdictSupported,
		"shape:cjk":                  verdictContradicted,
	}
	for _, c := range verifyCells(cells, ev, 0.95, true) {
		if c.Verdict != want[c.Cell] {
			t.Errorf("%s: %s (%s), want %s", c.Cell, c.Verdict, c.Evidence, want[c.Cell])
		}
	}
	// An SSR label needs the served HTML to carry the content.
	if c := verifyCell("hydration:next", ev, 0.05, true); c.Verdict != verdictContradicted {
		t.Errorf("next with empty served HTML: %s (%s)", c.Verdict, c.Evidence)
	}
	if c := verifyCell("hydration:client-only", ev, 0.05, true); c.Verdict != verdictSupported {
		t.Errorf("client-only with empty served HTML: %s", c.Verdict)
	}
	if c := verifyCell("hydration:client-only", ev, 0, false); c.Verdict != verdictUnchecked {
		t.Errorf("client-only on an unscored page must not be guessed: %s", c.Verdict)
	}
	if got := unclaimedStacks([]string{"shape:docs"}, ev); strings.Join(got, ",") != "next,react,webpack" {
		t.Errorf("unclaimed = %v", got)
	}
}

func TestAggregateLinkRatioAndLossFlags(t *testing.T) {
	rep := aggregate([]pageResult{
		{ID: "a", Cells: []string{"c:x"}, Bucket: bucketPass, Scored: true, Recall: 0.9, StaticRecall: 0.9, LinkRatio: 1.0, LinkRatioOK: true, PassWithLoss: true},
		// Chromium saw no visible link: this page has no ratio and must not move the mean.
		{ID: "b", Cells: []string{"c:x"}, Bucket: bucketPass, Scored: true, Recall: 0.9, StaticRecall: 0.1, LinkRatio: 0, LinkRatioOK: false},
		{ID: "c", Cells: []string{"c:x"}, Bucket: bucketHarness, Reason: "oracle unusable: capture got HTTP 403",
			OracleWarnings: nil, CellChecks: []cellCheck{{Cell: "c:x", Verdict: verdictContradicted, Evidence: "nope"}}},
		{ID: "d", Cells: []string{"c:x"}, Bucket: bucketPass, Scored: true, Recall: 1, StaticRecall: 1, LinkRatio: 0.5, LinkRatioOK: true,
			OracleWarnings: []string{"capture hit the hard cap"}},
	}, nil)
	row := rep.Cells[0]
	if row.LinkN != 2 || row.MedianLinkRate != 0.75 {
		t.Errorf("link median = %.3f over %d pages, want 0.75 over 2", row.MedianLinkRate, row.LinkN)
	}
	if m := median([]float64{1, 57.5, 0.9}); m != 1 {
		t.Errorf("median must shrug off an outlier: %v", m)
	}
	if row.PassWithLoss != 1 || row.RenderGain != 1 {
		t.Errorf("pass-with-loss=%d render-gain=%d, want 1 and 1", row.PassWithLoss, row.RenderGain)
	}
	if len(rep.Recapture) != 2 || rep.Recapture[0].ID != "c" || rep.Recapture[1].ID != "d" {
		t.Errorf("recapture = %+v", rep.Recapture)
	}
	if rep.ManifestChecks[verdictContradicted] != 1 || len(rep.ManifestIssues) != 1 {
		t.Errorf("manifest issues = %+v", rep.ManifestIssues)
	}
	rep.Pages[0].Spread = &runSpread{Runs: 3, Unstable: true, RecallMin: 0.001, RecallMax: 0.9, RequestsMin: 13, RequestsMax: 353,
		Buckets: map[string]int{bucketPass: 2, bucketHarness: 1}}
	var sb strings.Builder
	writeScorecard(&sb, rep)
	for _, want := range []string{"unscored", "captures to redo", "contradicted", "passes with consequential replay losses", "UNSTABLE      recall 0.001..0.900  requests 13..353"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("scorecard lacks %q:\n%s", want, sb.String())
		}
	}
}
