package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// A paginated read says how much of the document the page is and how to go on;
// and a page whose body is one long list does not open with a first page that
// holds only what came before the list.
func TestReadContinuationNote(t *testing.T) {
	var items strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&items, "<li>ENTRY %d: a reference entry with enough words in it to carry some weight</li>", i)
	}
	body := `<html><head><title>Reference</title></head><body><main><h1>Reference</h1><p>Short introduction.</p><ul>` +
		items.String() + `</ul></main></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
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

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{
		"url": srv.URL + "/", "mode": "full", "render": false, "max_tokens": 1000,
	}})
	if err != nil || res.IsError {
		t.Fatalf("read: %v %v", err, res)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	out := text.String()
	sc, _ := res.StructuredContent.(map[string]any)

	if !strings.Contains(out, "ENTRY 0:") || !strings.Contains(out, "ENTRY 20:") {
		t.Errorf("first page stops before the list instead of running into it:\n%s", tail(out))
	}
	total, _ := sc["total_tokens"].(float64)
	if total < 5000 {
		t.Errorf("total_tokens = %v, want the whole document's estimate", sc["total_tokens"])
	}
	for _, want := range []string{
		"_Page 1 of ", " of a document of about ", " tokens — the content above is incomplete.",
		"call read again with cursor ", "use find (text search) or browse (heading outline)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("continuation note missing %q:\n%s", want, tail(out))
		}
	}
	if got := groupThousands(int(total)); !strings.Contains(out, got) {
		t.Errorf("note does not carry the total %s:\n%s", got, tail(out))
	}
}

func TestGroupThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 28452: "28,452", 1234567: "1,234,567"} {
		if got := groupThousands(n); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", n, got, want)
		}
	}
}

// An article that holds only part of the page's main content says so, and says
// how to get the rest; an article that is the page says nothing.
func TestReadArticleCoverageNote(t *testing.T) {
	var art, rest strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&art, "<p>ARTICLE paragraph %d explains, at some length and in plain sentences, what the reader came here to learn about the subject at hand.</p>", i)
	}
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&rest, "<p>Brief %d: a short unrelated item that sits beside the lead story.</p>", i)
	}
	pages := map[string]string{
		"/part":  `<html><head><title>Lead</title></head><body><main><article><h1>Lead story</h1>` + art.String() + `</article><div class="sidebar related">` + rest.String() + `</div></main></body></html>`,
		"/whole": `<html><head><title>Lead</title></head><body><main><article><h1>Lead story</h1>` + art.String() + `</article></main></body></html>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, pages[r.URL.Path])
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

	read := func(path string) (string, map[string]any) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{"url": srv.URL + path, "render": false}})
		if err != nil || res.IsError {
			t.Fatalf("read %s: %v %v", path, err, res)
		}
		var text strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		sc, _ := res.StructuredContent.(map[string]any)
		return text.String(), sc
	}

	text, sc := read("/part")
	if sc["mode"] != "article" {
		t.Fatalf("mode = %v, want article (this test needs readability to pick the article)", sc["mode"])
	}
	cov, _ := sc["article_coverage"].(float64)
	if strings.Contains(text, "Brief 3:") {
		if cov != 0 {
			t.Errorf("article holds the briefs yet reports coverage %v", cov)
		}
		t.Skip("readability kept the briefs on this fixture; nothing left out to report")
	}
	if cov <= 0.5 || cov >= 0.85 {
		t.Errorf("article_coverage = %v, want between 0.5 and 0.85", cov)
	}
	if !strings.Contains(text, "% of its main content") || !strings.Contains(text, "Call read with mode=full for all of it.") {
		t.Errorf("no coverage note:\n%s", tail(text))
	}

	text, sc = read("/whole")
	if _, ok := sc["article_coverage"]; ok || strings.Contains(text, "of its main content") {
		t.Errorf("a whole article carries a coverage note: %v\n%s", sc["article_coverage"], tail(text))
	}
}
