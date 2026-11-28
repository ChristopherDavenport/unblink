package dom

import "golang.org/x/net/html"

// collapsedMarker prefixes the summary of a closed <details>.
const collapsedMarker = "▸ "

// MarkCollapsedDetails labels every closed <details> in the tree: its summary
// gains a leading "▸ ", the disclosure triangle a browser draws beside it.
//
// The content of a closed <details> is kept, unlike a closed <dialog> or a
// hidden/aria-hidden/display:none subtree (see IsHidden). Those are not part of
// what the page shows and a reader cannot reach them; a <details> is in-flow
// content one click away, and it is where pages put real answers — FAQ entries,
// "show more" sections, API member lists. Dropping it loses them: replayed over
// the real-page corpus, dropping cut mean recall by 0.008 and one page from 0.99
// to 0.30, because pages also open their <details> from CSS and script without
// ever setting the attribute. So the content stays and the marker says how the
// page presents it: collapsed behind that label. A <details> with no <summary>
// gets the label a browser gives it, "Details".
//
// Callers must pass a tree they own (a reduce clone): it inserts text nodes.
func MarkCollapsedDetails(root *html.Node) {
	if root == nil {
		return
	}
	var next *html.Node
	for c := root.FirstChild; c != nil; c = next {
		next = c.NextSibling
		if c.Type != html.ElementNode {
			continue
		}
		if c.Data == "details" && c.Namespace == "" && !hasAttr(c, "open") {
			markDetails(c)
		}
		MarkCollapsedDetails(c)
	}
}

func markDetails(d *html.Node) {
	var summary *html.Node
	for c := d.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "summary" {
			summary = c
			break
		}
	}
	if summary == nil {
		// A browser shows a default "Details" label for a <details> with no summary.
		summary = &html.Node{Type: html.ElementNode, Data: "summary"}
		summary.AppendChild(&html.Node{Type: html.TextNode, Data: "Details"})
		d.InsertBefore(summary, d.FirstChild)
	}
	// Put the marker where the summary's text starts, so a summary that wraps a
	// heading (<summary><h3>…</h3></summary>) keeps the marker inside it.
	at := summary
	for at.FirstChild != nil && at.FirstChild.Type == html.ElementNode && at.FirstChild.FirstChild != nil {
		at = at.FirstChild
	}
	at.InsertBefore(&html.Node{Type: html.TextNode, Data: collapsedMarker}, at.FirstChild)
}
