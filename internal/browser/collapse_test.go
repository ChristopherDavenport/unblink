package browser_test

// Tests for the render-collapse guard (ADR 0023): a JavaScript render that
// destroys a server-rendered page is discarded in favour of the server's HTML,
// and legitimate renders that merely shorten or rearrange a page are kept.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// ssrProse is n paragraphs of server-rendered text, ~150 bytes each.
func ssrProse(tag string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "<p>%s paragraph %d of the server-rendered guide, which explains one step of the procedure in a full sentence.</p>\n", tag, i)
	}
	return b.String()
}

// ssrPage is a server-rendered docs page inside a site layout; script runs at
// the end of the body. The layout's navigation is large on purpose: an error
// boundary leaves it standing, and it must not make a lost page look kept.
func ssrPage(script string) string {
	var nav strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&nav, `<a href="/docs/%d">Reference section %d</a> `, i, i)
	}
	return `<!doctype html><html><head><title>Guide</title></head><body>
	<nav>` + nav.String() + `</nav>
	<div id="root"><main><h1>Guide</h1>` + ssrProse("GUIDE", 30) + `</main></div>
	<footer>Copyright Example Project contributors.</footer>
	<script>` + script + `</script></body></html>`
}

// What a framework's error boundary does when hydration throws: the layout
// survives, the content is replaced by an apology.
const crashScript = `
	document.querySelector('main').innerHTML =
	  '<h1>This page crashed.</h1><p>Object has no member getBoundingClientRect</p><button id="retry">Try again</button>';
	throw new Error('hydration failed');`

func readRendered(t *testing.T, b *browser.Browser, req browser.Request) *browser.ReadResult {
	t.Helper()
	req.Render = true
	r, err := b.Read(context.Background(), req, "full", 0, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return r
}

func TestRenderCollapseRestoresServerPage(t *testing.T) {
	srv := liveServer(t, ssrPage(crashScript))
	b := newJSBrowser(t)

	r := readRendered(t, b, browser.Request{URL: srv.URL})
	if !r.RenderDiscarded {
		t.Fatalf("render_discarded not set; got:\n%s", r.Markdown)
	}
	if !strings.Contains(r.Markdown, "GUIDE paragraph 29") {
		t.Errorf("server-rendered content not restored:\n%s", r.Markdown)
	}
	if strings.Contains(r.Markdown, "This page crashed") {
		t.Errorf("the crashed render leaked into the restored page:\n%s", r.Markdown)
	}
	// The render still happened, and its diagnostics still say so.
	if len(r.JSErrors) == 0 {
		t.Errorf("js_errors lost: the render's diagnostics must survive the discard")
	}

	// Every other view of the page agrees with read: it is the server's HTML.
	raw, err := b.Read(context.Background(), browser.Request{URL: srv.URL, Render: true, Format: "raw_html"}, "", 0, "")
	if err != nil {
		t.Fatalf("raw_html: %v", err)
	}
	// The server's bytes: the guide is in <main>, and the script is still source
	// text (a serialized post-render DOM would have the apology there instead).
	if !strings.Contains(raw.Markdown, "<main><h1>Guide</h1>") || !strings.Contains(raw.Markdown, "throw new Error('hydration failed')") {
		t.Errorf("raw_html is not the server document:\n%s", raw.Markdown)
	}
	br, err := b.Browse(context.Background(), browser.Request{URL: srv.URL, Render: true})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if !br.RenderDiscarded {
		t.Errorf("browse did not report render_discarded")
	}

	// A repeat read (served from the page cache) reports the same thing.
	if again := readRendered(t, b, browser.Request{URL: srv.URL}); !again.RenderDiscarded || again.Markdown != r.Markdown {
		t.Errorf("cached read differs: discarded=%v", again.RenderDiscarded)
	}
}

func TestRenderCollapseBodyWiped(t *testing.T) {
	srv := liveServer(t, ssrPage(`document.body.innerHTML = '';`))
	r := readRendered(t, newJSBrowser(t), browser.Request{URL: srv.URL})
	if !r.RenderDiscarded || !strings.Contains(r.Markdown, "GUIDE paragraph 29") {
		t.Errorf("discarded=%v; a wiped body must fall back to the server page:\n%s", r.RenderDiscarded, r.Markdown)
	}
}

// With the guard off, whatever the scripts produced is what the caller gets.
func TestRenderCollapseGuardOff(t *testing.T) {
	srv := liveServer(t, ssrPage(crashScript))
	b, err := browser.New(browser.WithJS(3*time.Second), browser.WithAllowPrivate(true), browser.WithRenderCollapseGuard(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	r := readRendered(t, b, browser.Request{URL: srv.URL})
	if r.RenderDiscarded || !strings.Contains(r.Markdown, "This page crashed") || strings.Contains(r.Markdown, "GUIDE paragraph 29") {
		t.Errorf("guard off must keep the render (discarded=%v):\n%s", r.RenderDiscarded, r.Markdown)
	}
}

// The renders the guard must leave alone.
func TestRenderCollapseKeepsLegitimateRenders(t *testing.T) {
	for _, tc := range []struct {
		name string
		html string
		want string   // must be in the output
		gone []string // must not be
	}{
		{
			// A client-rendered app: the server sent a shell. There is nothing
			// to fall back to, and the guard does not apply.
			name: "SPA shell that the render fills",
			html: `<!doctype html><html><body><div id="app">Loading…</div><script>
				var h = '<main><h1>App</h1>';
				for (var i = 0; i < 30; i++) h += '<p>APP row ' + i + ' rendered on the client with enough words to be real content.</p>';
				document.getElementById('app').innerHTML = h + '</main>';
			</script></body></html>`,
			want: "APP row 29",
		},
		{
			// A long "enable JavaScript" notice and loading placeholder, replaced
			// by real content a third its size. Shorter is not collapsed.
			name: "long placeholder replaced by shorter real content",
			html: `<!doctype html><html><body><div id="app">` + ssrProse("PLACEHOLDER please enable JavaScript", 14) + `</div><script>
				var h = '<main><h1>Dashboard</h1>';
				for (var i = 0; i < 6; i++) h += '<p>REAL figure ' + i + ' for the current quarter, loaded and rendered on the client.</p>';
				document.getElementById('app').innerHTML = h + '</main>';
			</script></body></html>`,
			want: "REAL figure 5",
			gone: []string{"PLACEHOLDER"},
		},
		{
			// A noscript fallback is not content the scripts could lose.
			name: "noscript fallback beside a client render",
			html: `<!doctype html><html><body><noscript>` + ssrProse("NOSCRIPT", 30) + `</noscript><div id="app"></div><script>
				document.getElementById('app').innerHTML = '<main><h1>Live</h1><p>CLIENT content, short and real.</p></main>';
			</script></body></html>`,
			want: "CLIENT content",
		},
		{
			// A client-side route change: the app replaces the server-rendered
			// view with another, shorter one.
			name: "client-side navigation to a shorter view",
			html: ssrPage(`
				history.pushState({}, '', '/settings');
				var h = '<h1>Settings</h1>';
				for (var i = 0; i < 6; i++) h += '<p>SETTING option ' + i + ' with its description, rendered after the route change.</p>';
				document.querySelector('main').innerHTML = h;`),
			want: "SETTING option 5",
			gone: []string{"GUIDE paragraph 29"},
		},
		{
			// Collapsing an accordion hides text; it does not remove it. The
			// render is kept (safe output then omits what a reader cannot see).
			name: "script hides most of the page",
			html: ssrPage(`
				var ps = document.querySelectorAll('main p');
				for (var i = 1; i < ps.length; i++) ps[i].setAttribute('hidden', '');
				document.querySelector('main').insertAdjacentHTML('beforeend', '<p>ACCORDION collapsed by script</p>');`),
			want: "ACCORDION collapsed by script",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := liveServer(t, tc.html)
			r := readRendered(t, newJSBrowser(t), browser.Request{URL: srv.URL})
			if r.RenderDiscarded {
				t.Fatalf("a legitimate render was discarded:\n%s", r.Markdown)
			}
			if !strings.Contains(r.Markdown, tc.want) {
				t.Errorf("rendered content %q missing:\n%s", tc.want, r.Markdown)
			}
			for _, g := range tc.gone {
				if strings.Contains(r.Markdown, g) {
					t.Errorf("%q should have been replaced by the render:\n%s", g, r.Markdown)
				}
			}
		})
	}
}

// A script that asks to navigate away is reported, not second-guessed: the
// document is intact, so there is nothing to discard.
func TestRenderCollapsePendingNavigationKept(t *testing.T) {
	srv := liveServer(t, ssrPage(`location.href = '/elsewhere';`))
	r := readRendered(t, newJSBrowser(t), browser.Request{URL: srv.URL})
	if r.RenderDiscarded {
		t.Errorf("a client-side redirect with the document intact was discarded")
	}
	if !strings.HasSuffix(r.PendingNavigation, "/elsewhere") {
		t.Errorf("pending_navigation = %q, want …/elsewhere", r.PendingNavigation)
	}
}

// The caller named what it was waiting for and the render has it: that render
// is the answer, however much shorter than the server's page.
func TestRenderCollapseWaitMetKept(t *testing.T) {
	srv := liveServer(t, ssrPage(`document.querySelector('main').innerHTML = '<p id="ready">WAITED-FOR result</p>';`))
	r := readRendered(t, newJSBrowser(t), browser.Request{URL: srv.URL, WaitFor: "#ready"})
	if r.RenderDiscarded || !strings.Contains(r.Markdown, "WAITED-FOR result") {
		t.Errorf("discarded=%v; a met wait_for gate must keep the render:\n%s", r.RenderDiscarded, r.Markdown)
	}
}

// In a session the stored page is the server's HTML after a discard, and reads
// say so. interact runs against the live JavaScript DOM — the only DOM whose
// handlers exist — so the first interact reports that the page it acted on is
// not the one reads had been returning, and from then on the session's page is
// the live DOM, with render_discarded cleared because it no longer holds.
func TestRenderCollapseSessionAndInteract(t *testing.T) {
	srv := liveServer(t, ssrPage(crashScript))
	b := newJSBrowser(t)
	ctx := context.Background()

	br, err := b.Browse(ctx, browser.Request{SessionID: "s", URL: srv.URL, Render: true})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if !br.RenderDiscarded {
		t.Fatalf("session navigation did not report render_discarded")
	}
	before, err := b.Read(ctx, browser.Request{SessionID: "s", UseCurrent: true}, "full", 0, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !before.RenderDiscarded || !strings.Contains(before.Markdown, "GUIDE paragraph 29") {
		t.Fatalf("session read: discarded=%v, want the server page:\n%s", before.RenderDiscarded, before.Markdown)
	}

	ir, err := b.Interact(ctx, "s", "#retry", "click", "", "")
	if err != nil {
		t.Fatalf("interact: %v", err)
	}
	if !ir.Matched {
		t.Errorf("#retry exists only in the live DOM and must match there")
	}
	if !ir.RenderWasDiscarded {
		t.Errorf("interact did not report that it acted on a different DOM than reads returned")
	}
	if ir.Summary == nil || ir.Summary.RenderDiscarded {
		t.Errorf("interact summary must describe the live DOM, not claim it is the server page")
	}

	after, err := b.Read(ctx, browser.Request{SessionID: "s", UseCurrent: true}, "full", 0, "")
	if err != nil {
		t.Fatalf("read after interact: %v", err)
	}
	if after.RenderDiscarded {
		t.Errorf("render_discarded still set although the page is now the live DOM")
	}
	if !strings.Contains(after.Markdown, "This page crashed") {
		t.Errorf("after interact the session page must be the live DOM interact acted on:\n%s", after.Markdown)
	}

	// The second interact acts on the same DOM reads now return: nothing to flag.
	ir2, err := b.Interact(ctx, "s", "#retry", "click", "", "")
	if err != nil {
		t.Fatalf("interact 2: %v", err)
	}
	if ir2.RenderWasDiscarded {
		t.Errorf("render_was_discarded repeated although reads and interact now agree")
	}
}
