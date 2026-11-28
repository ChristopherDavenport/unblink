package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// Under --respect-robots a disallowed URL comes back as a tool error that says
// what happened, who decided, and that a retry is pointless; the site tool says
// the rules are enforced.
func TestRobotsRefusalToolError(t *testing.T) {
	var privateHits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "User-agent: *\nDisallow: /private\n")
		case "/llms.txt", "/llms-full.txt":
			http.NotFound(w, r)
		default:
			if strings.HasPrefix(r.URL.Path, "/private") {
				atomic.AddInt64(&privateHits, 1)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html><head><title>T</title></head><body><p>an allowed page body with enough prose to keep.</p></body></html>")
		}
	}))
	defer srv.Close()

	b, err := browser.New(browser.WithAllowPrivate(true), browser.WithRespectRobots(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := New(b, Config{SafeOutput: true})

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	call := func(tool string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		var text strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		return text.String(), res.IsError
	}

	text, isErr := call("read", map[string]any{"url": srv.URL + "/private/doc"})
	if !isErr {
		t.Fatalf("read of a disallowed URL was not a tool error: %q", text)
	}
	for _, want := range []string{"error [robots_disallowed]:", srv.URL + "/private/doc",
		"disallowed by the site's robots.txt", "--respect-robots", "retrying will not help"} {
		if !strings.Contains(text, want) {
			t.Errorf("tool error %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, "transient") {
		t.Errorf("a robots refusal was reported as retryable: %q", text)
	}
	if n := atomic.LoadInt64(&privateHits); n != 0 {
		t.Errorf("the disallowed URL was requested %d time(s)", n)
	}

	if text, isErr := call("read", map[string]any{"url": srv.URL + "/public"}); isErr || !strings.Contains(text, "an allowed page body") {
		t.Errorf("read of an allowed URL: isError=%v text=%q", isErr, text)
	}
	if text, isErr := call("site", map[string]any{"url": srv.URL + "/private/doc"}); isErr ||
		!strings.Contains(text, `"enforced": true`) || !strings.Contains(text, `"allowed_for_us": false`) {
		t.Errorf("site on a disallowed URL: isError=%v text=%q", isErr, text)
	}
}
