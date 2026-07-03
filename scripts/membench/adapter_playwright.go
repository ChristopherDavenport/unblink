package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Pinned via `-probe playwright` (see README): the npx invocation resolves this
// exact version so runs are reproducible with a warm npx cache.
// 0.0.77 = Playwright 1.62.0-alpha-2026-06-29, 23 tools.
const playwrightMCPVersion = "0.0.77"

// playwrightAdapter drives Microsoft's Playwright MCP server (npx
// @playwright/mcp): a real Chromium exposed as accessibility-tree snapshots.
// It is pointed at the same Chromium binary as the raw chromedp baseline so
// the browser cost is identical across columns; --isolated keeps the profile
// in-memory (no on-disk session state to warm between runs).
type playwrightAdapter struct{}

func (playwrightAdapter) name() string { return "playwright" }

func (playwrightAdapter) resolve(cfg toolConfig) (spawn, error) {
	if o := cfg.overrides["playwright"]; o != "" {
		parts := strings.Fields(o)
		return spawn{cmd: parts[0], args: parts[1:]}, nil
	}
	if _, err := exec.LookPath("npx"); err != nil {
		return spawn{}, fmt.Errorf("npx not found (Node ≥ 18 required)")
	}
	// --output-mode stdout keeps snapshots inline in the tool result (the
	// alpha writes them to files otherwise — an agent still pays to read the
	// file, but inline is the canonical agent experience and what we meter).
	// The navigate result still drops a snapshot file regardless; --output-dir
	// keeps that out of the repo. Viewport matches unblink's constant 1280x720
	// and the chromedp baseline.
	args := []string{
		"--yes", "@playwright/mcp@" + playwrightMCPVersion,
		"--headless", "--isolated", "--output-mode", "stdout", "--viewport-size", "1280x720",
		"--output-dir", filepath.Join(os.TempDir(), "membench-playwright"),
	}
	if cfg.chromePath != "" {
		args = append(args, "--executable-path", cfg.chromePath)
	} else {
		args = append(args, "--browser", "chromium")
	}
	return spawn{cmd: "npx", args: args, installNote: "npx package (excl. Node runtime)", warmup: true}, nil
}

func (playwrightAdapter) start(p *mcpProc) error {
	return requireTools(p, "browser_navigate", "browser_snapshot")
}

// render is navigate + snapshot: this @playwright/mcp version defers the
// navigate result's snapshot to a file even in stdout output mode, so the
// canonical agent flow to get the page in front of the model is a
// browser_navigate followed by a browser_snapshot (which does return inline).
func (playwrightAdapter) render(p *mcpProc, url string) (string, error) {
	nav, err := p.callTool("browser_navigate", map[string]any{"url": url})
	if err != nil {
		return "", err
	}
	if nav.isError {
		return nav.text, fmt.Errorf("browser_navigate error: %.120s", nav.text)
	}
	snap, err := p.callTool("browser_snapshot", map[string]any{})
	if err != nil {
		return nav.text, err
	}
	if snap.isError {
		return nav.text, fmt.Errorf("browser_snapshot error: %.120s", snap.text)
	}
	return nav.text + "\n" + snap.text, nil
}

// readArticle is what a Playwright-MCP agent pays to read a page: the
// navigate result plus the accessibility-tree snapshot.
func (a playwrightAdapter) readArticle(p *mcpProc, url string) (taskOutput, error) {
	t0 := time.Now()
	text, err := a.render(p, url)
	if err != nil {
		return taskOutput{}, err
	}
	return taskOutput{text: text, calls: 2, dur: time.Since(t0)}, nil
}

// readFull: the snapshot is not paginated — one call already carries the
// whole tree.
func (a playwrightAdapter) readFull(p *mcpProc, url string) (taskOutput, error) {
	return a.readArticle(p, url)
}

// orient: Playwright MCP has no cheap "what's on this page" surface — the
// snapshot is the only page representation. That absence is a finding.
func (playwrightAdapter) orient(*mcpProc, string) (taskOutput, error) {
	return taskOutput{}, errNotSupported
}

// concurrentOK: one browsing context per server — pipelined navigations would
// interleave into the same tab.
func (playwrightAdapter) concurrentOK() bool { return false }
