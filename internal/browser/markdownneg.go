package browser

import (
	"context"

	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Markdown content negotiation (--prefer-markdown, ADR 0021). A read can always
// ask per call (Request.PreferMarkdown); the operator default below only decides
// what a read that does not say gets. A text/markdown *response* is passed
// through regardless (classify → page.KindMarkdown).

// DefaultPreferMarkdown is the operator default for Markdown negotiation: what
// --prefer-markdown is when the flag is not given, and what a Browser built
// without WithPreferMarkdown does. It is the one place that default lives — the
// CLI flag, browser.New and the tests all read it. Provisionally off; ADR 0021
// lists the arguments for each value.
const DefaultPreferMarkdown = false

// markdownAccept is the Accept header a negotiating read sends: Markdown first,
// then HTML at a lower weight so an origin without a Markdown representation
// answers exactly as it would have.
const markdownAccept = "text/markdown, text/html;q=0.9, application/xhtml+xml;q=0.9, */*;q=0.8"

// negotiatesMarkdown decides whether one Read asks the origin for Markdown.
// The rule is that only a navigation whose result the caller consumes as Markdown
// text negotiates:
//
//   - only a read that navigates (a URL, not a session's current page);
//   - only format=markdown — raw_html and text exist to reach the HTML source;
//   - never with wait_for/wait_text, which ask for a JavaScript-rendered DOM;
//   - in a session only when the call says so: a session page is shared with
//     links/click/forms/interact, which need a DOM a Markdown body cannot give.
//
// Within those limits the per-call PreferMarkdown wins over the operator default.
func (b *Browser) negotiatesMarkdown(req Request, format string) bool {
	if req.URL == "" || req.UseCurrent {
		return false
	}
	if format != "" && format != "markdown" {
		return false
	}
	if req.WaitFor != "" || req.WaitText != "" {
		return false
	}
	if req.PreferMarkdown != nil {
		return *req.PreferMarkdown
	}
	return b.preferMarkdown && req.SessionID == ""
}

// checkCurrentRepresentation refuses a read of a session's current page that
// names a representation the page is not in. Reading the current page does not
// navigate, so prefer_markdown cannot be honoured there; answering with the other
// representation would silently contradict the caller. The error says how to get
// the one asked for.
func checkCurrentRepresentation(req Request, format string, p *page.Page) error {
	if req.PreferMarkdown == nil || (req.URL != "" && !req.UseCurrent) {
		return nil
	}
	if format != "" && format != "markdown" {
		return nil // raw_html/text never negotiate, so the argument is moot
	}
	isMarkdown := p.Kind == page.KindMarkdown
	if *req.PreferMarkdown == isMarkdown {
		return nil
	}
	have, want := "HTML", "Markdown"
	if isMarkdown {
		have, want = "Markdown", "HTML"
	}
	return errf(ErrBadInput, "prefer_markdown=%t cannot apply to the session's current page: it was fetched as %s, and reading it again does not navigate. To get the %s version, read url=%q with prefer_markdown=%t",
		*req.PreferMarkdown, have, want, pageURL(p), *req.PreferMarkdown)
}

// docContext returns the context a document request is sent under: navContext's
// robots redirect check, plus the Markdown Accept when this navigation
// negotiates. Only the document request uses it — the render's subrequests run
// under the caller's plain ctx, so they never inherit the header.
func (b *Browser) docContext(ctx context.Context, ro renderOpts) context.Context {
	ctx = b.navContext(ctx)
	if ro.markdown {
		ctx = fetch.WithAccept(ctx, markdownAccept)
	}
	return ctx
}
