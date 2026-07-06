package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// unblinkAdapter drives the shipped bin/unblink binary — the actual published
// artifact, not an in-process build. All task calls use render:true so the JS
// engine runs on every page, matching what the browser-backed tools always do.
type unblinkAdapter struct{}

func (unblinkAdapter) name() string { return "unblink" }

func (unblinkAdapter) resolve(cfg toolConfig) (spawn, error) {
	bin := filepath.Join(cfg.root, "bin", "unblink")
	if _, err := os.Stat(bin); err != nil {
		return spawn{}, fmt.Errorf("bin/unblink not found at %s — run `make build` first", bin)
	}
	// Tool defaults throughout. unblink's per-host politeness limiter is opt-in
	// (--rate-limit, default off) precisely so this comparison is like-for-like:
	// when it defaulted to 5 req/s, the single-host cache-busted corpus paced
	// every render at ~200ms of token waiting and the published numbers measured
	// that crawl policy, not the engine.
	// "Binary on disk" must be the *shipped* artifact, so it compares
	// like-for-like with obscura's/lightpanda's downloaded release binaries. The
	// harness runs bin/unblink (an unstripped `make build`, ~38 MB) for the
	// footprint/latency passes — stripping doesn't change runtime cost — but the
	// size metric measures a stripped release-equivalent build (goreleaser uses
	// `CGO_ENABLED=0 -ldflags "-s -w"`), which is what a user actually downloads.
	sizeMB, note := releaseBinaryMB(cfg.root, bin)
	return spawn{
		cmd:         bin,
		args:        []string{"--allow-private", "--js-allow-private", "--log-level", "error"},
		installMB:   sizeMB,
		installNote: note,
	}, nil
}

// releaseBinaryMB builds a stripped release-equivalent binary (matching
// .goreleaser.yaml: CGO_ENABLED=0, -ldflags "-s -w") to a temp file and returns
// its size — the artifact a user downloads, not the unstripped dev build the
// harness runs. On any build failure it falls back to the dev binary's size so
// the run still reports a number.
func releaseBinaryMB(root, devBin string) (float64, string) {
	tmp, err := os.CreateTemp("", "unblink-release-*")
	if err != nil {
		return fileMB(devBin), "unstripped dev build (release build failed)"
	}
	out := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(out)

	cmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", out, "./cmd/unblink")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=auto")
	if err := cmd.Run(); err != nil {
		return fileMB(devBin), "unstripped dev build (release build failed)"
	}
	return fileMB(out), "stripped release build (goreleaser -s -w)"
}

func (unblinkAdapter) start(p *mcpProc) error { return requireTools(p, "read", "browse") }

// render matches the original membench read call — whole reduced page, JS on,
// the default 6000-token budget — so footprint/latency numbers stay comparable
// with previously published runs.
func (unblinkAdapter) render(p *mcpProc, url string) (string, error) {
	res, err := p.callTool("read", map[string]any{"url": url, "mode": "full", "render": true, "max_tokens": 6000})
	if err != nil {
		return "", err
	}
	return res.text, nil
}

// readArticle is unblink's default read: article-mode semantic reduction under
// the default token budget.
func (unblinkAdapter) readArticle(p *mcpProc, url string) (taskOutput, error) {
	return callText(p, "read", map[string]any{"url": url, "render": true})
}

// readFull reads the whole reduced page, following pagination cursors until
// the document is exhausted.
func (unblinkAdapter) readFull(p *mcpProc, url string) (taskOutput, error) {
	t0 := time.Now()
	var out taskOutput
	args := map[string]any{"url": url, "mode": "full", "render": true}
	for {
		res, err := p.callTool("read", args)
		if err != nil {
			return out, err
		}
		if res.isError {
			return out, fmt.Errorf("read returned an error result: %.120s", res.text)
		}
		out.calls++
		if out.text != "" {
			out.text += "\n"
		}
		out.text += res.text
		next := nextCursor(res.structured)
		if next == "" || out.calls >= 50 { // 50 pages = runaway guard, far above any fixture
			out.dur = time.Since(t0)
			return out, nil
		}
		args = map[string]any{"url": url, "mode": "full", "render": true, "cursor": next}
	}
}

// nextCursor pulls next_cursor from read's structured result.
func nextCursor(raw json.RawMessage) string {
	var v struct {
		NextCursor string `json:"next_cursor"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.NextCursor
}

func (unblinkAdapter) orient(p *mcpProc, url string) (taskOutput, error) {
	return callText(p, "browse", map[string]any{"url": url, "render": true})
}

func (unblinkAdapter) concurrentOK() bool { return true }

// pipelineRender puts len(urls) renders in flight at once on the single stdio
// connection (the server dispatches each request in its own goroutine).
func (unblinkAdapter) pipelineRender(p *mcpProc, urls []string) error {
	argsList := make([]map[string]any, len(urls))
	for i, u := range urls {
		argsList[i] = map[string]any{"url": u, "mode": "full", "render": true, "max_tokens": 6000}
	}
	return p.callToolsPipelined("read", argsList)
}
