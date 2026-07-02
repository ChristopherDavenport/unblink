package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// unblinkProc drives the shipped unblink binary as a subprocess over stdio
// JSON-RPC (newline-delimited), the same way a real MCP client does — so we
// measure the actual published artifact, not an in-process build.
type unblinkProc struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	pid  int
	next int
}

func startUnblink(bin string) (*unblinkProc, time.Duration, error) {
	cmd := exec.Command(bin, "--js", "--allow-private", "--js-allow-private", "--log-level", "error")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	u := &unblinkProc{cmd: cmd, in: stdin, out: bufio.NewReader(stdout), pid: cmd.Process.Pid, next: 1}
	// initialize → response, then the initialized notification. The round-trip
	// time is the cold-start-to-ready figure.
	if _, err := u.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "membench", "version": "0"},
	}); err != nil {
		u.Close()
		return nil, 0, err
	}
	ready := time.Since(t0)
	if err := u.notify("notifications/initialized"); err != nil {
		u.Close()
		return nil, 0, err
	}
	return u, ready, nil
}

func (u *unblinkProc) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = u.in.Write(append(b, '\n'))
	return err
}

func (u *unblinkProc) notify(method string) error {
	return u.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

// call sends a request and reads until the matching id response, skipping
// notifications/logs the server may interleave.
func (u *unblinkProc) call(method string, params any) (json.RawMessage, error) {
	id := u.next
	u.next++
	if err := u.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		line, err := u.out.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.ID == nil || *msg.ID != id {
			continue // notification or a different id
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("rpc error: %s", string(msg.Error))
		}
		return msg.Result, nil
	}
}

// read drives one read tool call with render=true against url.
func (u *unblinkProc) read(url string) error {
	_, err := u.call("tools/call", map[string]any{
		"name": "read",
		"arguments": map[string]any{
			"url": url, "mode": "full", "render": true, "max_tokens": 6000,
		},
	})
	return err
}

// readTimed times one render — URL to agent-ready Markdown (fetch + parse +
// render + reduce + emit), the whole end-to-end round trip a tool call is.
func (u *unblinkProc) readTimed(url string) (time.Duration, error) {
	t0 := time.Now()
	err := u.read(url)
	return time.Since(t0), err
}

// readConcurrent pipelines n read requests (distinct ids) onto the single stdio
// connection, then drains n responses. The MCP server dispatches each request in
// its own goroutine, so the peak-RSS sampler sees n renders in flight at once —
// without racing goroutines on the shared reader/writer (which corrupts framing).
func (u *unblinkProc) readConcurrent(urls []string, n int) error {
	ids := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		id := u.next
		u.next++
		ids[id] = true
		url := urls[i%len(urls)]
		if err := u.send(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{
				"name":      "read",
				"arguments": map[string]any{"url": url, "mode": "full", "render": true, "max_tokens": 6000},
			},
		}); err != nil {
			return err
		}
	}
	for len(ids) > 0 {
		line, err := u.out.ReadBytes('\n')
		if err != nil {
			return err
		}
		var msg struct {
			ID *int `json:"id"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.ID == nil {
			continue
		}
		delete(ids, *msg.ID)
	}
	return nil
}

func (u *unblinkProc) Close() {
	_ = u.in.Close()
	if u.cmd.Process != nil {
		_ = u.cmd.Process.Kill()
	}
	_ = u.cmd.Wait()
}
