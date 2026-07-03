package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// obscuraAdapter drives Obscura (a from-scratch Rust engine with embedded V8)
// via `obscura mcp` over stdio. Vocabulary pinned from `-probe obscura`
// (serverInfo "obscura-mcp 0.1.0", 35 tools): browser_navigate {url, waitUntil}
// + browser_snapshot (URL, title, readable body text — hard-truncates at ~4 KB
// with a "...(truncated, N more chars)" marker and no continuation mechanism)
// + browser_markdown {max_chars, default 4000} as the documented way to ask
// for more content.
//
// Resolution: -obscura-bin flag → $OBSCURA_BIN → PATH → ~/.cache/unblink-membench/obscura.
// Install (not automated — external binary, user's call):
//
//	curl -LO https://github.com/h4ckf0r0day/obscura/releases/latest/download/obscura-x86_64-linux.tar.gz
//	tar xzf obscura-x86_64-linux.tar.gz -C ~/.cache/unblink-membench/
type obscuraAdapter struct{}

func (obscuraAdapter) name() string { return "obscura" }

func (obscuraAdapter) resolve(cfg toolConfig) (spawn, error) {
	bin, err := findBinary("obscura", cfg.overrides["obscura"], "OBSCURA_BIN")
	if err != nil {
		return spawn{}, err
	}
	// The release ships a sibling obscura-worker binary the server spawns —
	// count it in the install footprint. --allow-private-network lets it reach
	// the 127.0.0.1 fixture server (its SSRF guard blocks loopback by default),
	// the same escape hatch unblink runs with (--allow-private).
	mb := fileMB(bin) + fileMB(filepath.Join(filepath.Dir(bin), "obscura-worker"))
	return spawn{cmd: bin, args: []string{"mcp", "--allow-private-network"}, installMB: mb}, nil
}

func (obscuraAdapter) start(p *mcpProc) error {
	return requireTools(p, "browser_navigate", "browser_snapshot")
}

// render is navigate + snapshot: Obscura hands the agent automation
// primitives, and snapshot (URL, title, body text) is its content surface.
func (obscuraAdapter) render(p *mcpProc, url string) (string, error) {
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

func (a obscuraAdapter) readArticle(p *mcpProc, url string) (taskOutput, error) {
	t0 := time.Now()
	text, err := a.render(p, url)
	if err != nil {
		return taskOutput{}, err
	}
	return taskOutput{text: text, calls: 2, dur: time.Since(t0)}, nil
}

// readFull: the snapshot hard-truncates at ~4 KB, so the whole document goes
// through browser_markdown with an explicit max_chars — the tool's documented
// expansion mechanism, the same way unblink's read-full follows its cursors.
// The end-sentinel check still gates the result.
func (obscuraAdapter) readFull(p *mcpProc, url string) (taskOutput, error) {
	t0 := time.Now()
	nav, err := p.callTool("browser_navigate", map[string]any{"url": url})
	if err != nil {
		return taskOutput{}, err
	}
	if nav.isError {
		return taskOutput{}, fmt.Errorf("browser_navigate error: %.120s", nav.text)
	}
	md, err := p.callTool("browser_markdown", map[string]any{"max_chars": 1 << 20})
	if err != nil {
		return taskOutput{}, err
	}
	if md.isError {
		return taskOutput{}, fmt.Errorf("browser_markdown error: %.120s", md.text)
	}
	return taskOutput{text: nav.text + "\n" + md.text, calls: 2, dur: time.Since(t0)}, nil
}

// orient: no cheap orientation surface — the snapshot is the only page
// representation. That absence is a finding.
func (obscuraAdapter) orient(*mcpProc, string) (taskOutput, error) {
	return taskOutput{}, errNotSupported
}

func (obscuraAdapter) concurrentOK() bool { return false }

// findBinary resolves an external tool binary: explicit override → env var →
// PATH → the membench tool cache (~/.cache/unblink-membench).
func findBinary(name, override, envVar string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("%s: %w", override, err)
		}
		return override, nil
	}
	if v := os.Getenv(envVar); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", fmt.Errorf("$%s=%s: %w", envVar, v, err)
		}
		return v, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		cached := filepath.Join(home, ".cache", "unblink-membench", name)
		if fi, err := os.Stat(cached); err == nil && fi.Mode()&0o111 != 0 {
			return cached, nil
		}
	}
	return "", fmt.Errorf("not installed (looked at -%s-bin, $%s, PATH, ~/.cache/unblink-membench/%s — see scripts/membench/README.md)",
		strings.ToLower(name), envVar, name)
}
