package main

import (
	"fmt"
	"time"
)

// lightpandaAdapter drives Lightpanda (a from-scratch Zig engine with V8) via
// `lightpanda mcp` over stdio. Vocabulary pinned from `-probe lightpanda`
// against the 2026-07-03 nightly (serverInfo "lightpanda 0.1.0", 28 tools):
// `goto`, `markdown` (optional url = navigate first; unbounded by default,
// optional maxBytes cap), `html`, `tree` (full semantic DOM tree; maxDepth is
// opt-in), `links`, `extract`, plus interaction tools. Beta engine:
// per-fixture failures are expected, recorded, and never abort the run (a
// crash count is itself a measured finding).
//
// Resolution: -lightpanda-bin flag → $LIGHTPANDA_BIN → PATH → ~/.cache/unblink-membench/lightpanda.
// Install (not automated — external binary, user's call):
//
//	curl -L -o ~/.cache/unblink-membench/lightpanda \
//	  https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-x86_64-linux
//	chmod +x ~/.cache/unblink-membench/lightpanda
type lightpandaAdapter struct{}

func (lightpandaAdapter) name() string { return "lightpanda" }

func (lightpandaAdapter) resolve(cfg toolConfig) (spawn, error) {
	bin, err := findBinary("lightpanda", cfg.overrides["lightpanda"], "LIGHTPANDA_BIN")
	if err != nil {
		return spawn{}, err
	}
	return spawn{cmd: bin, args: []string{"mcp"}, installMB: fileMB(bin)}, nil
}

func (lightpandaAdapter) start(p *mcpProc) error {
	return requireTools(p, "goto", "markdown")
}

// render is one `markdown{url}` call: Lightpanda's advertised read path
// navigates and renders Markdown in a single tool call.
func (a lightpandaAdapter) render(p *mcpProc, url string) (string, error) {
	out, err := a.readArticle(p, url)
	return out.text, err
}

// readArticle: `markdown` at tool defaults — no maxBytes, no selector — is the
// whole document as Markdown, so article and full reads are the same surface.
func (lightpandaAdapter) readArticle(p *mcpProc, url string) (taskOutput, error) {
	t0 := time.Now()
	md, err := p.callTool("markdown", map[string]any{"url": url})
	if err != nil {
		return taskOutput{}, err
	}
	if md.isError {
		return taskOutput{}, fmt.Errorf("markdown error: %.120s", md.text)
	}
	return taskOutput{text: md.text, calls: 1, dur: time.Since(t0)}, nil
}

func (a lightpandaAdapter) readFull(p *mcpProc, url string) (taskOutput, error) {
	return a.readArticle(p, url)
}

// orient: no default-shaped orientation surface. `tree` at defaults is the
// full semantic DOM tree (a snapshot analog, same ruling as Playwright), and
// scoping it needs a maxDepth the tool doesn't default — that absence is a
// finding, not a harness gap.
func (lightpandaAdapter) orient(*mcpProc, string) (taskOutput, error) {
	return taskOutput{}, errNotSupported
}

func (lightpandaAdapter) concurrentOK() bool { return false }
