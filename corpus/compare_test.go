//go:build corpus

package corpus

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// proxyClient is an HTTP client that reaches everything through p, the way a
// browser started with --proxy-server --ignore-certificate-errors does.
func proxyClient(p *Proxy) *http.Client {
	pu, _ := url.Parse("http://" + p.Addr())
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(pu),
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // the proxy's CA is throwaway by design
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func proxyFixture(t *testing.T) (*Proxy, *replay) {
	t.Helper()
	a := archive{Version: archiveVersion, ID: "x", URL: "https://a.test/", Entries: []*entry{
		{Method: "GET", URL: "https://a.test/", Status: 200, Text: true, MIME: "text/html",
			Headers: map[string][]string{"content-type": {"text/html; charset=shift_jis"}, "content-encoding": {"br"},
				"alt-svc": {`h3=":443"`}, "x-kept": {"yes"}}},
		{Method: "GET", URL: "http://plain.test/page", Status: 200, Text: true, MIME: "text/plain"},
		{Method: "GET", URL: "https://a.test/app.js?v=3&_=1700000000000", Status: 200},
		{Method: "POST", URL: "https://api.a.test/q", Status: 200, ReqBody: sha("first")},
		{Method: "POST", URL: "https://api.a.test/q", Status: 200, ReqBody: sha("second")},
		{Method: "GET", URL: "https://a.test/redir", Status: 302, Headers: map[string][]string{"location": {"/"}}},
		{Method: "GET", URL: "https://a.test/dead", Failed: "net::ERR_FAILED"},
	}}
	// writeCapture keys bodies by URL, so the two POSTs get theirs by hand.
	dir := writeCapture(t, a, map[string]string{
		"https://a.test/":                           "<p>hi</p>",
		"http://plain.test/page":                    "plain body",
		"https://a.test/app.js?v=3&_=1700000000000": "js",
	})
	for i, body := range []string{"answer one", "answer two"} {
		e := a.Entries[3+i]
		e.Body, e.Size = sha(body), len(body)
		if err := os.WriteFile(filepath.Join(dir, "bodies", e.Body), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(a)
	if err := os.WriteFile(filepath.Join(dir, "archive.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	r := newReplay(dir, loaded)
	p.SetRoundTripper(r)
	return p, r
}

func fetchVia(t *testing.T, c *http.Client, method, u, body string) (*http.Response, string, error) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b), nil
}

// TestProxyServesArchiveThroughTheReplayMatcher: a browser behind the proxy
// gets exactly what the in-process replay would answer — over a terminated
// CONNECT tunnel and over plain HTTP, with every matcher tier — and a request
// the archive cannot answer is a network error, never a made-up response.
func TestProxyServesArchiveThroughTheReplayMatcher(t *testing.T) {
	p, r := proxyFixture(t)
	c := proxyClient(p)

	resp, body, err := fetchVia(t, c, "GET", "https://a.test/", "")
	if err != nil || body != "<p>hi</p>" {
		t.Fatalf("https document: %q, %v", body, err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "utf-8") {
		t.Errorf("text body must be served as UTF-8, got Content-Type %q", ct)
	}
	if resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Alt-Svc") != "" {
		t.Errorf("stored-body and connection headers leaked: %v", resp.Header)
	}
	if resp.Header.Get("X-Kept") != "yes" {
		t.Errorf("ordinary response headers must pass through: %v", resp.Header)
	}

	if _, body, err = fetchVia(t, c, "GET", "http://plain.test/page", ""); err != nil || body != "plain body" {
		t.Errorf("plain http: %q, %v", body, err)
	}
	// Host case and a cache-busting parameter are tolerated by the matcher.
	if _, body, err = fetchVia(t, c, "GET", "https://A.TEST/app.js?v=3&_=1800000000000", ""); err != nil || body != "js" {
		t.Errorf("cachebust tier through the proxy: %q, %v", body, err)
	}
	// Two POSTs to one endpoint are told apart by request body, in either order.
	if _, body, err = fetchVia(t, c, "POST", "https://api.a.test/q", "second"); err != nil || body != "answer two" {
		t.Errorf("POST matched by body: %q, %v", body, err)
	}
	if _, body, err = fetchVia(t, c, "POST", "https://api.a.test/q", "first"); err != nil || body != "answer one" {
		t.Errorf("POST matched by body: %q, %v", body, err)
	}
	if resp, _, err = fetchVia(t, c, "GET", "https://a.test/redir", ""); err != nil || resp.StatusCode != 302 || resp.Header.Get("Location") != "/" {
		t.Errorf("redirect hop must be served as recorded: %v, %v", resp, err)
	}

	for _, u := range []string{"https://a.test/not-captured.js", "https://elsewhere.test/", "http://plain.test/other", "https://a.test/dead"} {
		if resp, body, err := fetchVia(t, c, "GET", u, ""); err == nil {
			t.Errorf("%s: want a network error, got %d %q", u, resp.StatusCode, body)
		}
	}
	st := r.snapshot()
	if st.Unmatched != 3 || st.CaptureFailed != 1 {
		t.Errorf("unmatched=%d capture_failed=%d, want 3 and 1", st.Unmatched, st.CaptureFailed)
	}
	if st.Exact != 5 || st.CacheBust != 1 {
		t.Errorf("exact=%d cachebust=%d, want 5 and 1", st.Exact, st.CacheBust)
	}
}

// TestProxyRefusesBrowserHousekeepingWithoutCountingIt: Chromium's own update
// and safe-browsing traffic is not the page's; it must neither be answered nor
// show up as the page's replay misses.
func TestProxyRefusesBrowserHousekeepingWithoutCountingIt(t *testing.T) {
	p, r := proxyFixture(t)
	c := proxyClient(p)
	for _, u := range []string{"https://update.googleapis.com/service/update2/json", "http://clients2.google.com/time/1/current"} {
		if _, _, err := fetchVia(t, c, "GET", u, ""); err == nil {
			t.Errorf("%s: browser housekeeping must be refused", u)
		}
	}
	// A fetch the browser makes on its own account carries Sec-Fetch-Site: none
	// without being a navigation, whatever its host.
	req, _ := http.NewRequest("GET", "https://www.example.test/async/thing", nil)
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		t.Error("a browser-initiated fetch must be refused")
	}
	if got := p.Background(); got != 3 {
		t.Errorf("Background() = %d, want 3", got)
	}
	// The top-level navigation is also Sec-Fetch-Site: none — and is the page's.
	nav, _ := http.NewRequest("GET", "https://a.test/", nil)
	nav.Header.Set("Sec-Fetch-Site", "none")
	nav.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := c.Do(nav)
	if err != nil {
		t.Fatalf("the navigation itself must be answered: %v", err)
	}
	resp.Body.Close()
	if st := r.snapshot(); st.Requests != 1 || st.Exact != 1 {
		t.Errorf("navigation did not reach the replay: %+v", st)
	}
	r2 := newReplay(r.dir, &archive{Version: archiveVersion, URL: "https://a.test/"})
	p.SetRoundTripper(r2)
	man, _ := http.NewRequest("GET", "https://a.test/site.webmanifest", nil)
	man.Header.Set("Sec-Fetch-Site", "same-origin")
	man.Header.Set("Sec-Fetch-Dest", "manifest")
	if resp, err := c.Do(man); err == nil {
		resp.Body.Close()
		t.Error("an uncaptured manifest is still a miss")
	}
	if st := r2.snapshot(); st.Unmatched != 1 || st.MissConsequential != 0 || st.MissHarmless != 1 {
		t.Errorf("a lost web manifest is harmless: %+v", st)
	}
	p.SetRoundTripper(r)
	if _, _, err := fetchVia(t, c, "GET", "https://update.googleapis.com/x", ""); err == nil {
		t.Error("browser housekeeping must be refused")
	}
	if st := r.snapshot(); st.Requests != 1 || st.Unmatched != 0 {
		t.Errorf("housekeeping reached the replay: %+v", st)
	}
	if got := p.Background(); got != 1 {
		t.Errorf("Background() after re-selecting = %d, want 1", got)
	}
	p.SetRoundTripper(nil)
	if _, _, err := fetchVia(t, c, "GET", "https://a.test/", ""); err == nil {
		t.Error("with no replay selected every request must be refused")
	}
	if p.Background() != 0 {
		t.Error("SetRoundTripper must reset the housekeeping count")
	}
}

// compareRoot lays down one synthetic capture under a real manifest id, so the
// exported comparison surface can be exercised without any real capture.
func compareRoot(t *testing.T, html string, o oracle) (root string, id string) {
	t.Helper()
	pages, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	mp := pages[0]
	a := archive{Version: archiveVersion, ID: mp.ID, URL: mp.URL, Entries: []*entry{
		{Method: "GET", URL: mp.URL, Status: 200, Text: true, MIME: "text/html", Type: "Document",
			Headers: map[string][]string{"content-type": {"text/html"}}},
	}}
	src := writeCapture(t, a, map[string]string{mp.URL: html})
	root = t.TempDir()
	dst := filepath.Join(root, mp.ID)
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	o.URL, o.FinalURL, o.DocStatus = mp.URL, mp.URL, 200
	raw, _ := json.Marshal(o)
	if err := os.WriteFile(filepath.Join(dst, "oracle.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, mp.ID
}

func TestComparePagesScoreAndUnblinkRun(t *testing.T) {
	words := strings.Repeat("the quick brown fox jumps over the lazy dog near the quiet river bank ", 12)
	html := `<!doctype html><html><head><title>T</title></head><body><nav><a href="/a">Home</a></nav><main><h1>Heading</h1><p>` +
		words + `</p></main></body></html>`
	root, id := compareRoot(t, html, oracle{
		BodyText: "Home\nHeading\n" + words, MainText: "Heading\n" + words, MainSelector: "main",
		LinksTotal: 1, LinksVisible: 1, Settled: true,
	})
	pages, _, err := ComparePages(root, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || !pages[0].Scorable() || pages[0].OracleSource != "main" {
		t.Fatalf("pages = %+v", pages)
	}
	p := pages[0]

	// The scorer is the one runPage uses: same tokens, same shingles.
	want := tokenize("Heading\n" + words)
	if got := p.Score("# Heading\n\n" + words + " [x](https://example.test/y)"); !got.Scored ||
		got.Recall != shingleRecall(want, tokenize(stripMarkdown("# Heading\n\n"+words+" [x](https://example.test/y)"))) || got.Recall < 0.99 {
		t.Errorf("Score = %+v", got)
	}
	if got := p.Score("nothing relevant at all"); got.Recall != 0 || got.Precision != 0 {
		t.Errorf("unrelated text scored %+v", got)
	}
	if got := p.Score("Just a moment... verifying you are human"); !got.Blocked {
		t.Errorf("challenge text not flagged: %+v", got)
	}

	for _, mode := range []string{"default", "article", "full"} {
		r := RunUnblink(context.Background(), p, mode)
		if r.Err != "" {
			t.Fatalf("%s: %s", mode, r.Err)
		}
		if s := p.Score(r.Text); s.Recall < PassRecall {
			t.Errorf("%s: recall %.3f on a page served whole from the archive\n%s", mode, s.Recall, r.Text)
		}
		if r.Calls < 1 || r.Replay.Requests == 0 || r.Replay.Unmatched != 0 {
			t.Errorf("%s: calls=%d replay=%+v", mode, r.Calls, r.Replay)
		}
		if r.Links != 1 {
			t.Errorf("%s: links=%d, want 1", mode, r.Links)
		}
	}
}

func TestCompareExcusedAndUnusable(t *testing.T) {
	lossy, clean := ReplayStats{MissConsequential: 2}, ReplayStats{MissHarmless: 5}
	switch {
	case !Excused(0.40, lossy):
		t.Error("a low score with a consequential replay loss is excused")
	case Excused(0.90, lossy):
		t.Error("a passing score is never excused")
	case Excused(0.40, clean):
		t.Error("harmless losses excuse nothing")
	}

	root, id := compareRoot(t, "<p>x</p>", oracle{
		BodyText: "", MainText: strings.Repeat("hidden words ", 40), MainSelector: "main", Settled: true,
	})
	pages, _, err := ComparePages(root, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if pages[0].Scorable() || pages[0].Unusable == "" {
		t.Fatalf("an unrendered oracle must be unusable: %+v", pages[0])
	}
	if s := pages[0].Score(strings.Repeat("hidden words ", 40)); s.Scored || s.Recall != 0 {
		t.Errorf("an unusable oracle must not produce a score: %+v", s)
	}
	if _, _, err := ComparePages(t.TempDir(), nil); err == nil {
		t.Error("an empty corpus directory must be an error, not an empty comparison")
	}
}
