package dom

import (
	"sort"
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

// detectCollections finds a page's dominant repeating record-sets (product lists,
// search results, table-like rows of similar sibling elements) and returns each as
// a ready-to-use extract schema: a validated root selector matching the repeated
// containers plus record-relative field selectors. It is schema-only (no sample
// values) so browse stays cheap. Every emitted root is re-resolved against the
// group and the whole schema is proven by one dom.Records round-trip, so a
// surfaced collection is always valid input to the extract tool. Read-only over
// doc; ix is the shared selector-uniqueness index; res may be nil.
func detectCollections(doc *html.Node, ix *selIndex, res *idResolver) []page.Collection {
	sets := collectRecordSets(doc, res)
	if len(sets) == 0 {
		return nil
	}

	// Gate + score; drop candidates that fail the hard gates.
	survivors := sets[:0]
	for _, s := range sets {
		if gateAndScore(s) {
			survivors = append(survivors, s)
		}
	}
	if len(survivors) == 0 {
		return nil
	}
	// Rank by score desc; stable so document order breaks ties.
	sort.SliceStable(survivors, func(i, j int) bool { return survivors[i].score > survivors[j].score })

	var out []page.Collection
	var accepted []*html.Node // parents already emitted, for overlap de-dup
	for _, s := range survivors {
		if len(out) >= maxCollections {
			break
		}
		if overlapsAccepted(s.parent, accepted) {
			continue
		}
		root, count, ok := synthRoot(ix, doc, s)
		if !ok {
			continue
		}
		fields := detectSchemaFields(doc, root, s, res)
		if len(fields) == 0 {
			continue // a repeat with no extractable fields is not worth surfacing
		}
		out = append(out, page.Collection{Root: root, Count: count, Region: s.region, Fields: fields})
		accepted = append(accepted, s.parent)
	}
	return out
}

const (
	minRecords          = 3   // a repeat needs >= this many same-shape siblings
	minRecordRunes      = 24  // representative-record text floor (unless a content field exists)
	maxCollections      = 8   // dom-level emitted cap (browse caps again, tighter)
	maxCollectionFields = 8   // fields per collection
	maxRecordsSampled   = 24  // records inspected for text length + cross-record validation
	maxRecordSetCands   = 512 // candidate sibling-groups examined (perf ceiling)
	maxFieldScanNodes   = 256 // nodes visited per representative record for field discovery
	maxShapeDepth       = 2   // structural-shape signature depth (class-less fallback)
	maxShapeChildren    = 16  // children folded into a shape signature
	minFieldCoveragePct = 50  // keep a field only if it resolves non-empty in >= this % of records
)

// recordSet is a candidate repeating structure: the group of same-shape sibling
// children under parent, with rep = the first member used as the schema template.
type recordSet struct {
	parent *html.Node
	rep    *html.Node
	group  []*html.Node
	region string
	fields []fieldCand // representative record's candidate fields (filled by gateAndScore)
	score  int
}

// collectRecordSets walks the tree and, for every element, groups its direct
// element children by a structural signature; each group of >= minRecords is a
// candidate. Bounded by maxRecordSetCands.
func collectRecordSets(doc *html.Node, res *idResolver) []*recordSet {
	var out []*recordSet
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= maxRecordSetCands {
			return
		}
		if n.Type == html.ElementNode {
			for _, g := range groupChildren(n) {
				out = append(out, &recordSet{parent: n, rep: g[0], group: g, region: enclosingRegionRole(n, res)})
				if len(out) >= maxRecordSetCands {
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// groupChildren buckets parent's direct element children by structural signature,
// returning the buckets (in first-seen order) with >= minRecords members. Groups
// need not be contiguous, so an interleaved ad or separator does not split a list.
func groupChildren(parent *html.Node) [][]*html.Node {
	elems := 0
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			elems++
		}
	}
	if elems < minRecords {
		return nil
	}
	groups := map[string][]*html.Node{}
	var order []string
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		sig := groupSig(c)
		if _, seen := groups[sig]; !seen {
			order = append(order, sig)
		}
		groups[sig] = append(groups[sig], c)
	}
	var out [][]*html.Node
	for _, sig := range order {
		if g := groups[sig]; len(g) >= minRecords {
			out = append(out, g)
		}
	}
	return out
}

// groupSig is a node's grouping key: tag + its sorted-deduped class set (the same
// key selIndex uses), or, for a class-less node, a bounded structural shape hash.
// The two namespaces can't collide (the shape form is prefixed).
func groupSig(n *html.Node) string {
	if classes := dedupSorted(strings.Fields(attr(n, "class"))); len(classes) > 0 {
		return n.Data + "\x00" + strings.Join(classes, "\x00")
	}
	return "\x01" + shapeSig(n, maxShapeDepth)
}

// shapeSig is a bounded, positional-index-free description of n's descendant tag
// structure, used to group class-less repeats (table rows, plain cards).
func shapeSig(n *html.Node, depth int) string {
	var sb strings.Builder
	sb.WriteString(n.Data)
	sb.WriteByte('(')
	count := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if count >= maxShapeChildren {
			sb.WriteByte('+')
			break
		}
		if count > 0 {
			sb.WriteByte(',')
		}
		if depth > 0 {
			sb.WriteString(shapeSig(c, depth-1))
		} else {
			sb.WriteString(c.Data)
		}
		count++
	}
	sb.WriteByte(')')
	return sb.String()
}

// gateAndScore fills s.fields/s.score and reports whether s passes the hard gates
// (enough records, enough content, not a link-only nav/footer list).
func gateAndScore(s *recordSet) bool {
	if len(s.group) < minRecords {
		return false
	}
	s.fields = scanFieldCandidates(s.rep)
	kinds := kindSet(s.fields)
	// A "content" field carries visible record data beyond a bare link/data-attr;
	// its presence separates a real record-set from page chrome.
	hasContent := kinds["heading"] || kinds["image"] || kinds["time"] || kinds["classtext"]

	sample := s.group
	if len(sample) > maxRecordsSampled {
		sample = sample[:maxRecordsSampled]
	}
	lens := make([]int, 0, len(sample))
	for _, rec := range sample {
		lens = append(lens, len([]rune(collapsedText(rec))))
	}
	medText := medianInt(lens)

	// Thin/pagination guard: short records with no content-bearing field.
	if medText < minRecordRunes && !hasContent {
		return false
	}
	// Nav/footer chrome guard: a content-less list (links/data only) in a
	// nav/footer/banner region is navigation, not a record-set.
	if !hasContent {
		switch s.region {
		case "navigation", "contentinfo", "banner":
			return false
		}
	}

	s.score = regionBias(s.region) + 2*ilog2(len(s.group)) + clampInt(medText/40, 0, 8) + 2*len(kinds)
	if !hasContent {
		s.score -= 4
	}
	return true
}

// overlapsAccepted reports whether parent is (or is nested in/around) an
// already-emitted record-set's parent — so a list-inside-a-list emits once.
func overlapsAccepted(parent *html.Node, accepted []*html.Node) bool {
	for _, a := range accepted {
		if a == parent || isAncestor(a, parent) || isAncestor(parent, a) {
			return true
		}
	}
	return false
}

// --- root synthesis ---

// synthRoot returns a selector that resolves exactly to s.group, trying a clean
// class-of selector first, then scoping under the parent. It returns ok=false
// (drop the candidate) when no validated selector is found — never an unvalidated
// root. count is the doc-wide match count of the chosen root.
func synthRoot(ix *selIndex, doc *html.Node, s *recordSet) (root string, count int, ok bool) {
	want := nodeSet(s.group)
	// 1. Clean class-of selector (e.g. li.product), accepted only when it resolves
	//    to exactly the group.
	if sel := tagClassSelector(s.rep); strings.Contains(sel, ".") {
		if cnt, ok := rootResolvesTo(doc, sel, want); ok {
			return sel, cnt, true
		}
	}
	// 2. Scope under the parent's unique selector.
	parentSel := selectorFor(ix, doc, s.parent)
	if parentSel == "" {
		return "", 0, false
	}
	childKey := s.rep.Data
	if cs := tagClassSelector(s.rep); strings.Contains(cs, ".") {
		childKey = cs
	}
	if cnt, ok := rootResolvesTo(doc, parentSel+" > "+childKey, want); ok {
		return parentSel + " > " + childKey, cnt, true
	}
	// 3. Scope by tag only (last resort).
	if childKey != s.rep.Data {
		if cnt, ok := rootResolvesTo(doc, parentSel+" > "+s.rep.Data, want); ok {
			return parentSel + " > " + s.rep.Data, cnt, true
		}
	}
	return "", 0, false
}

// rootResolvesTo reports whether sel compiles and matches exactly the want set
// (every wanted node, and no extras from sibling containers).
func rootResolvesTo(doc *html.Node, sel string, want map[*html.Node]bool) (int, bool) {
	cs, err := cascadia.Compile(sel)
	if err != nil {
		return 0, false
	}
	matches := cs.MatchAll(doc)
	if len(matches) != len(want) {
		return 0, false
	}
	for _, m := range matches {
		if !want[m] {
			return 0, false
		}
	}
	return len(matches), true
}

// --- field detection ---

// fieldCand is a candidate extraction field discovered in a representative record.
type fieldCand struct {
	node *html.Node
	kind string // heading|link|image|time|data|classtext
	attr string // "" for collapsed text, else the attribute to read
}

type fkey struct {
	n *html.Node
	a string
}

// scanFieldCandidates walks a representative record (bounded) enumerating the
// leaf fields worth extracting.
func scanFieldCandidates(rep *html.Node) []fieldCand {
	var out []fieldCand
	seen := map[fkey]bool{}
	visited := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if visited >= maxFieldScanNodes || len(out) >= 4*maxCollectionFields {
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			visited++
			classifyNode(c, &out, seen)
			walk(c)
		}
	}
	walk(rep)
	return out
}

func classifyNode(c *html.Node, out *[]fieldCand, seen map[fkey]bool) {
	// A data-* attribute is worth capturing on any element (data-sku on a link,
	// data-price on a span…), independent of the tag-specific field below.
	if k := firstDataAttr(c); k != "" {
		addFieldCand(out, seen, fieldCand{c, "data", k})
	}
	switch {
	case headingLevel(c.Data) > 0:
		addFieldCand(out, seen, fieldCand{c, "heading", ""})
	case c.Data == "a" && hasAttr(c, "href"):
		addFieldCand(out, seen, fieldCand{c, "link", ""})
		addFieldCand(out, seen, fieldCand{c, "link", "href"})
	case c.Data == "img" && hasAttr(c, "src"):
		addFieldCand(out, seen, fieldCand{c, "image", "src"})
	case c.Data == "time" && hasAttr(c, "datetime"):
		addFieldCand(out, seen, fieldCand{c, "time", "datetime"})
	default:
		if hasClass(c) && hasOwnText(c) {
			addFieldCand(out, seen, fieldCand{c, "classtext", ""})
		}
	}
}

func addFieldCand(out *[]fieldCand, seen map[fkey]bool, f fieldCand) {
	k := fkey{f.node, f.attr}
	if seen[k] {
		return
	}
	seen[k] = true
	*out = append(*out, f)
}

// detectSchemaFields turns a record-set's candidate fields into a validated,
// deduped, coverage-pruned schema. It runs the exact dom.Records executor once,
// so a surfaced field provably resolves for a majority of records.
func detectSchemaFields(doc *html.Node, root string, s *recordSet, res *idResolver) []page.SchemaField {
	type built struct {
		name string
		spec FieldSpec
	}
	var builts []built
	usedNames := map[string]bool{}
	usedSel := map[string]bool{}
	for _, f := range s.fields {
		if len(builts) >= 2*maxCollectionFields {
			break
		}
		sel, ok := recordFieldSelector(s.rep, f)
		if !ok {
			continue
		}
		selKey := sel + "\x00" + f.attr
		if usedSel[selKey] {
			continue
		}
		usedSel[selKey] = true
		name := fieldName(f, res)
		if name == "" {
			name = "field"
		}
		name = uniqueFieldName(name, usedNames)
		builts = append(builts, built{name, FieldSpec{Selector: sel, Attr: f.attr}})
	}
	if len(builts) == 0 {
		return nil
	}

	fieldMap := make(map[string]FieldSpec, len(builts))
	for _, b := range builts {
		fieldMap[b.name] = b.spec
	}
	recs, _, err := Records(doc, root, fieldMap, maxRecordsSampled)
	if err != nil || len(recs) == 0 {
		return nil
	}
	coverage := map[string]int{}
	for _, rec := range recs {
		for name, val := range rec {
			if val != "" {
				coverage[name]++
			}
		}
	}
	threshold := (len(recs)*minFieldCoveragePct + 99) / 100 // ceil
	if threshold < 1 {
		threshold = 1
	}

	var out []page.SchemaField
	for _, b := range builts { // preserve discovery (document) order
		if coverage[b.name] < threshold {
			continue
		}
		out = append(out, page.SchemaField{Name: b.name, Selector: b.spec.Selector, Attr: b.spec.Attr})
		if len(out) >= maxCollectionFields {
			break
		}
	}
	return out
}

// recordFieldSelector synthesizes a record-relative selector that resolves to f's
// node as the first match within rep. It prefers a class-of selector, then a
// data-attribute selector, then the bare tag; it returns ok=false when none
// resolves to exactly this node (so a generic tag matching the container or a
// sibling is rejected).
func recordFieldSelector(rep *html.Node, f fieldCand) (string, bool) {
	var cands []string
	if cs := tagClassSelector(f.node); strings.Contains(cs, ".") {
		cands = append(cands, cs)
	}
	if f.kind == "data" && cssSafeIdent(f.attr) {
		cands = append(cands, "["+f.attr+"]")
	}
	cands = append(cands, f.node.Data)
	for _, sel := range cands {
		cs, err := cascadia.Compile(sel)
		if err != nil {
			continue
		}
		if cs.MatchFirst(rep) == f.node {
			return sel, true
		}
	}
	return "", false
}

// fieldName derives a stable, human-ish field name (a distinctive class token,
// then attribute/semantic defaults, then the accessible name). "" defers to the
// caller's fieldN fallback.
func fieldName(f fieldCand, res *idResolver) string {
	if f.kind == "data" {
		return sanitizeFieldName(strings.TrimPrefix(f.attr, "data-"))
	}
	for _, cl := range strings.Fields(attr(f.node, "class")) {
		if cssSafeIdent(cl) && !genericClassToken(cl) {
			if n := sanitizeFieldName(cl); n != "" {
				return n
			}
		}
	}
	switch {
	case f.kind == "link" && f.attr == "href":
		return "url"
	case f.kind == "link":
		return "link"
	case f.kind == "image":
		return "image"
	case f.kind == "time":
		return "date"
	case f.kind == "heading":
		return "title"
	}
	if an := accessibleName(f.node, res); an != "" {
		return sanitizeFieldName(an)
	}
	return ""
}

// genericClassToken filters common layout/utility classes that make poor field
// names (the selector still uses them; only the human-facing name skips them).
func genericClassToken(cl string) bool {
	switch strings.ToLower(cl) {
	case "row", "col", "column", "container", "wrapper", "inner", "content",
		"item", "cell", "flex", "grid", "block", "clearfix", "active", "selected":
		return true
	}
	return false
}

// sanitizeFieldName lowercases and reduces s to an identifier-ish token.
func sanitizeFieldName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSep := true // suppress leading separators
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSep = false
		default:
			if !prevSep {
				b.WriteByte('_')
				prevSep = true
			}
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "_")
}

func uniqueFieldName(name string, used map[string]bool) string {
	if !used[name] {
		used[name] = true
		return name
	}
	for i := 2; ; i++ {
		cand := name + "_" + strconv.Itoa(i)
		if !used[cand] {
			used[cand] = true
			return cand
		}
	}
}

// --- small helpers ---

// enclosingRegionRole returns the coarse landmark role of n's nearest landmark
// ancestor (with <article> treated as a content boost) for ranking/tagging, or
// "" when n sits at body level.
func enclosingRegionRole(n *html.Node, res *idResolver) string {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		if role := landmarkRole(p, res); role != "" {
			return role
		}
		if p.Data == "article" {
			return "article"
		}
	}
	return ""
}

func regionBias(region string) int {
	switch region {
	case "main", "article":
		return 20
	case "region", "complementary", "search":
		return 5
	case "navigation", "contentinfo", "banner":
		return -20
	default:
		return 0
	}
}

func kindSet(fields []fieldCand) map[string]bool {
	ks := make(map[string]bool, len(fields))
	for _, f := range fields {
		ks[f.kind] = true
	}
	return ks
}

func hasClass(n *html.Node) bool { return strings.TrimSpace(attr(n, "class")) != "" }

func hasOwnText(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode && strings.TrimSpace(c.Data) != "" {
			return true
		}
	}
	return false
}

func firstDataAttr(n *html.Node) string {
	for _, a := range n.Attr {
		if strings.HasPrefix(a.Key, "data-") && len(a.Key) > len("data-") {
			return a.Key
		}
	}
	return ""
}

func nodeSet(nodes []*html.Node) map[*html.Node]bool {
	s := make(map[*html.Node]bool, len(nodes))
	for _, n := range nodes {
		s[n] = true
	}
	return s
}

func ilog2(n int) int {
	l := 0
	for n > 1 {
		n >>= 1
		l++
	}
	return l
}

func medianInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int(nil), xs...)
	sort.Ints(s)
	return s[len(s)/2]
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
