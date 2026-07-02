package mcpserver

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/browser"
)

func TestFilesOf(t *testing.T) {
	// Happy path: text and base64 content, default filename, MIME carried.
	parts, err := filesOf([]fileArg{
		{Field: "doc", Filename: "a.txt", MIME: "text/plain", Content: "hello"},
		{Field: "img", ContentBase64: base64.StdEncoding.EncodeToString([]byte{0x89, 0x50})},
	})
	if err != nil {
		t.Fatalf("filesOf: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("len = %d, want 2", len(parts))
	}
	if parts[0].Filename != "a.txt" || parts[0].MIME != "text/plain" || string(parts[0].Data) != "hello" {
		t.Errorf("part 0 = %+v", parts[0])
	}
	if parts[1].Filename != "upload.bin" || string(parts[1].Data) != "\x89P" {
		t.Errorf("part 1 = %+v (default filename + decoded bytes)", parts[1])
	}

	// Empty input is a clean nil.
	if parts, err := filesOf(nil); err != nil || parts != nil {
		t.Errorf("filesOf(nil) = %v, %v", parts, err)
	}

	for name, args := range map[string][]fileArg{
		"missing field": {{Content: "x"}},
		"both contents": {{Field: "f", Content: "x", ContentBase64: "eA=="}},
		"bad base64":    {{Field: "f", ContentBase64: "!!not-base64!!"}},
	} {
		if _, err := filesOf(args); err == nil {
			t.Errorf("%s: want error", name)
		}
	}

	// Caps: file count and total bytes.
	many := make([]fileArg, maxUploadFiles+1)
	for i := range many {
		many[i] = fileArg{Field: "f", Content: "x"}
	}
	if _, err := filesOf(many); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("count cap: err = %v", err)
	}
	big := strings.Repeat("a", maxUploadBytes/2+1)
	if _, err := filesOf([]fileArg{
		{Field: "a", Content: big}, {Field: "b", Content: big},
	}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("size cap: err = %v", err)
	}
}

// frame wraps content in the untrusted fence with an unpredictable per-call
// token; with safe output off it is the identity.
func TestFrame(t *testing.T) {
	safe := &Server{safeOutput: true}
	out := safe.frame("PAGE CONTENT")
	if !strings.Contains(out, "[UNTRUSTED WEB CONTENT") || !strings.Contains(out, "PAGE CONTENT") {
		t.Fatalf("framed output missing banner or content:\n%s", out)
	}
	if n := strings.Count(out, "«untrusted"); n != 3 {
		t.Errorf("fence token occurrences = %d, want 3 (named once, brackets twice)", n)
	}
	// Two calls must not share a fence token (page text cannot pre-forge it).
	tok := func(s string) string {
		i := strings.Index(s, "«untrusted:")
		j := strings.Index(s[i:], "»")
		return s[i : i+j+len("»")]
	}
	if tok(out) == tok(safe.frame("PAGE CONTENT")) {
		t.Error("fence token is predictable across calls")
	}

	unsafe := &Server{safeOutput: false}
	if got := unsafe.frame("PAGE CONTENT"); got != "PAGE CONTENT" {
		t.Errorf("safe output off must be identity, got %q", got)
	}
}

// The session tool's argument validation errors cleanly (IsError, bad_input)
// without touching the network.
func TestHandleSessionArgErrors(t *testing.T) {
	b, err := browser.New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := New(b, false)
	ctx := context.Background()

	for name, args := range map[string]sessionArgs{
		"unknown action":       {Action: "teleport"},
		"credentials need url": {Action: "new", Auth: &authArg{Token: "t"}},
	} {
		res, _, err := s.handleSession(ctx, nil, args)
		if err != nil {
			t.Fatalf("%s: transport err %v", name, err)
		}
		if !res.IsError {
			t.Errorf("%s: want IsError", name)
		}
	}
}
