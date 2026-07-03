package dom

import (
	"strings"

	"golang.org/x/net/html"
)

// IsHidden reports whether an element node is hidden from a human reader by common
// means: the `hidden` boolean attribute, aria-hidden="true", or an inline style that
// sets display:none / visibility:hidden / pushes the element off-screen. It is a
// heuristic (no full CSS/layout engine), aimed at the channels used to smuggle
// prompt-injection text past a human — hidden divs, off-screen "screen-reader" text —
// while avoiding false positives on ordinary content. Color-based hiding
// (white-on-white) needs the full cascade and is deliberately not attempted.
func IsHidden(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(a.Val), "true") {
				return true
			}
		case "style":
			if styleHides(a.Val) {
				return true
			}
		}
	}
	return false
}

// styleHides reports whether an inline style declaration visually hides its element.
func styleHides(style string) bool {
	s := strings.ToLower(style)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\t", "")
	switch {
	case strings.Contains(s, "display:none"):
		return true
	case strings.Contains(s, "visibility:hidden"):
		return true
	// Off-screen positioning / clipping (classic "visually hidden" smuggling).
	case strings.Contains(s, "-9999px"), strings.Contains(s, "-999em"),
		strings.Contains(s, "-10000px"), strings.Contains(s, "clip:rect(0"):
		return true
	}
	return false
}

// HasHidden reports whether the tree rooted at root contains any hidden element
// (see IsHidden) or comment node — the nodes StripHidden would remove. It is a
// read-only walk with no allocation, so a caller that would otherwise clone the
// tree just to strip it can skip the clone when nothing needs stripping (the
// common case: most pages carry no hidden-injection nodes).
func HasHidden(root *html.Node) bool {
	if root == nil {
		return false
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.CommentNode || IsHidden(c) || HasHidden(c) {
			return true
		}
	}
	return false
}

// StripHidden removes hidden element subtrees (see IsHidden) and all comment nodes
// from the tree rooted at root, in place. Callers must pass a tree they own (e.g. a
// reduce clone) — it mutates the nodes. Comments are dropped because they never
// render for a human yet survive into some output paths, making them a place to hide
// instructions aimed at an AI reading the content.
func StripHidden(root *html.Node) {
	if root == nil {
		return
	}
	var next *html.Node
	for c := root.FirstChild; c != nil; c = next {
		next = c.NextSibling
		switch {
		case c.Type == html.CommentNode:
			root.RemoveChild(c)
		case IsHidden(c):
			root.RemoveChild(c)
		default:
			StripHidden(c)
		}
	}
}
