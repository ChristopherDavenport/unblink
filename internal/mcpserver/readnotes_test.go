package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// read tells the agent, in the trusted text after the fenced content, when it
// did not get what it asked for: an article request answered with the page's
// main region (a hub page), and a JavaScript render that was discarded because
// it destroyed the server-rendered page.
func TestReadNotesForFallbacks(t *testing.T) {
	var river, prose strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&river, `<div><h2><a href="/s/%d">STORY headline number %d about an event that happened today</a></h2></div>`, i, i)
		fmt.Fprintf(&prose, "<p>GUIDE paragraph %d of the server-rendered guide, which explains one step of the procedure in a full sentence.</p>", i)
	}
	pages := map[string]string{
		"/hub": `<html><head><title>Latest</title></head><body><main><h1>Latest</h1>` + river.String() + `</main>
			<footer><div><p>The Daily Example has been separating the signal from the noise for over twenty-five years, with a unique combination of technical savvy and wide-ranging interest in the arts and sciences, and remains a trusted source in a sea of information for its readers.</p></div></footer></body></html>`,
		"/crash": `<html><head><title>Guide</title></head><body><main><h1>Guide</h1>` + prose.String() + `</main>
			<script>document.querySelector('main').innerHTML = '<h1>This page crashed.</h1>';</script></body></html>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	b, err := browser.New(browser.WithAllowPrivate(true), browser.WithJS(3*time.Second))
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

	read := func(args map[string]any) (string, map[string]any) {
		t.Helper()
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
			t.Fatalf("read error: %s", text.String())
		}
		structured, _ := res.StructuredContent.(map[string]any)
		return text.String(), structured
	}

	text, sc := read(map[string]any{"url": srv.URL + "/hub", "render": false})
	if !strings.Contains(text, "STORY headline number 39") {
		t.Errorf("hub content missing")
	}
	if !strings.Contains(text, "the page is a hub or listing, not a single article") || !strings.Contains(text, "this is the page's main region") {
		t.Errorf("hub read carries no fallback note:\n%s", tail(text))
	}
	if sc["mode"] != "main" || sc["article_fallback"] != true || sc["article_fallback_reason"] != "partial_article" {
		t.Errorf("structured: mode=%v fallback=%v reason=%v", sc["mode"], sc["article_fallback"], sc["article_fallback_reason"])
	}

	text, sc = read(map[string]any{"url": srv.URL + "/crash", "mode": "full"})
	if !strings.Contains(text, "GUIDE paragraph 39") || strings.Contains(text, "This page crashed") {
		t.Errorf("crashed render was not replaced by the server page:\n%s", tail(text))
	}
	if !strings.Contains(text, "its result was discarded: this is the page as the server sent it") {
		t.Errorf("discarded render carries no note:\n%s", tail(text))
	}
	if sc["render_discarded"] != true {
		t.Errorf("structured: render_discarded=%v", sc["render_discarded"])
	}
}

func tail(s string) string {
	if len(s) > 600 {
		return "…" + s[len(s)-600:]
	}
	return s
}
