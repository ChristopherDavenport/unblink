package browser_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
)

const (
	mdAccept      = "text/markdown, text/html;q=0.9, application/xhtml+xml;q=0.9, */*;q=0.8"
	defaultAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	mdBody        = "# Negotiated Title\r\n\r\nMARKDOWN-VARIANT body with a [link](/other).\r\n\r\n![beacon](https://tracker.example/p.gif)\r\n"
	htmlBody      = `<html><head><title>HTML Title</title><script src="/app.js"></script></head>` +
		`<body><main><h1>HTML Title</h1><p>HTML-VARIANT body, with enough prose to survive reduction intact.</p>` +
		`<a href="/other">other</a></main></body></html>`
)

// negotiatingOrigin serves /page as Markdown when the request prefers it and as
// HTML otherwise (like an edge "Markdown for Agents" feature), /html-only as HTML
// regardless, and /always.md as Markdown regardless. It records every request's
// Accept header by path.
type negotiatingOrigin struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (o *negotiatingOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	if o.seen == nil {
		o.seen = map[string][]string{}
	}
	o.seen[r.URL.Path] = append(o.seen[r.URL.Path], r.Header.Get("Accept"))
	o.mu.Unlock()
	wantsMD := strings.HasPrefix(r.Header.Get("Accept"), "text/markdown")
	switch r.URL.Path {
	case "/robots.txt", "/llms.txt", "/llms-full.txt":
		http.NotFound(w, r)
	case "/app.js":
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte("document.title = document.title;"))
	case "/moved":
		http.Redirect(w, r, "/page", http.StatusFound)
	case "/always.md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(mdBody))
	case "/page":
		w.Header().Set("Vary", "Accept")
		if wantsMD {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("X-Markdown-Tokens", "999999")
			_, _ = w.Write([]byte(mdBody))
			return
		}
		fallthrough
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(htmlBody))
	}
}

func (o *negotiatingOrigin) accepts(path string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.seen[path]...)
}

func (o *negotiatingOrigin) reset() {
	o.mu.Lock()
	o.seen = nil
	o.mu.Unlock()
}

func newNegotiating(t *testing.T, opts ...browser.Option) (*browser.Browser, *negotiatingOrigin, string) {
	t.Helper()
	origin := &negotiatingOrigin{}
	srv := httptest.NewServer(origin)
	t.Cleanup(srv.Close)
	b, err := browser.New(append([]browser.Option{browser.WithAllowPrivate(true)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b, origin, srv.URL
}

func mustRead(t *testing.T, b *browser.Browser, req browser.Request) *browser.ReadResult {
	t.Helper()
	r, err := b.Read(context.Background(), req, "", 0, "")
	if err != nil {
		t.Fatalf("read %+v: %v", req, err)
	}
	return r
}

func ptr(v bool) *bool { return &v }

// kindFor is the representation a read gets from the negotiating /page.
func kindFor(negotiates bool) (kind, accept, sentinel string) {
	if negotiates {
		return "markdown", mdAccept, "MARKDOWN-VARIANT"
	}
	return "html", defaultAccept, "HTML-VARIANT"
}

// A Browser built without WithPreferMarkdown follows DefaultPreferMarkdown, and
// behaves exactly like one given that value explicitly. Written against the
// constant so flipping the default does not need this test edited.
func TestPreferMarkdownDefaultFollowsConstant(t *testing.T) {
	wantKind, wantAccept, sentinel := kindFor(browser.DefaultPreferMarkdown)
	for name, opts := range map[string][]browser.Option{
		"unset":    nil,
		"explicit": {browser.WithPreferMarkdown(browser.DefaultPreferMarkdown)},
	} {
		b, origin, base := newNegotiating(t, opts...)
		r := mustRead(t, b, browser.Request{URL: base + "/page"})
		if r.Kind != wantKind || !strings.Contains(r.Markdown, sentinel) {
			t.Errorf("%s: kind = %q, markdown = %q; want %s", name, r.Kind, r.Markdown, wantKind)
		}
		if got := origin.accepts("/page"); len(got) != 1 || got[0] != wantAccept {
			t.Errorf("%s: Accept = %v, want [%q]", name, got, wantAccept)
		}
		// The per-call argument reaches both representations whatever the default.
		for _, want := range []bool{true, false} {
			k, _, _ := kindFor(want)
			if r := mustRead(t, b, browser.Request{URL: base + "/page", PreferMarkdown: ptr(want)}); r.Kind != k {
				t.Errorf("%s: prefer_markdown=%t read: kind = %q, want %s", name, want, r.Kind, k)
			}
		}
	}
}

// Explicitly off: the read's Accept is byte-identical to the browser default and
// the origin's HTML is what comes back.
func TestPreferMarkdownOffIsUnchanged(t *testing.T) {
	b, origin, base := newNegotiating(t, browser.WithPreferMarkdown(false))
	r := mustRead(t, b, browser.Request{URL: base + "/page"})
	if r.Kind != "html" || !strings.Contains(r.Markdown, "HTML-VARIANT") {
		t.Errorf("kind = %q, markdown = %q; want the HTML variant", r.Kind, r.Markdown)
	}
	for _, a := range origin.accepts("/page") {
		if a != defaultAccept {
			t.Errorf("Accept with the flag off = %q, want %q", a, defaultAccept)
		}
	}
}

// Receiving side is always on: a text/markdown response passes through unfenced
// (line endings normalised, image beacons still defanged), whatever was asked for.
func TestMarkdownResponsePassesThrough(t *testing.T) {
	b, _, base := newNegotiating(t, browser.WithPreferMarkdown(false))
	r := mustRead(t, b, browser.Request{URL: base + "/always.md"})
	if r.Kind != "markdown" || r.Mode != "markdown" {
		t.Errorf("kind = %q, mode = %q; want markdown/markdown", r.Kind, r.Mode)
	}
	if !strings.HasPrefix(r.Markdown, "# Negotiated Title\n\nMARKDOWN-VARIANT body with a [link](/other).\n") {
		t.Errorf("markdown not passed through unfenced with LF endings:\n%q", r.Markdown)
	}
	if strings.Contains(r.Markdown, "```") || strings.Contains(r.Markdown, "\r") {
		t.Errorf("markdown was fenced or kept CR:\n%q", r.Markdown)
	}
	if strings.Contains(r.Markdown, "![beacon](https://tracker.example/p.gif)") {
		t.Errorf("image beacon survived safe output:\n%q", r.Markdown)
	}

	// The document names itself in browse; HTML-only tools say what they need.
	br, err := b.Browse(context.Background(), browser.Request{URL: base + "/always.md"})
	if err != nil {
		t.Fatal(err)
	}
	if br.Title != "Negotiated Title" || br.Kind != "markdown" {
		t.Errorf("browse title = %q kind = %q", br.Title, br.Kind)
	}
	if _, err := b.Data(context.Background(), browser.Request{URL: base + "/always.md"}, "all"); err == nil || !strings.Contains(err.Error(), "got markdown") {
		t.Errorf("data on a markdown page: err = %v, want an HTML-required error naming markdown", err)
	}
	// find follows the existing non-HTML contract: no DOM, no hits, no error.
	if hits, err := b.Find(context.Background(), browser.Request{URL: base + "/always.md"}, "body", 5); err != nil || len(hits) != 0 {
		t.Errorf("find on a markdown page = %v, err = %v", hits, err)
	}
	if _, err := b.Read(context.Background(), browser.Request{URL: base + "/always.md", Format: "raw_html", Selector: "p"}, "", 0, ""); err == nil || !strings.Contains(err.Error(), "got markdown") {
		t.Errorf("raw_html selector on a markdown page: err = %v", err)
	}
	// format=text and selector-less raw_html return the source bytes, as for text.
	if rt := mustRead(t, b, browser.Request{URL: base + "/always.md", Format: "text"}); !strings.Contains(rt.Markdown, "MARKDOWN-VARIANT") {
		t.Errorf("format=text on markdown = %q", rt.Markdown)
	}
}

// Explicitly on: a stateless read negotiates and gets the Markdown variant (also across
// a redirect); no other request does.
func TestPreferMarkdownNegotiatesOnlyReads(t *testing.T) {
	b, origin, base := newNegotiating(t, browser.WithPreferMarkdown(true), browser.WithJS(2*time.Second), browser.WithJSAllowPrivate(true))
	ctx := context.Background()

	r := mustRead(t, b, browser.Request{URL: base + "/page", Render: true})
	if r.Kind != "markdown" || !strings.Contains(r.Markdown, "MARKDOWN-VARIANT") || strings.Contains(r.Markdown, "HTML-VARIANT") {
		t.Fatalf("kind = %q, markdown = %q; want the Markdown variant", r.Kind, r.Markdown)
	}
	if got := origin.accepts("/page"); len(got) != 1 || got[0] != mdAccept {
		t.Errorf("read Accept = %v, want exactly [%q]", got, mdAccept)
	}
	if got := origin.accepts("/app.js"); len(got) != 0 {
		t.Errorf("a Markdown response ran page JS: /app.js fetched %d time(s)", len(got))
	}

	// Redirect hops carry the negotiated Accept.
	origin.reset()
	if r := mustRead(t, b, browser.Request{URL: base + "/moved"}); r.Kind != "markdown" {
		t.Errorf("read across a redirect: kind = %q, want markdown", r.Kind)
	}
	if got := origin.accepts("/moved"); len(got) != 1 || got[0] != mdAccept {
		t.Errorf("redirecting hop Accept = %v", got)
	}
	if got := origin.accepts("/page"); len(got) != 1 || got[0] != mdAccept {
		t.Errorf("redirected hop Accept = %v", got)
	}

	// Every other tool, and every read that needs the HTML, gets HTML with the
	// default Accept — from the origin, not from the Markdown cache entry.
	origin.reset()
	if br, err := b.Browse(ctx, browser.Request{URL: base + "/page"}); err != nil || br.Kind != "html" || br.Counts.Links != 1 {
		t.Errorf("browse = %+v, err = %v; want the HTML page with its link", br, err)
	}
	if links, err := b.Links(ctx, browser.Request{URL: base + "/page"}, "", false); err != nil || len(links) != 1 {
		t.Errorf("links = %v, err = %v", links, err)
	}
	if rr := mustRead(t, b, browser.Request{URL: base + "/page", Format: "raw_html"}); rr.Kind != "html" || !strings.Contains(rr.Markdown, "<main>") {
		t.Errorf("raw_html read: kind = %q body = %q", rr.Kind, rr.Markdown)
	}
	if rr := mustRead(t, b, browser.Request{URL: base + "/page", Render: true, WaitFor: "main"}); rr.Kind != "html" {
		t.Errorf("wait_for read: kind = %q, want html", rr.Kind)
	}
	if rr := mustRead(t, b, browser.Request{URL: base + "/page", PreferMarkdown: ptr(false)}); rr.Kind != "html" {
		t.Errorf("prefer_markdown=false read: kind = %q, want html", rr.Kind)
	}
	// A session navigation does not negotiate by default: its page is shared with
	// links/click/forms.
	if rr := mustRead(t, b, browser.Request{SessionID: "s1", URL: base + "/page"}); rr.Kind != "html" {
		t.Errorf("session read: kind = %q, want html", rr.Kind)
	}
	for path, accepts := range map[string][]string{
		"/page": origin.accepts("/page"), "/app.js": origin.accepts("/app.js"),
		"/robots.txt": origin.accepts("/robots.txt"), "/llms.txt": origin.accepts("/llms.txt"),
	} {
		for _, a := range accepts {
			if strings.Contains(a, "markdown") {
				t.Errorf("%s was requested with a Markdown Accept: %q", path, a)
			}
		}
	}
	if len(origin.accepts("/app.js")) == 0 {
		t.Error("the rendered HTML reads never fetched /app.js; the subresource check proved nothing")
	}

	// The Markdown entry is still cached under its own key: no new request.
	origin.reset()
	if rr := mustRead(t, b, browser.Request{URL: base + "/page", Render: true}); rr.Kind != "markdown" {
		t.Errorf("repeat read: kind = %q, want markdown", rr.Kind)
	}
	if got := origin.accepts("/page"); len(got) != 0 {
		t.Errorf("repeat Markdown read refetched: %v", got)
	}
}

// An origin with no Markdown representation answers the negotiating read exactly
// as before: HTML, reduced as usual.
func TestPreferMarkdownFallsBackToHTML(t *testing.T) {
	b, origin, base := newNegotiating(t, browser.WithPreferMarkdown(true))
	r := mustRead(t, b, browser.Request{URL: base + "/html-only"})
	if r.Kind != "html" || r.Mode == "markdown" || !strings.Contains(r.Markdown, "HTML-VARIANT") {
		t.Errorf("kind = %q mode = %q markdown = %q", r.Kind, r.Mode, r.Markdown)
	}
	if got := origin.accepts("/html-only"); len(got) != 1 || got[0] != mdAccept {
		t.Errorf("Accept = %v", got)
	}
}

// The per-call argument needs no operator flag: with the flag explicitly off it
// turns negotiation on, including in a session; the session's later tools then
// see a Markdown page and say so.
func TestPreferMarkdownPerCall(t *testing.T) {
	b, origin, base := newNegotiating(t, browser.WithPreferMarkdown(false))
	ctx := context.Background()

	if r := mustRead(t, b, browser.Request{URL: base + "/page", PreferMarkdown: ptr(true)}); r.Kind != "markdown" {
		t.Errorf("stateless per-call: kind = %q, want markdown", r.Kind)
	}
	// The default-Accept read of the same URL is a separate cache entry.
	if r := mustRead(t, b, browser.Request{URL: base + "/page"}); r.Kind != "html" {
		t.Errorf("default read after a negotiated one: kind = %q, want html", r.Kind)
	}

	origin.reset()
	if r := mustRead(t, b, browser.Request{SessionID: "s", URL: base + "/page", PreferMarkdown: ptr(true)}); r.Kind != "markdown" {
		t.Errorf("session per-call: kind = %q, want markdown", r.Kind)
	}
	if got := origin.accepts("/page"); len(got) != 1 || got[0] != mdAccept {
		t.Errorf("session per-call Accept = %v", got)
	}
	cur := browser.Request{SessionID: "s", UseCurrent: true}
	if r := mustRead(t, b, cur); r.Kind != "markdown" || !strings.Contains(r.Markdown, "MARKDOWN-VARIANT") {
		t.Errorf("re-read of the session's current page: kind = %q", r.Kind)
	}
	if links, err := b.Links(ctx, cur, "", false); err != nil || len(links) != 0 {
		t.Errorf("links on a Markdown current page = %v, err = %v; want none, no error", links, err)
	}
	if _, err := b.Data(ctx, cur, "all"); err == nil || !strings.Contains(err.Error(), "got markdown") {
		t.Errorf("data on a Markdown current page: err = %v", err)
	}
	if _, err := b.Click(ctx, "s", 0, "", false); err == nil {
		t.Error("click on a Markdown current page succeeded; want a clear error")
	}
	// Reading the current page cannot change its representation: asking for HTML
	// there is refused with the way to get it, never answered with the Markdown.
	_, err := b.Read(ctx, browser.Request{SessionID: "s", UseCurrent: true, PreferMarkdown: ptr(false)}, "", 0, "")
	if err == nil || !strings.Contains(err.Error(), "fetched as Markdown") || !strings.Contains(err.Error(), base+"/page") {
		t.Errorf("prefer_markdown=false on a Markdown current page: err = %v", err)
	}
	if r := mustRead(t, b, browser.Request{SessionID: "s", UseCurrent: true, PreferMarkdown: ptr(true)}); r.Kind != "markdown" {
		t.Errorf("prefer_markdown=true on a Markdown current page: kind = %q", r.Kind)
	}
	// prefer_markdown=false with the url refetches the same URL as HTML…
	origin.reset()
	if r := mustRead(t, b, browser.Request{SessionID: "s", URL: base + "/page", PreferMarkdown: ptr(false)}); r.Kind != "html" || !strings.Contains(r.Markdown, "HTML-VARIANT") {
		t.Errorf("session prefer_markdown=false after a Markdown read: kind = %q", r.Kind)
	}
	if got := origin.accepts("/page"); len(got) != 1 || got[0] != defaultAccept {
		t.Errorf("session HTML refetch Accept = %v, want one default-Accept request", got)
	}
	if links, err := b.Links(ctx, cur, "", false); err != nil || len(links) != 1 {
		t.Errorf("links after the HTML refetch = %v, err = %v; want the page's link", links, err)
	}
	_, err = b.Read(ctx, browser.Request{SessionID: "s", UseCurrent: true, PreferMarkdown: ptr(true)}, "", 0, "")
	if err == nil || !strings.Contains(err.Error(), "fetched as HTML") {
		t.Errorf("prefer_markdown=true on an HTML current page: err = %v", err)
	}
	// …and the reverse: true with the url refetches it as Markdown.
	origin.reset()
	if r := mustRead(t, b, browser.Request{SessionID: "s", URL: base + "/page", PreferMarkdown: ptr(true)}); r.Kind != "markdown" {
		t.Errorf("session prefer_markdown=true after an HTML read: kind = %q", r.Kind)
	}
	if got := origin.accepts("/page"); len(got) != 1 || got[0] != mdAccept {
		t.Errorf("session Markdown refetch Accept = %v", got)
	}
}

// Stateless, both orders and both operator settings: each explicit value always
// returns its own representation of the same URL, never the other one's cache
// entry, and each representation is fetched from the origin exactly once.
func TestPreferMarkdownExplicitNeverCrosses(t *testing.T) {
	for _, operator := range []bool{true, false} {
		for _, first := range []bool{true, false} {
			b, origin, base := newNegotiating(t, browser.WithPreferMarkdown(operator))
			for i, want := range []bool{first, !first, first, !first} {
				kind, _, sentinel := kindFor(want)
				r := mustRead(t, b, browser.Request{URL: base + "/page", PreferMarkdown: ptr(want)})
				if r.Kind != kind || !strings.Contains(r.Markdown, sentinel) {
					t.Errorf("operator=%t first=%t step %d: prefer_markdown=%t gave kind %q", operator, first, i, want, r.Kind)
				}
			}
			got := origin.accepts("/page")
			var md, html int
			for _, a := range got {
				switch a {
				case mdAccept:
					md++
				case defaultAccept:
					html++
				}
			}
			if len(got) != 2 || md != 1 || html != 1 {
				t.Errorf("operator=%t first=%t: origin saw %v, want one request per representation", operator, first, got)
			}
		}
	}
}
