package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// bust appends a unique query param so each render is a genuine page cache miss
// (both unblink and Chromium key their page cache on the full URL). The fixture
// server ignores the query, so the same HTML is served; the JS *asset* cache
// still serves the framework bundle warm — the realistic steady state. Distinct
// keys per phase (mb/sq/cc) avoid cross-phase collisions that would be cache hits.
func bust(u, key string, n int) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + key + "=" + strconv.Itoa(n)
}

// perfResult is the latency/throughput half of the benchmark: how fast each
// engine turns a URL into agent-ready output.
type perfResult struct {
	engine   string
	fixtures []string           // in display order
	medianMS map[string]float64 // per-fixture median render latency
	seqPPS   float64            // sequential throughput, pages/sec
	concPPS  float64            // throughput at N-concurrency, pages/sec
	concN    int
	note     string
}

// perfSettle is the quiet window both engines get in the latency pass, so the
// comparison isolates engine speed rather than settle policy. It matches
// unblink's ~60ms quiet period (4×15ms ticks).
const perfSettle = 60 * time.Millisecond

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// measurePerfUnblink times per-fixture renders (median of iters) and sequential
// + concurrent throughput, against an already-started process.
func measurePerfUnblink(u *unblinkProc, fx []fixture, urls []string, iters, concurrency int) perfResult {
	p := perfResult{engine: "unblink", medianMS: map[string]float64{}, concN: concurrency}
	for i, f := range fx {
		p.fixtures = append(p.fixtures, f.name)
		var ds []time.Duration
		for k := 0; k < iters; k++ {
			d, err := u.readTimed(bust(urls[i], "mb", k))
			if err != nil {
				continue
			}
			ds = append(ds, d)
		}
		p.medianMS[f.name] = float64(median(ds).Microseconds()) / 1000
	}
	p.seqPPS = seqThroughput(func(url string) { _ = u.read(url) }, urls, iters)
	// Concurrent: pipeline concurrency*iters cache-missing reads, divide by wall time.
	total := concurrency * iters
	big := make([]string, total)
	for i := range big {
		big[i] = bust(urls[i%len(urls)], "cc", i)
	}
	t0 := time.Now()
	if err := u.readConcurrent(big, total); err == nil {
		p.concPPS = float64(total) / time.Since(t0).Seconds()
	}
	return p
}

// measurePerfChrome is the Chromium counterpart, with a settle window matched to
// unblink's quiet period.
func measurePerfChrome(cr *chromeRunner, fx []fixture, urls []string, iters, concurrency int) perfResult {
	p := perfResult{engine: "chromium (headless)", medianMS: map[string]float64{}, concN: concurrency}
	for i, f := range fx {
		p.fixtures = append(p.fixtures, f.name)
		var ds []time.Duration
		for k := 0; k < iters; k++ {
			d, err := cr.renderTimed(bust(urls[i], "mb", k), perfSettle)
			if err != nil {
				continue
			}
			ds = append(ds, d)
		}
		p.medianMS[f.name] = float64(median(ds).Microseconds()) / 1000
	}
	p.seqPPS = seqThroughput(func(url string) { _ = cr.renderSettle(url, perfSettle) }, urls, iters)
	total := concurrency * iters
	big := make([]string, total)
	for i := range big {
		big[i] = bust(urls[i%len(urls)], "cc", i)
	}
	t0 := time.Now()
	cr.renderConcurrentSettle(big, total, perfSettle)
	p.concPPS = float64(total) / time.Since(t0).Seconds()
	return p
}

// seqThroughput runs render over the full url set iters times back-to-back
// (each a cache miss) and returns pages/sec.
func seqThroughput(render func(string), urls []string, iters int) float64 {
	t0 := time.Now()
	n := 0
	for k := 0; k < iters; k++ {
		for i, url := range urls {
			render(bust(url, "sq", k*len(urls)+i))
			n++
		}
	}
	elapsed := time.Since(t0).Seconds()
	if elapsed == 0 {
		return 0
	}
	return float64(n) / elapsed
}

func printPerfTable(rs []perfResult) {
	if len(rs) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("### Render latency & throughput")
	fmt.Println()
	fmt.Println("| Metric | " + perfHeaders(rs) + " |")
	fmt.Println("|---|" + perfSep(rs))
	for _, name := range rs[0].fixtures {
		fmt.Printf("| %s (median render) | %s |\n", name, perfRow(rs, func(p perfResult) string {
			return fmt.Sprintf("%.0f ms", p.medianMS[name])
		}))
	}
	fmt.Printf("| Throughput, sequential | %s |\n", perfRow(rs, func(p perfResult) string {
		return fmt.Sprintf("%.0f pages/s", p.seqPPS)
	}))
	fmt.Printf("| Throughput, %d concurrent | %s |\n", rs[0].concN, perfRow(rs, func(p perfResult) string {
		return fmt.Sprintf("%.0f pages/s", p.concPPS)
	}))
}

func perfHeaders(rs []perfResult) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += " | "
		}
		out += r.engine
	}
	return out
}

func perfSep(rs []perfResult) string {
	out := ""
	for range rs {
		out += "---|"
	}
	return out
}

func perfRow(rs []perfResult, f func(perfResult) string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += " | "
		}
		out += f(r)
	}
	return out
}
