package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
)

func TestErrorResultIncludesCode(t *testing.T) {
	res := errorResult(&browser.Error{Code: browser.ErrBadInput, Message: "a url is required"})
	if !res.IsError {
		t.Error("IsError not set")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.HasPrefix(text, "error [bad_input]: a url is required") {
		t.Errorf("text = %q, want error [bad_input] prefix", text)
	}

	res = errorResult(&browser.Error{Code: browser.ErrTimeout, Retryable: true, Message: "deadline exceeded"})
	text = res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "[timeout]") || !strings.Contains(text, "transient") {
		t.Errorf("retryable text = %q, want code + transient hint", text)
	}
}

// Every tool must carry annotations so hosts can distinguish read-only fetches
// from state-changing actions (the human-in-the-loop contract).
func TestToolAnnotations(t *testing.T) {
	b, err := browser.New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := New(b, Config{SafeOutput: true, JSEnabled: true, SearchEnabled: true})

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	wantReadOnly := map[string]bool{
		"read": true, "browse": true, "links": true, "forms": true, "find": true,
		"controls": true, "data": true, "extract": true, "requests": true, "console": true,
		"site": true, "map": true, "search": true,
		"click": false, "submit_form": false, "interact": false,
		"session": false, "cookies": false,
	}
	seen := 0
	for _, tl := range tools.Tools {
		want, known := wantReadOnly[tl.Name]
		if !known {
			t.Errorf("unexpected tool %q", tl.Name)
			continue
		}
		seen++
		if tl.Annotations == nil {
			t.Errorf("tool %q has no annotations", tl.Name)
			continue
		}
		if tl.Annotations.ReadOnlyHint != want {
			t.Errorf("tool %q ReadOnlyHint = %v, want %v", tl.Name, tl.Annotations.ReadOnlyHint, want)
		}
	}
	if seen != len(wantReadOnly) {
		t.Errorf("saw %d known tools, want %d", seen, len(wantReadOnly))
	}
	for _, tl := range tools.Tools {
		switch tl.Name {
		case "submit_form", "interact":
			if tl.Annotations.DestructiveHint == nil || !*tl.Annotations.DestructiveHint {
				t.Errorf("tool %q should hint destructive", tl.Name)
			}
		case "click", "session":
			if tl.Annotations.DestructiveHint == nil || *tl.Annotations.DestructiveHint {
				t.Errorf("tool %q should hint non-destructive explicitly", tl.Name)
			}
		}
	}
}
