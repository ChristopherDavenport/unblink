package js

import (
	"strings"

	"golang.org/x/net/html"
)

// WaitCondition is an optional gate the render/interact settle waits for before it
// considers the page ready: the selector resolves and/or the text appears in the
// live DOM. When both fields are set, both must hold. A nil or blank condition is
// treated as satisfied immediately, so the settle behaves exactly as before.
type WaitCondition struct {
	Selector string // CSS selector (cascadia) that must resolve to a node
	Text     string // substring that must appear in the tree's visible text
}

// empty reports whether the condition asks for nothing (nil receiver included), in
// which case it is satisfied from the first tick.
func (w *WaitCondition) empty() bool {
	return w == nil || (w.Selector == "" && w.Text == "")
}

// settleStats reports how a settle ended, so callers can tell a page that went
// quiet from one that was cut off mid-work by the budget. Filled by settlePoll on
// its closing tick; read only after the loop has terminated (or the close has been
// observed), which provides the happens-before edge.
type settleStats struct {
	met      bool  // the wait condition held before the settle closed
	deadline bool  // closed by the budget deadline, not by quiescence
	pending  int32 // network requests still in flight at close
	domBusy  bool  // the DOM mutated within the final tick window
}

// satisfied reports whether the condition holds in the tree rooted at doc. An empty
// condition is always satisfied.
func (w *WaitCondition) satisfied(doc *html.Node) bool {
	if w.empty() || doc == nil {
		return w.empty()
	}
	if w.Selector != "" && query(doc, w.Selector) == nil {
		return false
	}
	if w.Text != "" && !strings.Contains(textContent(doc), w.Text) {
		return false
	}
	return true
}
