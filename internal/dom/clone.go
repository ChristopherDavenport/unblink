package dom

import "golang.org/x/net/html"

// CloneTree returns a deep copy of n (detached: no parent or siblings), with
// copied attribute slices so neither tree can mutate the other. It exists so
// consumers that need an owned copy of a parsed document (reduce.Full) don't
// pay a full html.Render + html.Parse round trip — and unlike re-parsing, a
// direct clone can't be normalized by the HTML5 parser's error recovery, so it
// is exactly the tree that was parsed (or that page JS built).
func CloneTree(n *html.Node) *html.Node {
	if n == nil {
		return nil
	}
	c := &html.Node{
		Type:      n.Type,
		DataAtom:  n.DataAtom,
		Data:      n.Data,
		Namespace: n.Namespace,
	}
	if len(n.Attr) > 0 {
		c.Attr = append([]html.Attribute(nil), n.Attr...)
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		c.AppendChild(CloneTree(child))
	}
	return c
}
