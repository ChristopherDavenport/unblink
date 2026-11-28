package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// read's prefer_markdown is tri-state over MCP: true and false choose the
// representation for that call with no operator flag, and an absent argument
// follows browser.DefaultPreferMarkdown. The schema advertises it.
func TestReadPreferMarkdownArg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt", "/llms.txt", "/llms-full.txt":
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Accept"), "text/markdown") {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = io.WriteString(w, "# Doc\n\nMARKDOWN-VARIANT\n")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><head><title>Doc</title></head><body><p>HTML-VARIANT body with enough prose to keep through reduction.</p></body></html>")
	}))
	defer srv.Close()

	b, err := browser.New(browser.WithAllowPrivate(true))
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

	read := func(args map[string]any) string {
		t.Helper()
		args["url"] = srv.URL + "/doc"
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: args})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var text strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		if res.IsError {
			t.Fatalf("read %v: tool error %q", args, text.String())
		}
		return text.String()
	}
	sentinel := func(markdown bool) (want, not string) {
		if markdown {
			return "MARKDOWN-VARIANT", "HTML-VARIANT"
		}
		return "HTML-VARIANT", "MARKDOWN-VARIANT"
	}
	check := func(name, text string, markdown bool) {
		t.Helper()
		want, not := sentinel(markdown)
		if !strings.Contains(text, want) || strings.Contains(text, not) {
			t.Errorf("%s: got %q, want the %s", name, text, want)
		}
	}
	check("prefer_markdown=true", read(map[string]any{"prefer_markdown": true}), true)
	check("prefer_markdown=false", read(map[string]any{"prefer_markdown": false}), false)
	check("prefer_markdown=true again", read(map[string]any{"prefer_markdown": true}), true)
	check("absent", read(map[string]any{}), browser.DefaultPreferMarkdown)

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "read" {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(schema), `"prefer_markdown"`) {
			t.Errorf("read's input schema does not advertise prefer_markdown: %s", schema)
		}
	}
}
