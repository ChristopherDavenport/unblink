//go:build corpus

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/christopherdavenport/unblink/corpus"
)

// charlotteRunner drives Charlotte against the replay proxy. Charlotte has no
// option for Chromium launch arguments, so PUPPETEER_EXECUTABLE_PATH points at
// a generated wrapper that execs the real binary with the proxy flags added —
// the tool itself is run unmodified, at its pinned version and defaults.
type charlotteRunner struct {
	cfg   toolConfig
	sp    spawn
	proxy *corpus.Proxy
	tmp   string
}

func newCharlotteRunner(cfg toolConfig) (*charlotteRunner, error) {
	if cfg.chromePath == "" {
		return nil, fmt.Errorf("no Chromium found (pass -chrome, or CHROME=/path to make)")
	}
	sp, err := charlotteAdapter{}.resolve(cfg)
	if err != nil {
		return nil, err
	}
	proxy, err := corpus.NewProxy()
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "corpus-compare-*")
	if err != nil {
		proxy.Close()
		return nil, err
	}
	// --ignore-certificate-errors is what lets the proxy terminate TLS with its
	// throwaway CA; it is safe here only because every byte the browser can
	// reach is a recorded one. QUIC is off because the proxy speaks HTTP/1.1.
	wrapper := filepath.Join(tmp, "chrome-via-replay")
	script := "#!/bin/sh\nexec " + shQuote(cfg.chromePath) +
		" --proxy-server=http://" + proxy.Addr() +
		" --ignore-certificate-errors --disable-quic \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		proxy.Close()
		return nil, err
	}
	sp.env = append(sp.env, "PUPPETEER_EXECUTABLE_PATH="+wrapper)
	c := &charlotteRunner{cfg: cfg, sp: sp, proxy: proxy, tmp: tmp}
	if sp.warmup {
		// Populate the npx cache before anything is timed.
		if w, _, err := startMCP("charlotte", sp.cmd, sp.args, sp.env, false); err == nil {
			closeTree(w)
		} else {
			c.Close()
			return nil, fmt.Errorf("launch failed: %w", err)
		}
	}
	return c, nil
}

func (c *charlotteRunner) Close() {
	c.proxy.Close()
	_ = os.RemoveAll(c.tmp)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// session starts a fresh Charlotte (and so a fresh Chromium profile) whose
// only network is the page's replay.
func (c *charlotteRunner) session(p *corpus.ComparePage) (*mcpProc, *corpus.CompareReplay, error) {
	rp := p.NewReplay()
	c.proxy.SetRoundTripper(rp)
	proc, _, err := startMCP("charlotte", c.sp.cmd, c.sp.args, c.sp.env, c.cfg.verbose)
	if err != nil {
		return nil, nil, err
	}
	if err := (charlotteAdapter{}).start(proc); err != nil {
		closeTree(proc)
		return nil, nil, err
	}
	return proc, rp, nil
}

func (c *charlotteRunner) record(p *corpus.ComparePage, rp *corpus.CompareReplay, text string, dur time.Duration, err error) ccRunRec {
	st := rp.Stats()
	cost := charlotteWithoutDiagnostics(text)
	rec := ccRunRec{
		TextScore: p.Score(charlotteReadable(text)), Tokens: countTokens(cost), Bytes: len(cost),
		TokensRaw: countTokens(text),
		LatencyMS: dur.Milliseconds(), Calls: 1,
		Requests: st.Requests, Unmatched: st.Unmatched + st.CaptureNoBody, Consequential: st.MissConsequential,
		Background: c.proxy.Background(),
	}
	if err != nil {
		rec = ccRunRec{Err: err.Error(), LatencyMS: dur.Milliseconds(), Calls: 1, Requests: st.Requests,
			Unmatched: st.Unmatched + st.CaptureNoBody, Consequential: st.MissConsequential}
		rec.Scored = p.Scorable()
		return rec
	}
	rec.Links, rec.Forms = charlotteCounts(text)
	rec.MissKinds, rec.MissSample = st.MissKinds, missSample(st)
	rec.Excused = corpus.Excused(rec.Recall, st)
	return rec
}

// run is one fresh-session navigate at Charlotte's default detail (minimal).
func (c *charlotteRunner) run(p *corpus.ComparePage, mode string) (ccRunRec, string) {
	proc, rp, err := c.session(p)
	if err != nil {
		return ccRunRec{Err: "launch: " + err.Error(), Scored: p.Scorable()}, ""
	}
	defer closeTree(proc)
	args := map[string]any{"url": p.URL}
	if mode != "default" {
		args["detail"] = mode
	}
	t0 := time.Now()
	out, err := callText(proc, "charlotte_navigate", args)
	return c.record(p, rp, out.text, time.Since(t0), err), out.text
}

// runFull is one fresh-session navigate at detail=full, then — on the page that
// call left loaded — an untimed observe at detail=summary, so the middle level
// is scored without a third browser launch per run.
func (c *charlotteRunner) runFull(p *corpus.ComparePage) (full ccRunRec, fullText string, sum ccRunRec, sumText string) {
	proc, rp, err := c.session(p)
	if err != nil {
		e := ccRunRec{Err: "launch: " + err.Error(), Scored: p.Scorable()}
		return e, "", e, ""
	}
	defer closeTree(proc)
	t0 := time.Now()
	out, err := callText(proc, "charlotte_navigate", map[string]any{"url": p.URL, "detail": "full"})
	full = c.record(p, rp, out.text, time.Since(t0), err)
	if err != nil {
		sum = full
		return full, out.text, sum, ""
	}
	so, serr := callText(proc, "charlotte_observe", map[string]any{"detail": "summary"})
	sum = c.record(p, rp, so.text, 0, serr)
	return full, out.text, sum, so.text
}

// closeTree shuts a tool down with its whole process tree. Charlotte is
// npx -> node -> Chromium: killing only the top process orphans the browser.
func closeTree(p *mcpProc) {
	pids := procTree(p.pid)
	_ = p.in.Close()
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	<-done
}
