package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// Pinned via `-probe charlotte`: 0.6.3, default profile "browse" (23 tools,
// charlotte_-prefixed). charlotte_navigate returns the page representation at
// a chosen detail level: "minimal" (landmarks/headings/counts, the default),
// "summary" (content summaries + full element list), "full" (all text
// content).
const charlotteVersion = "0.6.3"

// charlotteAdapter drives Charlotte (npx @ticktockbent/charlotte): persistent
// real headless Chromium under a token-efficiency layer — typed page
// decomposition at three detail levels. PUPPETEER_EXECUTABLE_PATH points it
// at the same Chromium binary as the raw baseline.
type charlotteAdapter struct{}

func (charlotteAdapter) name() string { return "charlotte" }

func (charlotteAdapter) resolve(cfg toolConfig) (spawn, error) {
	if o := cfg.overrides["charlotte"]; o != "" {
		parts := strings.Fields(o)
		return spawn{cmd: parts[0], args: parts[1:]}, nil
	}
	if _, err := exec.LookPath("npx"); err != nil {
		return spawn{}, fmt.Errorf("npx not found (Node ≥ 20 required)")
	}
	sp := spawn{
		cmd:         "npx",
		args:        []string{"--yes", "@ticktockbent/charlotte@" + charlotteVersion},
		installNote: "npx package (excl. Node runtime)",
		warmup:      true,
	}
	if cfg.chromePath != "" {
		sp.env = append(sp.env, "PUPPETEER_EXECUTABLE_PATH="+cfg.chromePath)
	}
	return sp, nil
}

func (charlotteAdapter) start(p *mcpProc) error {
	return requireTools(p, "charlotte_navigate", "charlotte_observe")
}

// render is one navigate at full detail: the engine loads the page in
// Chromium and serializes all text content — the sentinel-verifiable unit of
// work, comparable to playwright's navigate+snapshot.
func (charlotteAdapter) render(p *mcpProc, url string) (string, error) {
	res, err := p.callTool("charlotte_navigate", map[string]any{"url": url, "detail": "full"})
	if err != nil {
		return "", err
	}
	if res.isError {
		return res.text, fmt.Errorf("charlotte_navigate error: %.120s", res.text)
	}
	return res.text, nil
}

// readArticle: navigate at "full" detail. Charlotte's "summary" level
// truncates prose (sentinel-verified: article text drops out on real
// articles), so the honest cost of actually reading a page's content is the
// full text serialization — Charlotte optimizes orientation and interaction,
// not article reading.
func (charlotteAdapter) readArticle(p *mcpProc, url string) (taskOutput, error) {
	return callText(p, "charlotte_navigate", map[string]any{"url": url, "detail": "full"})
}

// readFull: navigate at "full" detail — all text content.
func (charlotteAdapter) readFull(p *mcpProc, url string) (taskOutput, error) {
	return callText(p, "charlotte_navigate", map[string]any{"url": url, "detail": "full"})
}

// orient: navigate at its default minimal detail — landmarks, headings, and
// interactive-element counts, Charlotte's designed orientation pass.
func (charlotteAdapter) orient(p *mcpProc, url string) (taskOutput, error) {
	return callText(p, "charlotte_navigate", map[string]any{"url": url})
}

// concurrentOK: one persistent Chromium session — navigations share it.
func (charlotteAdapter) concurrentOK() bool { return false }
