package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Abstract task names (token table rows).
const (
	taskReadArticle = "read-article"
	taskReadFull    = "read-full"
	taskOrient      = "orient"
)

// runEnv is the shared measurement environment: the fixture corpus (render
// fixtures feed the footprint/latency passes, token fixtures feed the token
// pass) and the run knobs.
type runEnv struct {
	cfg         toolConfig
	renderFx    []fixture
	renderURLs  []string
	tokenFx     []fixture
	tokenURLs   []string
	concurrency int
	perfIters   int
	dumpDir     string
}

// taskTokens is the measured cost of one task on one fixture.
type taskTokens struct {
	tokens int
	bytes  int
	calls  int
	failed bool // task errored or its output missed the fixture sentinel
	na     bool // tool has no equivalent for this task
}

// tokenResult is the token half of a tool's measurement: task → fixture → cost.
type tokenResult struct {
	engine  string
	perTask map[string]map[string]taskTokens
	note    string
}

// measureTool runs the full footprint + latency + token measurement for one
// MCP tool, mirroring the original measureUnblink flow so unblink's published
// numbers stay comparable.
func measureTool(a adapter, sp spawn, env runEnv) (result, perfResult, tokenResult) {
	r := result{engine: a.name(), binaryMB: sp.installMB, note: sp.installNote}
	pr := perfResult{engine: a.name(), medianMS: map[string]float64{}, concN: env.concurrency, concNA: true}
	tr := tokenResult{engine: a.name(), perTask: map[string]map[string]taskTokens{}}
	fail := func(msg string) (result, perfResult, tokenResult) {
		r.note = joinNotes(r.note, msg)
		pr.note = msg
		tr.note = msg
		return r, pr, tr
	}

	if sp.warmup {
		fmt.Fprintf(os.Stderr, "membench: %s: warm-up start (npx cache)\n", a.name())
		if w, _, err := startMCP(a.name(), sp.cmd, sp.args, sp.env, false); err == nil {
			w.Close()
		}
	}
	p, ready, err := startMCP(a.name(), sp.cmd, sp.args, sp.env, env.cfg.verbose)
	if err != nil {
		return fail("launch failed: " + err.Error())
	}
	defer p.Close()
	r.coldStartMS = ready.Milliseconds()
	r.version = p.server.Version
	if err := a.start(p); err != nil {
		return fail(err.Error())
	}

	// Let the process settle, then read idle PSS.
	time.Sleep(300 * time.Millisecond)
	r.idleRSSMB = mb(treeRSSKiB(p.pid))
	if env.cfg.debugMem {
		dumpTreePSS(a.name()+" (idle)", p.pid)
	}

	// Sequential renders, sampling the peak (PSS). The first render is timed
	// separately: browser-backed servers launch their browser lazily on the
	// first navigation, and that cost belongs in the table, not hidden inside
	// a flattering cold-start figure.
	s := newSampler(p.pid)
	var misses []string
	for i, url := range env.renderURLs {
		t0 := time.Now()
		text, err := a.render(p, url)
		if i == 0 {
			r.firstReadMS = time.Since(t0).Milliseconds()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "membench: %s render %s: %v\n", a.name(), url, err)
			continue
		}
		if !containsSentinel(text, env.renderFx[i].sentinel) {
			misses = append(misses, env.renderFx[i].name)
			continue
		}
		r.rendered++
	}
	r.peakRSSMB = mb(s.peakKiB())
	if len(misses) > 0 {
		r.note = joinNotes(r.note, "output missing expected content on: "+strings.Join(misses, ", "))
	}

	// Concurrent renders, only where the server actually dispatches them
	// concurrently — pipelining navigations into a single browsing context
	// would measure garbage confidently.
	if pl, ok := a.(pipeliner); ok && a.concurrentOK() {
		s2 := newSampler(p.pid)
		urls := make([]string, env.concurrency)
		for i := range urls {
			urls[i] = env.renderURLs[i%len(env.renderURLs)]
		}
		if err := pl.pipelineRender(p, urls); err != nil {
			fmt.Fprintf(os.Stderr, "membench: %s concurrent renders: %v\n", a.name(), err)
		}
		r.concurRSSMB = mb(s2.peakKiB())
	} else {
		r.concurNA = true
	}

	pr = measurePerfMCP(a, p, env.renderFx, env.renderURLs, env.perfIters, env.concurrency)
	tr = measureTokens(a, p, env)
	return r, pr, tr
}

// containsSentinel reports whether the output carries the fixture's expected
// content. Markdown emitters escape punctuation in prose (Lightpanda renders
// "forty\-one"), which is a rendering artifact, not content loss — so a miss
// on the raw text is retried with backslash escapes stripped. Token counts
// still run over the raw text: the agent pays for the escapes.
func containsSentinel(text, sentinel string) bool {
	return strings.Contains(text, sentinel) ||
		strings.Contains(strings.ReplaceAll(text, `\`, ""), sentinel)
}

// measureTokens runs the abstract token tasks and counts what the agent's
// context window would pay. Output that errors or misses the fixture sentinel
// is marked failed — it never scores as token efficiency.
func measureTokens(a adapter, p *mcpProc, env runEnv) tokenResult {
	tr := tokenResult{engine: a.name(), perTask: map[string]map[string]taskTokens{}}
	record := func(task string, f fixture, fn func(*mcpProc, string) (taskOutput, error), url string) {
		out, err := fn(p, url)
		var tt taskTokens
		switch {
		case errors.Is(err, errNotSupported):
			tt.na = true
		case err != nil:
			fmt.Fprintf(os.Stderr, "membench: %s %s %s: %v\n", a.name(), task, f.name, err)
			tt.failed = true
		case task == taskOrient && !containsSentinel(out.text, f.title),
			task != taskOrient && !containsSentinel(out.text, f.sentinel):
			fmt.Fprintf(os.Stderr, "membench: %s %s %s: output missing expected content — not counted\n", a.name(), task, f.name)
			tt.failed = true
		case task == taskReadFull && f.endSentinel != "" && !containsSentinel(out.text, f.endSentinel):
			fmt.Fprintf(os.Stderr, "membench: %s %s %s: output missing document-tail content (truncated?) — not counted\n", a.name(), task, f.name)
			tt.failed = true
		default:
			tt = taskTokens{tokens: countTokens(out.text), bytes: len(out.text), calls: out.calls}
		}
		if tr.perTask[task] == nil {
			tr.perTask[task] = map[string]taskTokens{}
		}
		tr.perTask[task][f.name] = tt
		if !tt.na {
			dumpTask(env.dumpDir, a.name(), task, f.name, out.text)
		}
	}
	for i, f := range env.tokenFx {
		record(taskReadArticle, f, a.readArticle, bust(env.tokenURLs[i], "ta", 0))
		if f.tokenOnly {
			record(taskReadFull, f, a.readFull, bust(env.tokenURLs[i], "tf", 0))
			record(taskOrient, f, a.orient, bust(env.tokenURLs[i], "to", 0))
		}
	}
	return tr
}

// dumpTreePSS prints the per-PID PSS breakdown (-debug-mem), for cross-checking
// treeRSSKiB against smem/ps — a detached browser process escaping the tree
// walker shows up here as a missing child.
func dumpTreePSS(label string, root int) {
	fmt.Fprintf(os.Stderr, "membench: %s process tree PSS:\n", label)
	for _, pid := range procTree(root) {
		fmt.Fprintf(os.Stderr, "  %6d %-24s %7.1f MB\n", pid, procComm(pid), mb(procPssKiB(pid)))
	}
}

// dumpTask writes one task's raw output for eyeball review (-dump-dir).
func dumpTask(dir, tool, task, fixture, text string) {
	if dir == "" {
		return
	}
	name := fmt.Sprintf("%s--%s--%s.txt", tool, task, fixture)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "membench: dump %s: %v\n", name, err)
	}
}

func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}
