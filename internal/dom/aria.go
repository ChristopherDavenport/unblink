package dom

// Semantic accessibility helpers computed straight from the parsed node tree —
// no layout, no browser. These back the structured representation surfaced to
// agents: accessible names (a computable subset of the WAI-ARIA accname
// algorithm — no visibility filtering, since unblink has no layout engine), the
// interactive ARIA-state read, landmark labelling, and the stable content-hash
// element ids. Everything here is pure and deterministic (in particular hashID
// uses no randomness) so re-extracting a cached page yields identical output.

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

func fnvByte(h uint64, b byte) uint64 { return (h ^ uint64(b)) * fnvPrime64 }

func fnvStr(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * fnvPrime64
	}
	return h
}

// fnvStrLower folds ASCII to lower case as it hashes, so casing variation of the
// same logical name maps to the same id. Non-ASCII bytes are fed verbatim
// (determinism, not locale-correct folding, is all a cache key needs).
func fnvStrLower(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		h = (h ^ uint64(c)) * fnvPrime64
	}
	return h
}

// idResolver resolves element ids to nodes for aria-labelledby, building the id
// index lazily on first use. Most pages have no aria-labelledby, so the index —
// a full-document DFS — is never built for them. Scanning the (possibly
// post-render) Doc rather than raw bytes keeps it correct when JS adds labels.
type idResolver struct {
	doc   *html.Node
	built bool
	idx   map[string]*html.Node
}

func newIDResolver(doc *html.Node) *idResolver { return &idResolver{doc: doc} }

func (r *idResolver) get(id string) *html.Node {
	if r == nil {
		return nil
	}
	if !r.built {
		r.idx = buildIDIndex(r.doc)
		r.built = true
	}
	return r.idx[id]
}

// buildIDIndex maps each element id to its node in a single DFS, first-writer-wins
// on duplicates (matching the browser's getElementById). It resolves
// aria-labelledby IDREFs without a per-element document query.
func buildIDIndex(doc *html.Node) map[string]*html.Node {
	idx := map[string]*html.Node{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id := attr(n, "id"); id != "" {
				if _, dup := idx[id]; !dup {
					idx[id] = n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return idx
}

// accessibleName computes an element's accessible name with ARIA precedence:
// aria-labelledby → aria-label → native input value → ancestor <label> → the
// alt/title/placeholder attributes → descendant text. Used for interactive
// controls. res may be nil (skips labelledby resolution).
func accessibleName(n *html.Node, res *idResolver) string {
	if name := labelledbyText(n, res); name != "" {
		return name
	}
	if l := strings.TrimSpace(attr(n, "aria-label")); l != "" {
		return l
	}
	if n.Data == "input" {
		if v := strings.TrimSpace(attr(n, "value")); v != "" {
			return v
		}
	}
	if l := ancestorLabelText(n); l != "" {
		return l
	}
	if a := strings.TrimSpace(attr(n, "alt")); a != "" {
		return a
	}
	if t := collapsedText(n); t != "" {
		return t
	}
	// title/placeholder are last-resort per the accname algorithm.
	if t := strings.TrimSpace(attr(n, "title")); t != "" {
		return t
	}
	return strings.TrimSpace(attr(n, "placeholder"))
}

// explicitName returns only an element's *explicit* accessible name —
// aria-labelledby → aria-label → title — with no text/native fallback. A
// landmark region (form/section) is only exposed when it carries one of these
// (per ARIA); using the full accessibleName would wrongly promote every
// text-bearing <form>/<section> to a named landmark.
func explicitName(n *html.Node, res *idResolver) string {
	if t := labelledbyText(n, res); t != "" {
		return t
	}
	if l := strings.TrimSpace(attr(n, "aria-label")); l != "" {
		return l
	}
	return strings.TrimSpace(attr(n, "title"))
}

// labelledbyText resolves n's aria-labelledby IDREF list against idIndex and
// joins the referenced elements' names. It is single-level (a referenced node's
// own aria-label is honored but its aria-labelledby is not chained) and skips
// self-references, so a malformed cyclic/self reference can neither loop nor
// recurse.
func labelledbyText(n *html.Node, res *idResolver) string {
	refs := strings.TrimSpace(attr(n, "aria-labelledby"))
	if refs == "" || res == nil {
		return ""
	}
	var parts []string
	for _, id := range strings.Fields(refs) {
		target := res.get(id)
		if target == nil || target == n {
			continue
		}
		if l := strings.TrimSpace(attr(target, "aria-label")); l != "" {
			parts = append(parts, l)
		} else if t := collapsedText(target); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// ancestorLabelText returns the text of the nearest ancestor <label> (implicit
// label association), or "".
func ancestorLabelText(n *html.Node) string {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "label" {
			return collapsedText(p)
		}
	}
	return ""
}

// normTristate normalizes an ARIA tri-state attribute value
// (aria-checked/expanded/pressed/selected): the recognized token, else a native
// boolean's presence → "true". An empty result means "not applicable"
// (absent/unrecognized) and is omitempty'd away by callers.
func normTristate(ariaVal string, allowMixed, native bool) string {
	switch v := strings.ToLower(strings.TrimSpace(ariaVal)); v {
	case "true", "false":
		return v
	case "mixed":
		if allowMixed {
			return "mixed"
		}
	}
	if native {
		return "true"
	}
	return ""
}

// ariaInvalid reports whether aria-invalid marks the element invalid: present and
// any value other than "false" (true/grammar/spelling all mean invalid).
func ariaInvalid(n *html.Node) bool {
	v, ok := lookupAttr(n, "aria-invalid")
	if !ok {
		return false
	}
	return strings.ToLower(strings.TrimSpace(v)) != "false"
}

// lookupAttr returns an attribute value and whether it was present.
func lookupAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// idMinter assigns stable content-hash ids for one Extract pass and guarantees
// per-snapshot uniqueness: identical semantic twins (same tag/role/name/path)
// share a base hash, so a repeat gets a "-2"/"-3"… suffix while the base hash
// stays reorder-stable. See ADR 0008.
type idMinter struct{ seen map[string]int }

func newIDMinter() *idMinter { return &idMinter{seen: map[string]int{}} }

func (m *idMinter) mint(prefix string, n *html.Node, name, role string) string {
	base := hashID(prefix, n, name, role)
	m.seen[base]++
	if c := m.seen[base]; c > 1 {
		return base + "-" + strconv.Itoa(c)
	}
	return base
}

// hashID returns a short, mutation-resilient identifier of the form
// "<prefix>-<6hex>". The hash is over a semantic composite key — tag, role,
// lowercased accessible name, and an ancestor tag-name path with NO positional
// (:nth) indices — so it survives class churn and sibling reordering. FNV-1a is
// deliberate: fast, stdlib, and this is a cache key, not a security token. It is
// computed with an inline accumulator (no hasher object, no []byte conversions)
// because extract calls it once per control/heading on control-dense pages.
func hashID(prefix string, n *html.Node, name, role string) string {
	h := uint64(fnvOffset64)
	h = fnvStr(h, n.Data)
	h = fnvByte(h, 0)
	h = fnvStrLower(h, strings.TrimSpace(role))
	h = fnvByte(h, 0)
	h = fnvStrLower(h, strings.TrimSpace(name))
	h = fnvByte(h, 0)
	h = feedStructuralSig(h, n)
	return formatID(prefix, h)
}

// feedStructuralSig hashes the ancestor tag-name chain down to n, WITHOUT
// positional indices, anchored at the nearest id-bearing or landmark ancestor
// when one exists. Dropping :nth positions is what lets the id survive sibling
// reordering; anchoring at a semantic ancestor keeps the signature short and
// stable across unrelated page changes.
func feedStructuralSig(h uint64, n *html.Node) uint64 {
	// Collect anchor→node into a stack buffer (no heap alloc for the common
	// shallow path), then feed root-first for a deterministic order.
	var buf [24]*html.Node
	chain := buf[:0]
	for cur := n; cur != nil && cur.Type == html.ElementNode && len(chain) < len(buf); cur = cur.Parent {
		chain = append(chain, cur)
		if cur != n && (strings.TrimSpace(attr(cur, "id")) != "" || isStructuralAnchor(cur)) {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		cur := chain[i]
		if i == len(chain)-1 && cur != n { // the anchor node
			if id := strings.TrimSpace(attr(cur, "id")); id != "" {
				h = fnvByte(h, '#')
				h = fnvStr(h, id)
			} else {
				h = fnvByte(h, '@')
				h = fnvStr(h, cur.Data)
			}
		} else {
			h = fnvStr(h, cur.Data)
		}
		h = fnvByte(h, '>')
	}
	return h
}

// formatID builds "<prefix>-<6hex>" (low 24 bits) in one allocation.
func formatID(prefix string, h uint64) string {
	const hexd = "0123456789abcdef"
	buf := make([]byte, 0, len(prefix)+7)
	buf = append(buf, prefix...)
	buf = append(buf, '-')
	for shift := 20; shift >= 0; shift -= 4 {
		buf = append(buf, hexd[(h>>uint(shift))&0xF])
	}
	return string(buf)
}

// isStructuralAnchor reports whether n is a sectioning/landmark element that can
// anchor a structural signature (a stable semantic root).
func isStructuralAnchor(n *html.Node) bool {
	switch n.Data {
	case "main", "nav", "header", "footer", "aside", "section", "article", "form":
		return true
	}
	return false
}
