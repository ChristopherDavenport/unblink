package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	// --rate-limit 0 disables unblink's default politeness limiter (5 req/s per
	// host). Every fixture is served from one loopback host and the perf pass
	// cache-busts each page URL, so with the default limit the latency medians
	// measure token pacing (~200ms/request), not engine capability. No other
	// benchmarked tool ships a crawl-politeness limiter, so leaving it on would
	// compare unblink's crawl policy against their absence of one. Production
	// defaults are unchanged.
	return spawn{
		cmd:       bin,
		args:      []string{"--js", "--allow-private", "--js-allow-private", "--rate-limit", "0", "--log-level", "error"},
		installMB: fileMB(bin),
	}, nil
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
