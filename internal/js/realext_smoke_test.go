package js_test

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/webext"
)

// smokeTransport returns 200 for every request and records the URLs it was asked for, so
// a blocked request (one that never reaches it) is observable.
type smokeTransport struct {
	mu   sync.Mutex
	seen []string
}

func (r *smokeTransport) Do(_ context.Context, _ string, url string, _ map[string]string, _ []byte) (*js.Response, error) {
	r.mu.Lock()
	r.seen = append(r.seen, url)
	r.mu.Unlock()
	return &js.Response{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("ok"), FinalURL: url}, nil
}

func (r *smokeTransport) reached(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.seen {
		if strings.Contains(u, sub) {
			return true
		}
	}
	return false
}

// TestRealExtensionSmoke loads a real extension (path in UNBLINK_TEST_EXT), starts its
// background, and drives a page that fetches a few tracker + benign URLs, reporting which
// were blocked. Skipped unless UNBLINK_TEST_EXT is set (the extension is never vendored).
// Set UNBLINK_DEBUG_EXT=1 to trace the background's errors/console to stderr.
func TestRealExtensionSmoke(t *testing.T) {
	path := os.Getenv("UNBLINK_TEST_EXT")
	if path == "" {
		t.Skip("set UNBLINK_TEST_EXT=<path to a real .xpi/dir> to run")
	}
	bundle, err := webext.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m := bundle.Manifest
	t.Logf("loaded %q v%s (MV%d); background.page=%q service_worker=%q scripts=%d; content_scripts=%d; DNR=%d rules; id=%s",
		m.Name, m.Version, m.ManifestVersion, m.Background.Page, m.Background.ServiceWorker,
		len(m.Background.Scripts), len(m.ContentScripts), bundle.Net.Count(), bundle.ID)

	eng := js.New(js.WithTimeout(10*time.Second), js.WithExtensions([]*webext.Bundle{bundle}))
	defer eng.Close()

	// Let the background's async init (filter-list compile, etc.) progress.
	time.Sleep(3 * time.Second)

	trackers := []string{
		"https://www.google-analytics.com/analytics.js",
		"https://www.googletagmanager.com/gtag/js",
		"https://stats.g.doubleclick.net/dc.js",
		"https://cdn.example-benign.test/app.js",
	}
	var b strings.Builder
	b.WriteString(`<html><body>`)
	for i := range trackers {
		b.WriteString(`<div id="r` + strconv.Itoa(i) + `">pending</div>`)
	}
	parts := make([]string, len(trackers))
	for i, u := range trackers {
		parts[i] = `"` + u + `"`
	}
	b.WriteString(`<script>var U=[` + strings.Join(parts, ",") + `];U.forEach(function(u,i){` +
		`fetch(u).then(function(){document.getElementById('r'+i).textContent='reached';})` +
		`.catch(function(){document.getElementById('r'+i).textContent='blocked';});});</script>`)
	b.WriteString(`</body></html>`)

	doc, err := html.Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://news.example.com/article")
	tr := &smokeTransport{}
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
		t.Fatalf("render: %v", err)
	}
	for i, u := range trackers {
		host := ""
		if pu, err := url.Parse(u); err == nil {
			host = pu.Host
		}
		t.Logf("  %-55s -> %-8s (reached network: %v)", u, divText(t, doc, "r"+strconv.Itoa(i)), tr.reached(host))
	}
}
