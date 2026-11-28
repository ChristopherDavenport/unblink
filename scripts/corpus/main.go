// Command corpus captures the real-page differential corpus (ADR 0022): for each
// manifest entry it loads the page once in headless Chromium, records every
// network exchange, and saves what Chromium rendered as the oracle. The captures
// are replayed offline by the root module's `corpus` package (`make corpus`).
//
// Captured bytes are third-party content and are never committed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type manifest struct {
	Pages []struct {
		ID    string   `json:"id"`
		URL   string   `json:"url"`
		Cells []string `json:"cells"`
		Note  string   `json:"note"`
	} `json:"pages"`
}

func main() {
	var (
		manifestPath = flag.String("manifest", "../../corpus/manifest.json", "page manifest")
		out          = flag.String("out", "../../testdata/corpus", "capture directory (gitignored)")
		chrome       = flag.String("chrome", "", "Chromium binary (default: auto-detect)")
		only         = flag.String("only", "", "comma-separated manifest ids to capture")
		force        = flag.Bool("force", false, "recapture pages that already have a capture")
		pause        = flag.Duration("pause", 2*time.Second, "pause between page loads")
	)
	flag.Parse()

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		fatal("parse manifest: %v", err)
	}
	want := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}

	bin := resolveChrome(*chrome)
	if bin == "" {
		fatal("no Chromium found: tried PATH (google-chrome-stable, google-chrome, chromium, chromium-browser, chrome) " +
			"and the puppeteer/playwright caches under ~/.cache; pass -chrome /path (make corpus-capture CHROME=/path)")
	}
	version := chromeVersion(bin)
	fmt.Fprintf(os.Stderr, "corpus: capturing with %s (%s)\n", version, bin)

	var okN, skipN int
	var failed []string
	first := true
	for _, p := range m.Pages {
		if len(want) > 0 && !want[p.ID] {
			continue
		}
		if !idRE.MatchString(p.ID) {
			failed = append(failed, fmt.Sprintf("%s: invalid id", p.ID))
			continue
		}
		dir := filepath.Join(*out, p.ID)
		if _, err := os.Stat(filepath.Join(dir, "oracle.json")); err == nil && !*force {
			skipN++
			continue
		}
		if !first {
			time.Sleep(*pause) // one polite, sequential load per page
		}
		first = false
		_ = os.RemoveAll(dir)
		// A fresh browser per page: no state crosses pages, and a page that
		// crashes Chromium costs only itself.
		o, err := captureOne(bin, p.ID, p.URL, dir, version)
		if err != nil {
			_ = os.RemoveAll(dir)
			failed = append(failed, fmt.Sprintf("%s: %v", p.ID, err))
			fmt.Fprintf(os.Stderr, "  FAIL %-28s %v\n", p.ID, err)
			continue
		}
		okN++
		fmt.Fprintf(os.Stderr, "  ok   %-28s status=%d reqs=%d main=%dB body=%dB settled=%v\n",
			p.ID, o.DocStatus, o.Requests, len(o.MainText), len(o.BodyText), o.Settled)
	}
	fmt.Fprintf(os.Stderr, "corpus: captured %d, already present %d, failed %d → %s\n", okN, skipN, len(failed), *out)
	for _, f := range failed {
		fmt.Fprintf(os.Stderr, "  failed: %s\n", f)
	}
}

func captureOne(bin, id, url, dir, version string) (*oracle, error) {
	c, err := launch(bin)
	if err != nil {
		return nil, fmt.Errorf("launch chromium: %w", err)
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return capturePage(ctx, c, id, url, dir, version)
}

// resolveChrome returns the given path or auto-detects one, as scripts/membench
// does (PATH, then the puppeteer and playwright browser caches; newest wins).
func resolveChrome(explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, name := range []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, pattern := range []string{
		filepath.Join(home, ".cache", "puppeteer", "chrome", "*", "chrome-linux64", "chrome"),
		filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux64", "chrome"),
		filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux", "chrome"),
	} {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1]
		}
	}
	return ""
}

func chromeVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "corpus: "+format+"\n", a...)
	os.Exit(1)
}
