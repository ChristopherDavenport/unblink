package dom

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Duplicate-block suppression targets pages that carry the same boilerplate
// region twice — most commonly a desktop navigation menu plus its mobile-drawer
// twin — which full-page reduction would otherwise emit twice. Only large,
// link-dense subtrees qualify, so repeated prose (a song chorus, a recurring
// disclaimer) is never touched.
const (
	dupMinRunes = 200 // minimum normalized-text length for a subtree to count as a block
	dupMinLinks = 3   // minimum descendant links — the navigation/boilerplate signature
)

// blockSig identifies an element subtree by its reader-visible content: the
// whitespace-normalized text plus every descendant link target, in document
// order. Two subtrees with the same signature read identically, links included —
// same words pointing at different targets is not a duplicate.
type blockSig struct {
	text  string
	links []string
}

func (s blockSig) key() string {
	return s.text + "\x00" + strings.Join(s.links, "\x00")
}

func (s blockSig) qualifies() bool {
	return len(s.links) >= dupMinLinks && utf8.RuneCountInString(s.text) >= dupMinRunes
}

// StripDuplicateBlocks removes element subtrees whose reader-visible content
// duplicates a subtree appearing earlier in document order, in place. The first
// (outermost) occurrence is kept. Callers must pass a tree they own (e.g. a
// reduce clone) — it mutates the nodes.
func StripDuplicateBlocks(root *html.Node) {
	if root == nil {
		return
	}
	sigs := make(map[*html.Node]blockSig)
	collectSigs(root, sigs)
	pruneDuplicates(root, sigs, make(map[string]*html.Node))
}

// collectSigs computes every element's signature bottom-up. Script-like elements
// contribute nothing: their text is not reader-visible, and two otherwise
// identical blocks must not be told apart by embedded code.
func collectSigs(n *html.Node, sigs map[*html.Node]blockSig) blockSig {
	switch n.Type {
	case html.TextNode:
		return blockSig{text: strings.Join(strings.Fields(n.Data), " ")}
	case html.ElementNode, html.DocumentNode:
		var sig blockSig
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "template", "noscript":
				return blockSig{}
			case "a":
				for _, a := range n.Attr {
					if a.Key == "href" {
						sig.links = append(sig.links, a.Val)
						break
					}
				}
			}
		}
		var parts []string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			cs := collectSigs(c, sigs)
			if cs.text != "" {
				parts = append(parts, cs.text)
			}
			sig.links = append(sig.links, cs.links...)
		}
		sig.text = strings.Join(parts, " ")
		if n.Type == html.ElementNode {
			sigs[n] = sig
		}
		return sig
	default:
		return blockSig{}
	}
}

// pruneDuplicates walks pre-order, registering the first subtree bearing each
// qualifying signature and removing later duplicates. A node that shares its
// signature with a registered ancestor is the same block seen through a wrapper,
// not a duplicate — it is descended into, and never re-registered, so the
// outermost node stays the representative.
func pruneDuplicates(n *html.Node, sigs map[*html.Node]blockSig, seen map[string]*html.Node) {
	var next *html.Node
	for c := n.FirstChild; c != nil; c = next {
		next = c.NextSibling
		if sig, ok := sigs[c]; ok && sig.qualifies() {
			key := sig.key()
			if first, dup := seen[key]; dup {
				if !isAncestor(first, c) {
					n.RemoveChild(c)
					continue
				}
			} else {
				seen[key] = c
			}
		}
		pruneDuplicates(c, sigs, seen)
	}
}

// isAncestor reports whether a is a proper ancestor of n.
func isAncestor(a, n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p == a {
			return true
		}
	}
	return false
}
