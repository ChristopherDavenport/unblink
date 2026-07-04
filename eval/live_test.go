//go:build eval

package eval

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/mcpserver"
)

// TestLiveSmoke is an opt-in, non-gating smoke check against the real web:
// set UNBLINK_EVAL_LIVE=1 to run it (it is skipped otherwise, so `make eval`
// stays hermetic and CI never depends on network weather). It drives the real
// MCP server against example.com — the most stable page on the internet — to
// catch a pipeline break that fixtures can't (TLS, redirects, real headers).
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("UNBLINK_EVAL_LIVE") != "1" {
		t.Skip("live smoke eval is opt-in: set UNBLINK_EVAL_LIVE=1")
	}
	ctx := context.Background()

	b, err := browser.New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// Safe output on (production posture); this smoke test's browser has no JS/
	// search configured, so those tools gate off — it only drives read anyway.
	s := mcpserver.New(b, mcpserver.Config{SafeOutput: true})

	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "unblink-live-eval", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{
		"url": "https://example.com/", "mode": "full",
	}})
	if err != nil {
		t.Fatalf("read example.com: %v", err)
	}
	if res.IsError {
		t.Fatalf("read errored: %v", res.Content)
	}
	text := textOf(res)
	for _, want := range []string{"Example Domain", "[UNTRUSTED WEB CONTENT"} {
		if !strings.Contains(text, want) {
			t.Errorf("live read missing %q:\n%s", want, text)
		}
	}
}
