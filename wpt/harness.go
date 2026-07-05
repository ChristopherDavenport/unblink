//go:build wpt

package wpt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// defaultRoot is the vendored corpus location; override with WPT_DIR.
const defaultRoot = "testdata/wpt"

// run enumerates and executes the in-scope corpus, returning per-test results, the
// per-dir count of skipped worker-only tests, and the pinned WPT version.
func run(ctx context.Context) ([]testResult, map[string]int, string, error) {
	root := os.Getenv("WPT_DIR")
	if root == "" {
		// The test runs from the wpt/ package dir; resolve the corpus relative to
		// the module root (go.mod) so `make wpt` works from anywhere.
		root = filepath.Join(repoRoot(), defaultRoot)
	}
	if _, err := os.Stat(root); err != nil {
		return nil, nil, "", fmt.Errorf("WPT corpus not found at %s (run `make wpt-sync`): %w", root, err)
	}
	version := readVersion(root)

	sc, err := loadScope()
	if err != nil {
		return nil, nil, version, err
	}
	descs, nonWindow, err := enumerate(root, sc)
	if err != nil {
		return nil, nil, version, fmt.Errorf("enumerate: %w", err)
	}
	// WPT_ONLY restricts the run to descriptors whose path has one of the given
	// comma-separated prefixes — for fast targeted iteration on one directory.
	if only := os.Getenv("WPT_ONLY"); only != "" {
		prefixes := strings.Split(only, ",")
		kept := descs[:0]
		for _, d := range descs {
			for _, pre := range prefixes {
				if strings.HasPrefix(d.RelPath, strings.TrimSpace(pre)) {
					kept = append(kept, d)
					break
				}
			}
		}
		descs = kept
	}

	conc := runtime.NumCPU() - 1
	if conc < 1 {
		conc = 1
	}
	// The asset cache amortizes the ~5k-line testharness.js fetch+compile across
	// every render (it is byte-identical for all tests), turning a ~4s cold render
	// into ~0.3s steady state. Safe here: the corpus is static and offline.
	eng := js.New(js.WithConcurrency(conc), js.WithTimeout(defaultBudget), js.WithAssetCache(time.Hour))

	results := make([]testResult, len(descs))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i := range descs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runOne(ctx, eng, root, descs[i])
		}(i)
	}
	wg.Wait()
	return results, nonWindow, version, nil
}

// runOne renders a single descriptor and classifies its outcome. Pre-skipped
// descriptors are classified without touching the engine.
func runOne(ctx context.Context, eng *js.Engine, root string, d testDesc) testResult {
	if d.Preskip != "" {
		return classify(d, false, nil, nil)
	}
	doc, err := html.Parse(strings.NewReader(d.HTML))
	if err != nil {
		return testResult{Dir: d.Dir, RelPath: d.RelPath, Variant: d.Variant, Bucket: bucketHarness, Msg: "parse: " + err.Error()}
	}
	base, _ := url.Parse("https://web-platform.test" + d.URLPath)
	diag := &js.RenderResult{}
	env := js.Env{
		Transport:       &wptTransport{root: root},
		Wait:            &js.WaitCondition{Selector: "#__wpt_results"},
		Timeout:         d.Budget,
		ResponseHeaders: d.Headers, // document CSP/COOP/COEP from the .headers sidecar
		Diag:            diag,
	}
	rctx, cancel := context.WithTimeout(ctx, d.Budget+2*time.Second)
	defer cancel()
	_ = eng.Render(rctx, doc, base, env) // errors are data; the results node (or its absence) decides

	payload, found := extractResults(doc)
	return classify(d, found, payload, diag)
}

// extractResults walks the rendered tree for <script id="__wpt_results"> and
// unmarshals the JSON our reporter wrote, reversing the defensive </script escape.
func extractResults(doc *html.Node) (*wptResults, bool) {
	node := findByID(doc, "__wpt_results")
	if node == nil || node.FirstChild == nil {
		return nil, false
	}
	raw := node.FirstChild.Data
	raw = strings.ReplaceAll(raw, `<\/script`, `</script`)
	var res wptResults
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, false
	}
	return &res, true
}

func findByID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findByID(c, id); r != nil {
			return r
		}
	}
	return nil
}

// repoRoot walks up from the working directory to the module root (the dir with
// go.mod), falling back to "." if none is found.
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

func readVersion(root string) string {
	b, err := os.ReadFile(root + "/WPT_VERSION")
	if err != nil {
		return "unknown"
	}
	v := strings.TrimSpace(string(b))
	if len(v) > 12 {
		v = v[:12]
	}
	return v
}
