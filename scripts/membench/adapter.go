package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// errNotSupported marks an abstract task a tool has no equivalent for. It
// prints as n/a — the absence is a finding, not a failure.
var errNotSupported = errors.New("task not supported by this tool")

// taskOutput is one abstract task's cost: the text the agent's context window
// pays for, across however many MCP calls the tool needed.
type taskOutput struct {
	text  string
	calls int
	dur   time.Duration
}

// spawn is a resolved tool invocation plus what resolve could learn about the
// install without starting it.
type spawn struct {
	cmd         string
	args        []string
	env         []string // extra KEY=VALUE entries appended to the environment
	installMB   float64  // on-disk size of the tool itself (0 = unknown)
	installNote string   // qualifier, e.g. "excl. Node runtime"
	// warmup: do one throwaway start+kill before the timed cold start, so
	// npx package download/extraction never pollutes the figure ("cold start
	// measured with a warm npx cache").
	warmup bool
}

// toolConfig carries flag-level configuration into resolve.
type toolConfig struct {
	root       string            // repo root
	chromePath string            // shared Chromium binary for browser-backed tools (fairness)
	overrides  map[string]string // per-tool command overrides from flags/env
	verbose    bool
	debugMem   bool // print per-PID PSS breakdowns for cross-checking
}

// adapter maps the harness's abstract tasks onto one tool's MCP vocabulary.
// Every tool is measured through the same task set on the same fixtures; the
// adapter is the only per-tool code.
type adapter interface {
	name() string
	// resolve locates the tool. An error means "not installed" — the tool is
	// skipped with a note, never a fatal error.
	resolve(cfg toolConfig) (spawn, error)
	// start validates the server after the MCP handshake (e.g. that the tool
	// names this adapter needs exist in tools/list, so a renamed tool degrades
	// to a clear note instead of confusing call errors).
	start(p *mcpProc) error
	// render makes the engine fully process url and returns whatever text the
	// call produced — the unit of work for the footprint and latency passes.
	render(p *mcpProc, url string) (string, error)
	// readArticle is a one-shot page read at the tool's defaults: what a
	// casual agent pays to get the page's content in front of the model.
	readArticle(p *mcpProc, url string) (taskOutput, error)
	// readFull retrieves the whole document (unblink: follows pagination
	// cursors until exhausted; most tools: identical to readArticle) — so a
	// budget cap can't be mistaken for token efficiency.
	readFull(p *mcpProc, url string) (taskOutput, error)
	// orient is the tool's cheapest "what's on this page" call.
	orient(p *mcpProc, url string) (taskOutput, error)
	// concurrentOK reports whether pipelining renders is meaningful (false
	// for single-browsing-context servers, where interleaved navigations
	// would corrupt each other's results).
	concurrentOK() bool
}

// pipeliner is implemented by adapters whose server dispatches concurrent
// requests (unblink); used for the N-concurrent footprint/throughput passes.
type pipeliner interface {
	pipelineRender(p *mcpProc, urls []string) error
}

// callText runs one tool call and wraps it as a taskOutput.
func callText(p *mcpProc, tool string, args map[string]any) (taskOutput, error) {
	t0 := time.Now()
	res, err := p.callTool(tool, args)
	if err != nil {
		return taskOutput{}, err
	}
	if res.isError {
		return taskOutput{}, fmt.Errorf("%s returned an error result: %.120s", tool, res.text)
	}
	return taskOutput{text: res.text, calls: 1, dur: time.Since(t0)}, nil
}

// requireTools diffs needed tool names against the server's tools/list.
func requireTools(p *mcpProc, names ...string) error {
	tools, err := p.listTools()
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	have := make(map[string]bool, len(tools))
	for _, t := range tools {
		have[t.Name] = true
	}
	var missing []string
	for _, n := range names {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("server lacks tool(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
