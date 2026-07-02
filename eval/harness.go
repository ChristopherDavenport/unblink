//go:build eval

// Package eval is a deterministic, offline, in-process eval harness for the
// unblink MCP server. It drives the *real* MCP server through the official
// SDK's in-memory transport against httptest fixtures and scores each tool over
// a fixture corpus, covering both MCP tool behaviour and reduction quality. It
// is opt-in (build tag "eval", run via `make eval`) so the unit suite stays
// fast and the eval set never gates `make test`.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/mcpserver"
	"github.com/christopherdavenport/unblink/internal/page"
)

// --- case / step / scorer model ---

// Step is one tool call in a case. When Path is non-empty it is appended to the
// fixture server's base URL and supplied as the "url" argument before the call.
type Step struct {
	Tool string
	Path string
	Args map[string]any
}

// Host builds the httptest handler a case runs against. Returning an error
// fails the case cleanly (e.g. a missing fixture file).
type Host func() (http.Handler, error)

// Case is a sequence of steps scored over the whole transcript. A case models a
// single flow (e.g. login -> submit -> click -> back), so session continuity is
// one case rather than many.
type Case struct {
	Name     string
	Host     Host
	Browser  []browser.Option
	Safe     bool // run with the production safe-output pipeline (framing + hidden-strip + defang)
	Steps    []Step
	Scorers  []Scorer
	Floor    float64 // case passes if its mean score >= Floor
	MustPass bool    // a failing must-pass case fails the whole gate
}

// Axis groups scorers by the quality dimension they validate, for the rollup.
type Axis string

const (
	AxisRegistration Axis = "registration"
	AxisJunk         Axis = "junk-stripping"
	AxisRecall       Axis = "content-recall"
	AxisToken        Axis = "token-budget"
	AxisPagination   Axis = "pagination-continuity"
	AxisStructure    Axis = "structured-extraction"
	AxisFind         Axis = "find-relevance"
	AxisSession      Axis = "session-state"
	AxisSite         Axis = "site-metadata"
	AxisRender       Axis = "js-render"
	AxisError        Axis = "error-path"
	AxisContent      Axis = "non-html-content"
	AxisAuth         Axis = "auth-credentials"
	AxisDiscovery    Axis = "search-discovery"
	AxisSafety       Axis = "safe-output"
)

// Scorer returns a partial-credit score in [0,1] with a one-line detail. A
// gradient (e.g. dropping one of three sections -> 0.67) is preferred over a
// binary pass/fail so a regression shows its magnitude.
type Scorer struct {
	Name string
	Axis Axis
	Fn   func(*Transcript) (float64, string)
}

// --- transcript ---

// StepResult records one executed step: the resolved args, the tool result, and
// any transport error. Tool-level failures surface as Result.IsError with a nil
// Err (the SDK contract), so scorers assert on IsError, not Err. Dur is the
// wall-clock time of the tool call — informational only, never scored or gated.
type StepResult struct {
	Tool   string
	Args   map[string]any
	Result *mcp.CallToolResult
	Err    error
	Dur    time.Duration
}

// Transcript is the scored record of a case run, plus the hooks pagination/
// replay scorers need to drive further tool calls against the same world.
type Transcript struct {
	Base  string
	Tools *mcp.ListToolsResult
	Steps []StepResult

	ctx  context.Context
	call func(ctx context.Context, tool string, args map[string]any) (*mcp.CallToolResult, error)
}

// at returns the step at index i (negative i counts from the end, -1 = last).
func (tr *Transcript) at(i int) (StepResult, bool) {
	if i < 0 {
		i += len(tr.Steps)
	}
	if i < 0 || i >= len(tr.Steps) {
		return StepResult{}, false
	}
	return tr.Steps[i], true
}

// replay drives an additional tool call against the same world (used by the
// pagination scorer to follow cursors).
func (tr *Transcript) replay(tool string, args map[string]any) (*mcp.CallToolResult, error) {
	return tr.call(tr.ctx, tool, args)
}

// --- world: real browser -> real mcpserver -> in-memory MCP client ---

type world struct {
	base      string
	server    *httptest.Server
	browser   *browser.Browser
	serverSes *mcp.ServerSession
	client    *mcp.ClientSession
}

// newWorld builds a fresh, isolated world: a real Browser (no page-cache or
// session bleed between cases), the real MCP adapter with the full tool set, an
// in-memory transport pair (server connected first, then client, per the SDK
// contract), and an httptest server for the case's fixture host.
func newWorld(ctx context.Context, host http.Handler, bopts []browser.Option, safe bool) (*world, error) {
	// Fixtures are httptest servers on loopback; allow the page-fetch SSRF guard to
	// reach them (a case can still re-disable it by appending WithAllowPrivate(false)).
	// Most cases validate extraction correctness against raw tool output, so the
	// untrusted-content safety pass (framing / hidden-strip / image-defang) is off
	// by default; safe cases run the full production pipeline instead.
	base := []browser.Option{browser.WithAllowPrivate(true), browser.WithSafeOutput(safe)}
	b, err := browser.New(append(base, bopts...)...)
	if err != nil {
		return nil, fmt.Errorf("browser.New: %w", err)
	}
	s := mcpserver.New(b, safe)

	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st)
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("server connect: %w", err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "unblink-eval", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		_ = ss.Close()
		b.Close()
		return nil, fmt.Errorf("client connect: %w", err)
	}

	srv := httptest.NewServer(host)
	return &world{base: srv.URL, server: srv, browser: b, serverSes: ss, client: cs}, nil
}

func (w *world) Close() {
	w.server.Close()
	_ = w.client.Close()
	_ = w.serverSes.Close()
	w.browser.Close()
}

func (w *world) call(ctx context.Context, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	return w.client.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
}

// run executes a case's steps against a fresh world and returns the transcript
// plus a cleanup func that tears the world down. The world must stay open while
// scorers run (the pagination scorer replays further tool calls), so the caller
// is responsible for invoking cleanup after scoring — not run itself. Each
// step's Path is resolved to a "url" argument before the call.
func run(ctx context.Context, c Case) (*Transcript, func(), error) {
	handler, err := c.Host()
	if err != nil {
		return nil, nil, fmt.Errorf("build host: %w", err)
	}
	w, err := newWorld(ctx, handler, c.Browser, c.Safe)
	if err != nil {
		return nil, nil, err
	}

	tools, err := w.client.ListTools(ctx, nil)
	if err != nil {
		w.Close()
		return nil, nil, fmt.Errorf("list tools: %w", err)
	}

	tr := &Transcript{Base: w.base, Tools: tools, ctx: ctx, call: w.call}
	for _, step := range c.Steps {
		args := map[string]any{}
		for k, v := range step.Args {
			args[k] = v
		}
		if step.Path != "" {
			args["url"] = w.base + step.Path
		}
		t0 := time.Now()
		res, err := w.call(ctx, step.Tool, args)
		tr.Steps = append(tr.Steps, StepResult{Tool: step.Tool, Args: args, Result: res, Err: err, Dur: time.Since(t0)})
	}
	return tr, w.Close, nil
}

// --- result decoding ---

// textOf concatenates the text content blocks of a tool result.
func textOf(r *mcp.CallToolResult) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// imageOf returns the first ImageContent block of a tool result, if any. The SDK
// decodes the base64 wire form back to raw bytes on the client, so ic.Data is the
// original image bytes — the basis for the binary-integrity ("no corruption")
// check.
func imageOf(r *mcp.CallToolResult) (*mcp.ImageContent, bool) {
	if r == nil {
		return nil, false
	}
	for _, c := range r.Content {
		if ic, ok := c.(*mcp.ImageContent); ok {
			return ic, true
		}
	}
	return nil, false
}

// into re-marshals a result's StructuredContent into v. The client receives
// StructuredContent as decoded JSON (map[string]any), so a marshal/unmarshal
// round-trip is the portable way to land it in a typed struct.
func into(r *mcp.CallToolResult, v any) error {
	if r == nil || r.StructuredContent == nil {
		return fmt.Errorf("result has no structured content")
	}
	data, err := json.Marshal(r.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// The link/form/find/session tools serialize unexported mcpserver structs; the
// harness mirrors their wire shape using the exported element types so the
// scorers can read them without reaching into mcpserver internals.

type linksOut struct {
	Count int         `json:"count"`
	Links []page.Link `json:"links"`
}

type formsOut struct {
	Count int         `json:"count"`
	Forms []page.Form `json:"forms"`
}

type findOut struct {
	Count int       `json:"count"`
	Hits  []dom.Hit `json:"hits"`
}

type sessionOut struct {
	Action  string                  `json:"action"`
	ID      string                  `json:"id,omitempty"`
	Closed  bool                    `json:"closed,omitempty"`
	State   *browser.SessionState   `json:"state,omitempty"`
	History *browser.SessionHistory `json:"history,omitempty"`
	Current *browser.BrowseResult   `json:"current,omitempty"`
}
