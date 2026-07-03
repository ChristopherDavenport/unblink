// Command membench measures unblink's memory/startup/latency/token footprint
// against other AI web-browsing tools on identical local fixtures, and prints
// markdown tables for docs/comparison.md.
//
// Every tool is driven the way a real MCP client drives it: as a subprocess
// over stdio JSON-RPC (the raw headless-Chromium baseline, via chromedp, is
// the one non-MCP column). It lives in a SEPARATE module (its own go.mod) so
// its dependencies never touch the published binary. Run it from the repo root
// via `make membench` (unblink vs. Chromium, the original comparison) or
// `make crossbench` (every installed tool). Tools that aren't installed are
// skipped with a note, never a hard failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type result struct {
	engine      string
	version     string
	binaryMB    float64
	coldStartMS int64
	firstReadMS int64 // first sequential render: browser-backed tools launch lazily here
	idleRSSMB   float64
	peakRSSMB   float64
	concurRSSMB float64 // N concurrent renders/tabs
	concurNA    bool    // concurrency not meaningful (single browsing context)
	rendered    int
	note        string
}

func mb(kib uint64) float64 { return float64(kib) / 1024 }

func main() {
	root := flag.String("root", ".", "repo root (for bin/unblink and testdata)")
	chromePath := flag.String("chrome", "", "path to a chrome/chromium binary (auto-detected if empty)")
	concurrency := flag.Int("concurrency", 8, "number of concurrent renders/tabs for the steady-state measurement")
	perfIters := flag.Int("perf-iters", 5, "iterations per fixture for the latency/throughput pass")
	toolsFlag := flag.String("tools", "unblink,chrome", "comma-separated tools to measure, or 'all' (known: "+strings.Join(knownTools(), ", ")+")")
	verbose := flag.Bool("verbose", false, "tee each tool's stderr to ours with a [tool] prefix")
	probe := flag.String("probe", "", "spawn the named tool, print its serverInfo and tools/list, and exit")
	safety := flag.Bool("safety", false, "run the content-boundary probe (hidden text, image beacon, PDF) on -tools and exit")
	dumpDir := flag.String("dump-dir", "", "write each token task's raw output here for eyeball review")
	selfCheck := flag.Bool("selfcheck", false, "run directional sanity assertions on the token results; exit nonzero on violation")
	debugMem := flag.Bool("debug-mem", false, "print per-PID PSS breakdowns for cross-checking against smem/ps")
	playwrightCmd := flag.String("playwright-cmd", "", "override the playwright MCP launch command")
	charlotteCmd := flag.String("charlotte-cmd", "", "override the charlotte MCP launch command")
	obscuraBin := flag.String("obscura-bin", "", "path to the obscura binary (default $OBSCURA_BIN, then PATH)")
	lightpandaBin := flag.String("lightpanda-bin", "", "path to the lightpanda binary (default $LIGHTPANDA_BIN, then PATH)")
	flag.Parse()

	cfg := toolConfig{
		root:       *root,
		chromePath: resolveChrome(*chromePath),
		verbose:    *verbose,
		debugMem:   *debugMem,
		overrides: map[string]string{
			"playwright": *playwrightCmd,
			"charlotte":  *charlotteCmd,
			"obscura":    *obscuraBin,
			"lightpanda": *lightpandaBin,
		},
	}

	if *probe != "" {
		runProbe(*probe, cfg)
		return
	}

	if *safety {
		runSafety(parseTools(*toolsFlag), cfg)
		return
	}

	fx, err := allFixtures(*root)
	if err != nil {
		die("fixtures: %v", err)
	}
	srv, err := serveFixtures(*root, fx)
	if err != nil {
		die("serve fixtures: %v", err)
	}
	defer srv.Close()

	env := runEnv{cfg: cfg, concurrency: *concurrency, perfIters: *perfIters, dumpDir: *dumpDir}
	for _, f := range fx {
		if !f.tokenOnly {
			env.renderFx = append(env.renderFx, f)
			env.renderURLs = append(env.renderURLs, srv.URL+f.path)
		}
		env.tokenFx = append(env.tokenFx, f)
		env.tokenURLs = append(env.tokenURLs, srv.URL+f.path)
	}
	if *dumpDir != "" {
		if err := os.MkdirAll(*dumpDir, 0o755); err != nil {
			die("dump-dir: %v", err)
		}
	}

	tools := parseTools(*toolsFlag)
	fmt.Fprintf(os.Stderr, "membench: %d render + %d token fixtures, tools=%s, concurrency=%d, perf-iters=%d, machine=%s/%s\n",
		len(env.renderFx), len(env.tokenFx)-len(env.renderFx), strings.Join(tools, ","), *concurrency, *perfIters, runtime.GOOS, runtime.GOARCH)

	var results []result
	var perf []perfResult
	var toks []tokenResult
	var skipped []string
	for _, name := range tools {
		if name == "chrome" {
			if cfg.chromePath == "" {
				skipped = append(skipped, "chrome: no chrome/chromium found (set -chrome or install one)")
				continue
			}
			fmt.Fprintf(os.Stderr, "membench: measuring chromium at %s\n", cfg.chromePath)
			res, pr := measureChrome(cfg.chromePath, env.renderFx, env.renderURLs, *concurrency, *perfIters)
			results = append(results, res)
			perf = append(perf, pr)
			continue
		}
		a, ok := adapterFor(name)
		if !ok {
			die("unknown tool %q (known: %s)", name, strings.Join(knownTools(), ", "))
		}
		sp, err := a.resolve(cfg)
		if err != nil {
			skipped = append(skipped, name+": "+err.Error())
			continue
		}
		fmt.Fprintf(os.Stderr, "membench: measuring %s (%s %s)\n", name, sp.cmd, strings.Join(sp.args, " "))
		res, pr, tr := measureTool(a, sp, env)
		results = append(results, res)
		perf = append(perf, pr)
		toks = append(toks, tr)
	}

	printRunHeader(results)
	printTable(results)
	printPerfTable(perf)
	printTokenTable(toks, env.tokenFx)
	if len(skipped) > 0 {
		fmt.Println()
		for _, s := range skipped {
			fmt.Printf("_Skipped %s_\n", s)
		}
	}
	if *selfCheck {
		if bad := selfcheck(toks); len(bad) > 0 {
			for _, b := range bad {
				fmt.Fprintf(os.Stderr, "membench: SELFCHECK FAIL: %s\n", b)
			}
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "membench: selfcheck passed")
	}
}

// runProbe spawns one tool and prints its identity and vocabulary — the
// discovery step that pins each adapter's tool names and parameters.
func runProbe(name string, cfg toolConfig) {
	a, ok := adapterFor(name)
	if !ok {
		die("unknown tool %q (known: %s)", name, strings.Join(knownTools(), ", "))
	}
	sp, err := a.resolve(cfg)
	if err != nil {
		die("resolve %s: %v", name, err)
	}
	fmt.Fprintf(os.Stderr, "membench: probing %s: %s %s\n", name, sp.cmd, strings.Join(sp.args, " "))
	p, ready, err := startMCP(name, sp.cmd, sp.args, sp.env, cfg.verbose)
	if err != nil {
		die("start %s: %v", name, err)
	}
	defer p.Close()
	fmt.Printf("server: %s %s (initialize in %s)\n", p.server.Name, p.server.Version, ready.Round(time.Millisecond))
	tools, err := p.listTools()
	if err != nil {
		die("tools/list: %v", err)
	}
	fmt.Printf("tools (%d):\n", len(tools))
	for _, t := range tools {
		fmt.Printf("\n## %s\n%s\nschema: %s\n", t.Name, t.Description, string(t.InputSchema))
	}
}

// measureChrome drives headless Chromium through the same corpus — the raw
// non-MCP baseline. Returns footprint and latency/throughput.
func measureChrome(chrome string, fx []fixture, urls []string, concurrency, perfIters int) (result, perfResult) {
	r := result{engine: "chromium (headless)", version: chromeVersion(chrome)}
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
	for i, url := range urls {
		t0 := time.Now()
		err := cr.render(url)
		if i == 0 {
			r.firstReadMS = time.Since(t0).Milliseconds()
		}
		if err != nil {
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
	// Browser caches: puppeteer's (npx @puppeteer/browsers install chrome) and
	// playwright's full-chromium builds. Newest match wins.
	if home, err := os.UserHomeDir(); err == nil {
		for _, pattern := range []string{
			filepath.Join(home, ".cache", "puppeteer", "chrome", "*", "chrome-linux64", "chrome"),
			filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux", "chrome"),
		} {
			matches, _ := filepath.Glob(pattern)
			if len(matches) > 0 {
				return matches[len(matches)-1]
			}
		}
	}
	return ""
}

func chromeVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fileMB(path string) float64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / (1024 * 1024)
}

// printRunHeader emits the provenance line the doc's measured sections cite:
// date, machine, and every measured tool's version.
func printRunHeader(rs []result) {
	fmt.Println()
	fmt.Printf("_Measured %s · %s/%s (kernel %s) · %d vCPU / %.1f GiB",
		time.Now().Format("2006-01-02"), runtime.GOOS, runtime.GOARCH, kernelRelease(), runtime.NumCPU(), memTotalGiB())
	for _, r := range rs {
		if r.version != "" {
			fmt.Printf(" · %s %s", r.engine, r.version)
		}
	}
	fmt.Println("_")
}

func kernelRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(data))
}

func memTotalGiB() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kib, _ := strconv.ParseFloat(f[1], 64)
				return kib / (1024 * 1024)
			}
		}
	}
	return 0
}

func printTable(rs []result) {
	if len(rs) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("### Measured footprint")
	fmt.Println()
	fmt.Println("| Metric | " + colHeaders(rs) + " |")
	fmt.Println("|---|" + colSep(rs))
	fmt.Printf("| Binary on disk | %s |\n", row(rs, func(r result) string {
		if r.binaryMB == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.0f MB", r.binaryMB)
	}))
	fmt.Printf("| Cold start → ready | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%d ms", r.coldStartMS) }))
	fmt.Printf("| First page ready | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%d ms", r.firstReadMS) }))
	fmt.Printf("| Idle RSS (post-init) | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%.0f MB", r.idleRSSMB) }))
	fmt.Printf("| Peak RSS (sequential renders) | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%.0f MB", r.peakRSSMB) }))
	fmt.Printf("| Peak RSS (8 concurrent) | %s |\n", row(rs, func(r result) string {
		if r.concurNA {
			return "n/a"
		}
		return fmt.Sprintf("%.0f MB", r.concurRSSMB)
	}))
	fmt.Printf("| Fixtures rendered | %s |\n", row(rs, func(r result) string { return fmt.Sprintf("%d", r.rendered) }))
	fmt.Println()
	for _, r := range rs {
		if r.note != "" {
			fmt.Printf("_%s: %s_\n", r.engine, r.note)
		}
	}
}

// printTokenTable emits the token-cost table: what the agent's context window
// pays per abstract task, per fixture, per tool — with a ×ratio against
// unblink where both numbers exist.
func printTokenTable(rs []tokenResult, fx []fixture) {
	if len(rs) == 0 {
		return
	}
	var base *tokenResult
	for i := range rs {
		if rs[i].engine == "unblink" {
			base = &rs[i]
		}
	}
	fmt.Println()
	fmt.Println("### Token cost per task")
	fmt.Println()
	fmt.Println("| Task · fixture | " + tokHeaders(rs) + " |")
	fmt.Println("|---|" + tokSep(rs))
	printRow := func(task string, f fixture) {
		cells := make([]string, len(rs))
		for i, r := range rs {
			tt, ok := r.perTask[task][f.name]
			if !ok {
				cells[i] = "—"
				continue
			}
			var baseTok int
			if base != nil {
				baseTok = base.perTask[task][f.name].tokens
			}
			cells[i] = tokCell(tt, baseTok, base != nil && r.engine == base.engine)
		}
		fmt.Printf("| %s · %s | %s |\n", task, f.name, strings.Join(cells, " | "))
	}
	for _, f := range fx {
		printRow(taskReadArticle, f)
	}
	for _, f := range fx {
		if f.tokenOnly {
			printRow(taskReadFull, f)
		}
	}
	for _, f := range fx {
		if f.tokenOnly {
			printRow(taskOrient, f)
		}
	}
	fmt.Println()
	fmt.Println("_Cells are tokens (raw bytes; MCP calls if >1), with the ×ratio vs. unblink. " + tokenizerNote + "_")
	for _, r := range rs {
		if r.note != "" {
			fmt.Printf("_%s: %s_\n", r.engine, r.note)
		}
	}
}

func tokCell(tt taskTokens, baseTok int, isBase bool) string {
	switch {
	case tt.na:
		return "n/a"
	case tt.failed:
		return "fail"
	}
	c := comma(tt.tokens) + " (" + humanBytes(tt.bytes)
	if tt.calls > 1 {
		c += fmt.Sprintf(", %d calls", tt.calls)
	}
	c += ")"
	if !isBase && baseTok > 0 {
		c += fmt.Sprintf(" ×%.1f", float64(tt.tokens)/float64(baseTok))
	}
	return c
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

func comma(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func tokHeaders(rs []tokenResult) string {
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.engine
	}
	return strings.Join(names, " | ")
}

func tokSep(rs []tokenResult) string {
	return strings.Repeat("---|", len(rs))
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

// parseTools expands the -tools flag into a validated, de-duplicated list in
// canonical column order.
func parseTools(s string) []string {
	if s == "all" {
		var out []string
		for _, t := range allToolOrder {
			if _, ok := adapterFor(t); ok || t == "chrome" {
				out = append(out, t)
			}
		}
		return out
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	// Canonical order keeps table columns stable regardless of flag order.
	sort.SliceStable(out, func(i, j int) bool { return toolRank(out[i]) < toolRank(out[j]) })
	return out
}

func toolRank(name string) int {
	for i, t := range allToolOrder {
		if t == name {
			return i
		}
	}
	return len(allToolOrder)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "membench: "+format+"\n", args...)
	os.Exit(1)
}
