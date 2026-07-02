// Command membench measures unblink's memory/startup footprint against a
// headless Chromium baseline, on identical local fixtures, and prints a markdown
// table for docs/comparison.md.
//
// It lives in a SEPARATE module (its own go.mod) so its chromedp dependency
// never touches the published binary. Run it from the repo root via
// `make membench` (which passes -root). A system chrome/chromium is required for
// the Chromium columns; without one, the unblink columns still print.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

type result struct {
	engine      string
	binaryMB    float64
	coldStartMS int64
	idleRSSMB   float64
	peakRSSMB   float64
	concurRSSMB float64 // 8 concurrent renders/tabs
	rendered    int
	note        string
}

func mb(kib uint64) float64 { return float64(kib) / 1024 }

func main() {
	root := flag.String("root", ".", "repo root (for bin/unblink and testdata)")
	chromePath := flag.String("chrome", "", "path to a chrome/chromium binary (auto-detected if empty)")
	concurrency := flag.Int("concurrency", 8, "number of concurrent renders/tabs for the steady-state measurement")
	perfIters := flag.Int("perf-iters", 5, "iterations per fixture for the latency/throughput pass")
	flag.Parse()

	srv, err := serveFixtures(*root)
	if err != nil {
		die("serve fixtures: %v", err)
	}
	defer srv.Close()
	fx := fixtures()
	urls := make([]string, len(fx))
	for i, f := range fx {
		urls[i] = srv.URL + f.path
	}

	fmt.Fprintf(os.Stderr, "membench: %d fixtures, concurrency=%d, perf-iters=%d, machine=%s/%s\n",
		len(fx), *concurrency, *perfIters, runtime.GOOS, runtime.GOARCH)

	var results []result
	var perf []perfResult
	res, pr := measureUnblink(*root, fx, urls, *concurrency, *perfIters)
	results = append(results, res)
	perf = append(perf, pr)

	chrome := resolveChrome(*chromePath)
	if chrome == "" {
		fmt.Fprintln(os.Stderr, "membench: no chrome/chromium found (set -chrome or install one); skipping the Chromium baseline")
	} else {
		fmt.Fprintf(os.Stderr, "membench: chromium at %s\n", chrome)
		res, pr := measureChrome(chrome, fx, urls, *concurrency, *perfIters)
		results = append(results, res)
		perf = append(perf, pr)
	}

	printTable(results)
	printPerfTable(perf)
}

// measureUnblink builds/locates bin/unblink, drives it over stdio, and returns
// both the footprint and the latency/throughput results (measured on the same
// live process before it is torn down).
func measureUnblink(root string, fx []fixture, urls []string, concurrency, perfIters int) (result, perfResult) {
	bin := filepath.Join(root, "bin", "unblink")
	if _, err := os.Stat(bin); err != nil {
		die("bin/unblink not found at %s — run `make build` first", bin)
	}
	r := result{engine: "unblink"}
	r.binaryMB = fileMB(bin)

	u, ready, err := startUnblink(bin)
	if err != nil {
		die("start unblink: %v", err)
	}
	defer u.Close()
	r.coldStartMS = ready.Milliseconds()

	// Let the process settle, then read idle PSS.
	time.Sleep(300 * time.Millisecond)
	r.idleRSSMB = mb(treeRSSKiB(u.pid))

	// Sequential renders, sampling the peak (PSS).
	s := newSampler(u.pid)
	for _, url := range urls {
		if err := u.read(url); err != nil {
			fmt.Fprintf(os.Stderr, "membench: unblink read %s: %v\n", url, err)
			continue
		}
		r.rendered++
	}
	r.peakRSSMB = mb(s.peakKiB())

	// Concurrent renders: pipeline `concurrency` reads at once, sampling the peak.
	s2 := newSampler(u.pid)
	if err := u.readConcurrent(urls, concurrency); err != nil {
		fmt.Fprintf(os.Stderr, "membench: unblink concurrent reads: %v\n", err)
	}
	r.concurRSSMB = mb(s2.peakKiB())

	pr := measurePerfUnblink(u, fx, urls, perfIters, concurrency)
	return r, pr
}

// measureChrome drives headless Chromium through the same corpus, returning both
// footprint and latency/throughput.
func measureChrome(chrome string, fx []fixture, urls []string, concurrency, perfIters int) (result, perfResult) {
	r := result{engine: "chromium (headless)"}
	r.binaryMB = fileMB(chrome)

	cr, ready, err := startChrome(chrome)
	if err != nil {
		r.note = "launch failed: " + err.Error()
		return r, perfResult{engine: r.engine, note: r.note}
	}
	defer cr.Close()
	r.coldStartMS = ready.Milliseconds()
	if cr.pid == 0 {
		cr.pid = findChromeRoot()
	}

	time.Sleep(300 * time.Millisecond)
	r.idleRSSMB = mb(treeRSSKiB(cr.pid))

	s := newSampler(cr.pid)
	for _, url := range urls {
		if err := cr.render(url); err != nil {
			fmt.Fprintf(os.Stderr, "membench: chrome render %s: %v\n", url, err)
			continue
		}
		r.rendered++
	}
	r.peakRSSMB = mb(s.peakKiB())

	s2 := newSampler(cr.pid)
	cr.renderConcurrent(urls, concurrency)
	r.concurRSSMB = mb(s2.peakKiB())

	pr := measurePerfChrome(cr, fx, urls, perfIters, concurrency)
	return r, pr
}

// resolveChrome returns the given path, or auto-detects a common chrome binary.
func resolveChrome(explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, name := range []string{
		"google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "chrome",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	// puppeteer's cache (npx @puppeteer/browsers install chrome).
	if home, err := os.UserHomeDir(); err == nil {
		matches, _ := filepath.Glob(filepath.Join(home, ".cache", "puppeteer", "chrome", "*", "chrome-linux64", "chrome"))
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
	}
	return ""
}

func fileMB(path string) float64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / (1024 * 1024)
}

func printTable(rs []result) {
	fmt.Println()
	fmt.Println("| Metric | " + colHeaders(rs) + " |")
	fmt.Println("|---|" + colSep(rs))
	fmt.Printf("| Binary on disk | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%.0f MB", r.binaryMB) }))
	fmt.Printf("| Cold start → ready | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%d ms", r.coldStartMS) }))
	fmt.Printf("| Idle RSS (post-init) | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%.0f MB", r.idleRSSMB) }))
	fmt.Printf("| Peak RSS (sequential renders) | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%.0f MB", r.peakRSSMB) }))
	fmt.Printf("| Peak RSS (8 concurrent) | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%.0f MB", r.concurRSSMB) }))
	fmt.Printf("| Fixtures rendered | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%d", r.rendered) }))
	fmt.Println()
	for _, r := range rs {
		if r.note != "" {
			fmt.Printf("_%s: %s_\n", r.engine, r.note)
		}
	}
}

func colHeaders(rs []result) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += " | "
		}
		out += r.engine
	}
	return out
}

func colSep(rs []result) string {
	out := ""
	for range rs {
		out += "---|"
	}
	return out
}

func row(rs []result, f func(result) string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += " | "
		}
		out += f(r)
	}
	return out
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "membench: "+format+"\n", args...)
	os.Exit(1)
}
