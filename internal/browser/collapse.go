package browser

import (
	"log/slog"

	"github.com/christopherdavenport/unblink/internal/dom"
	"github.com/christopherdavenport/unblink/internal/page"
)

// The render-collapse guard (ADR 0023).
//
// Running a page's JavaScript is meant to add to what the server sent. When it
// instead leaves almost nothing — hydration threw and the framework's error
// boundary replaced the document with "This page crashed", or the app booted
// into an error or consent shell — the rendered DOM is strictly worse than the
// HTML the server delivered. The guard sizes the document's content text before
// the scripts run and after; on a collapse it throws the rendered DOM away and
// restores the server's document, and says so.
//
// The rule is two conditions, both required:
//
//   - collapseMinBefore: the server-rendered document held at least this much
//     content text. A client-rendered app's shell holds next to none, so the
//     guard never applies to the pages whose content only JavaScript can
//     produce — there is nothing to fall back to.
//   - collapseKeep: the rendered document holds under this fraction of it. A
//     route render, a client-side filter or a paginated table shortens a page;
//     only an error shell shortens it by an order of magnitude.
//
// "Content text" is dom.MeasureDocument's Region: text in the main landmark (or
// the body) outside navigation/banner/footer chrome, hidden or not. Chrome is
// left out so that an error boundary rendered inside an intact site layout is
// still seen as the loss it is; hidden text is counted so that a script
// collapsing an accordion or a tab set — hiding text that is still in the
// document — is not.
const (
	collapseMinBefore = 1000 // bytes, roughly 250 tokens
	collapseKeep      = 0.10
)

// renderCollapsed is the rule on numbers alone.
func renderCollapsed(before, after int) bool {
	return before >= collapseMinBefore && float64(after) < collapseKeep*float64(before)
}

// WithRenderCollapseGuard toggles the render-collapse guard. On by default;
// turning it off always keeps whatever the page's scripts produced.
func WithRenderCollapseGuard(enabled bool) Option {
	return func(o *options) { o.noCollapseGuard = !enabled }
}

// measureForCollapse sizes p's document before its scripts run. It returns -1
// when the guard is off, so the render path pays nothing for it then. The walk
// is read-only and allocation-free.
func (b *Browser) measureForCollapse(p *page.Page) int {
	if b.noCollapseGuard {
		return -1
	}
	return dom.MeasureDocument(p.Doc).Region
}

// guardRenderCollapse runs after a one-shot render of p, whose document held
// textBefore bytes of content text before its scripts ran. If the render
// collapsed it, p.Doc is replaced by a fresh parse of the server's bytes, so
// extraction, reduction and every later tool see the server-rendered page;
// p.Rendered goes back to false, since nothing downstream is the rendered DOM;
// and RenderDiag.Discarded records what happened. The re-parse is the only extra
// parse the guard ever does, and only on the collapse path.
//
// A render whose wait_for/wait_text gate was met is never discarded: the caller
// named what it wanted and the rendered DOM has it.
func (b *Browser) guardRenderCollapse(p *page.Page, textBefore int) {
	if textBefore < 0 || p.RenderDiag == nil {
		return
	}
	d := p.RenderDiag
	d.TextBefore, d.TextAfter = textBefore, dom.MeasureDocument(p.Doc).Region
	if (d.WaitRequested && d.WaitMet) || !renderCollapsed(d.TextBefore, d.TextAfter) {
		return
	}
	restored := &page.Page{Raw: p.Raw}
	if err := dom.Parse(restored); err != nil {
		// The same bytes parsed a moment ago; keep the render rather than fail.
		return
	}
	// The static path's own step (processFetched): SSR web components flatten
	// here because the engine's imperative shadow composition is gone with its DOM.
	dom.ComposeDeclarativeShadow(restored.Doc)
	p.Doc = restored.Doc
	p.Rendered = false
	d.Discarded = true
	slog.Debug("js: render collapsed the page; restored the server document",
		"url", pageURL(p), "text_before", d.TextBefore, "text_after", d.TextAfter, "errors", len(d.Errors))
}

// liveRenderDiag returns the diagnostics a page snapshotted from a live runtime
// carries: the stored page's, minus Discarded. That flag says "this page is the
// server's HTML", which stops being true the moment the page is the live DOM —
// the guard does not act on live runtimes, because interact must operate on the
// very DOM its results describe. The copy keeps the stored page's own
// diagnostics intact for anything still holding it.
func liveRenderDiag(d *page.RenderDiag) *page.RenderDiag {
	if d == nil || !d.Discarded {
		return d
	}
	c := *d
	c.Discarded = false
	return &c
}
