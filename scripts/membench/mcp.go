package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// callTimeout bounds every JSON-RPC round trip. A hung server (a beta engine
// wedged on a page) is killed so the run can continue with the next tool.
const callTimeout = 60 * time.Second

// mcpProc drives an MCP server binary as a subprocess over stdio JSON-RPC
// (newline-delimited), the same way a real MCP client does — so we measure the
// actual shipped artifact, not an in-process build.
type mcpProc struct {
	label  string // tool name, for log prefixes
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	pid    int
	next   int
	server serverInfo
}

// serverInfo is the server's self-identification from the initialize response.
type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// startMCP spawns the server, does the initialize handshake (timed — the
// round trip is the cold-start-to-ready figure), and sends the initialized
// notification. extraEnv entries are appended to the inherited environment.
func startMCP(label, cmdName string, args, extraEnv []string, verbose bool) (*mcpProc, time.Duration, error) {
	cmd := exec.Command(cmdName, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	if verbose {
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, 0, err
		}
		go func() {
			sc := bufio.NewScanner(stderr)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				fmt.Fprintf(os.Stderr, "[%s] %s\n", label, sc.Text())
			}
		}()
	}
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	p := &mcpProc{label: label, cmd: cmd, in: stdin, out: bufio.NewReader(stdout), pid: cmd.Process.Pid, next: 1}
	raw, err := p.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "membench", "version": "0"},
	})
	if err != nil {
		p.Close()
		return nil, 0, fmt.Errorf("initialize: %w", err)
	}
	ready := time.Since(t0)
	// Accept whatever protocol version the server negotiated down to; we only
	// need tools/list + tools/call, which every revision has.
	var init struct {
		ServerInfo serverInfo `json:"serverInfo"`
	}
	_ = json.Unmarshal(raw, &init)
	p.server = init.ServerInfo
	if err := p.notify("notifications/initialized"); err != nil {
		p.Close()
		return nil, 0, err
	}
	return p, ready, nil
}

func (p *mcpProc) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = p.in.Write(append(b, '\n'))
	return err
}

func (p *mcpProc) notify(method string) error {
	return p.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

// call sends a request and reads until the matching id response, skipping
// notifications/logs the server may interleave. A watchdog kills the child if
// no response arrives within callTimeout, unblocking the read loop with EOF.
func (p *mcpProc) call(method string, params any) (json.RawMessage, error) {
	id := p.next
	p.next++
	if err := p.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	stop := p.watchdog()
	defer stop()
	for {
		line, err := p.out.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.ID == nil || *msg.ID != id {
			continue // notification, log line, or a different id
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("rpc error: %s", string(msg.Error))
		}
		return msg.Result, nil
	}
}

func (p *mcpProc) watchdog() func() {
	t := time.AfterFunc(callTimeout, func() {
		fmt.Fprintf(os.Stderr, "membench: %s: no response within %s — killing\n", p.label, callTimeout)
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
	return func() { t.Stop() }
}

// toolResult is one tools/call outcome: the text the agent's context window
// would ingest, plus the structured result for adapter-side logic (cursors).
type toolResult struct {
	text       string
	structured json.RawMessage
	isError    bool
}

// callTool issues tools/call and extracts every content[].text block. A server
// that returns only structuredContent has that JSON counted as the payload —
// the meter measures whatever the agent actually reads.
func (p *mcpProc) callTool(tool string, args map[string]any) (toolResult, error) {
	raw, err := p.call("tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return toolResult{}, err
	}
	return parseToolResult(raw), nil
}

func parseToolResult(raw json.RawMessage) toolResult {
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return toolResult{text: string(raw)}
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if c.Text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(c.Text)
	}
	t := toolResult{text: sb.String(), structured: res.StructuredContent, isError: res.IsError}
	if t.text == "" && len(res.StructuredContent) > 0 {
		t.text = string(res.StructuredContent)
	}
	return t
}

// toolInfo is one tools/list entry.
type toolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (p *mcpProc) listTools() ([]toolInfo, error) {
	var all []toolInfo
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := p.call("tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []toolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
}

// callToolsPipelined writes len(argsList) tools/call requests (distinct ids)
// onto the single stdio connection, then drains the responses. The server
// dispatches each request in its own goroutine, so the peak-RSS sampler sees
// them all in flight at once — without racing goroutines on the shared
// reader/writer (which corrupts framing).
func (p *mcpProc) callToolsPipelined(tool string, argsList []map[string]any) error {
	ids := make(map[int]bool, len(argsList))
	for _, args := range argsList {
		id := p.next
		p.next++
		ids[id] = true
		if err := p.send(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": args},
		}); err != nil {
			return err
		}
	}
	stop := p.watchdog()
	defer stop()
	for len(ids) > 0 {
		line, err := p.out.ReadBytes('\n')
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

func (p *mcpProc) Close() {
	_ = p.in.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
}
