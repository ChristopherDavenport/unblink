package dom

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"

	"github.com/christopherdavenport/unblink/internal/page"
)

// Precompiled selectors. cascadia.MustCompile panics on a bad selector, so these
// are validated at package init. Selector.MatchAll returns matches in document
// order (pre-order DFS).
var (
	selMeta     = cascadia.MustCompile("meta")
	selTitle    = cascadia.MustCompile("title")
	selHTML     = cascadia.MustCompile("html")
	selBase     = cascadia.MustCompile("base[href]")
	selCanon    = cascadia.MustCompile("link[rel=canonical]")
	selAnchor   = cascadia.MustCompile("a[href]")
	selImg      = cascadia.MustCompile("img[src]")
	selForm     = cascadia.MustCompile("form")
	selField    = cascadia.MustCompile("input, select, textarea")
	selOption   = cascadia.MustCompile("option")
	selHeadings = cascadia.MustCompile("h1, h2, h3, h4, h5, h6")
	selPara     = cascadia.MustCompile("p")
	// Non-anchor interactive controls. a[href] is covered by links/click; this is
	// the surface that needs real DOM-event dispatch (the interact tool).
	selInteractive = cascadia.MustCompile(
		"button, [role=button], input[type=submit], input[type=button], " +
			"input[type=reset], [onclick], [tabindex], summary, [role=tab]")
)

// Extract walks p.Doc and fills p.Meta (title, description, links, forms, images,
// headings). URLs are resolved to absolute against the page's base URL.
func Extract(p *page.Page) error {
	if p.Doc == nil {
		return fmt.Errorf("dom: extract called before parse")
	}
	base := BaseURL(p)
	m := &p.Meta

	metas := map[string]string{}
	for _, mn := range selMeta.MatchAll(p.Doc) {
		key := attr(mn, "name")
		if key == "" {
			key = attr(mn, "property")
		}
		if key == "" {
			continue
		}
		metas[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(attr(mn, "content"))
	}

	if t := selTitle.MatchFirst(p.Doc); t != nil {
		m.Title = collapsedText(t)
	}
	if m.Title == "" {
		m.Title = metas["og:title"]
	}
	m.Description = firstNonEmpty(metas["description"], metas["og:description"])
	m.SiteName = metas["og:site_name"]
	if h := selHTML.MatchFirst(p.Doc); h != nil {
		m.Lang = attr(h, "lang")
	}
	if c := selCanon.MatchFirst(p.Doc); c != nil {
		m.Canonical = resolveURL(base, attr(c, "href"))
	}

	m.Links = extractLinks(p.Doc, base)
	m.Forms = extractForms(p.Doc, base, p.FinalURL)
	m.Images = extractImages(p.Doc, base)
	m.Headings = extractHeadings(p.Doc)
	m.Controls = extractInteractive(p.Doc)
	return nil
}

// BaseURL returns the page's base URL: the <base href> if present and resolvable,
// otherwise the final (post-redirect) URL.
func BaseURL(p *page.Page) *url.URL {
	base := p.FinalURL
	if bn := selBase.MatchFirst(p.Doc); bn != nil {
		if href := strings.TrimSpace(attr(bn, "href")); href != "" {
			if u, err := url.Parse(href); err == nil {
				if u.IsAbs() {
					return u
				}
				if base != nil {
					return base.ResolveReference(u)
				}
			}
		}
	}
	return base
}

// AbsolutizeURLs rewrites a[href] and img[src] under n to absolute URLs resolved
// against base. It mutates n in place, so callers must pass a clone of any tree
// they want to keep relative (reduce passes the readability clone).
func AbsolutizeURLs(n *html.Node, base *url.URL) {
	if n == nil || base == nil {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		AbsolutizeURLs(c, base)
	}
	if n.Type != html.ElementNode {
		return
	}
	switch n.Data {
	case "a":
		setAbs(n, "href", base)
	case "img":
		setAbs(n, "src", base)
	}
}

// FirstParagraph returns the collapsed text of the first non-empty <p>, truncated
// to a short excerpt. Used for a cheap browse excerpt when no meta description.
func FirstParagraph(doc *html.Node) string {
	for _, p := range selPara.MatchAll(doc) {
		if t := collapsedText(p); t != "" {
			return truncate(t, 300)
		}
	}
	return ""
}

func extractLinks(doc *html.Node, base *url.URL) []page.Link {
	var out []page.Link
	seen := map[string]bool{}
	for _, a := range selAnchor.MatchAll(doc) {
		href := resolveURL(base, attr(a, "href"))
		if href == "" {
			continue
		}
		text := collapsedText(a)
		key := href + "\x00" + text
		if seen[key] {
			continue
		}
		seen[key] = true
		hu, _ := url.Parse(href)
		out = append(out, page.Link{
			Text:     text,
			Href:     href,
			Rel:      attr(a, "rel"),
			Internal: sameRegistrableDomain(base, hu),
		})
	}
	return out
}

func extractForms(doc *html.Node, base, pageURL *url.URL) []page.Form {
	var out []page.Form
	for _, f := range selForm.MatchAll(doc) {
		action := resolveURL(base, attr(f, "action"))
		if action == "" && pageURL != nil {
			action = pageURL.String()
		}
		method := strings.ToUpper(strings.TrimSpace(attr(f, "method")))
		if method == "" {
			method = "GET"
		}
		form := page.Form{
			ID:      attr(f, "id"),
			Name:    attr(f, "name"),
			Action:  action,
			Method:  method,
			Enctype: strings.ToLower(strings.TrimSpace(attr(f, "enctype"))),
		}
		for _, fld := range selField.MatchAll(f) {
			form.Fields = append(form.Fields, extractField(fld))
		}
		out = append(out, form)
	}
	return out
}

func extractField(n *html.Node) page.Field {
	f := page.Field{
		Name:     attr(n, "name"),
		Value:    attr(n, "value"),
		Required: hasAttr(n, "required"),
	}
	switch n.Data {
	case "select":
		f.Type = "select"
		for _, o := range selOption.MatchAll(n) {
			v := attr(o, "value")
			if v == "" {
				v = collapsedText(o)
			}
			f.Options = append(f.Options, v)
		}
	case "textarea":
		f.Type = "textarea"
		if f.Value == "" {
			f.Value = collapsedText(n)
		}
	default:
		f.Type = attr(n, "type")
		if f.Type == "" {
			f.Type = "text"
		}
	}
	return f
}

func extractImages(doc *html.Node, base *url.URL) []page.Image {
	var out []page.Image
	for _, im := range selImg.MatchAll(doc) {
		src := resolveURL(base, attr(im, "src"))
		if src == "" {
			continue
		}
		out = append(out, page.Image{Alt: attr(im, "alt"), Src: src})
	}
	return out
}

func extractHeadings(doc *html.Node) []page.Heading {
	var out []page.Heading
	for _, h := range selHeadings.MatchAll(doc) {
		lvl := headingLevel(h.Data)
		if lvl == 0 {
			continue
		}
		out = append(out, page.Heading{Level: lvl, Text: collapsedText(h), ID: attr(h, "id")})
	}
	return out
}

// extractInteractive collects the page's non-anchor interactive controls, each
// with a stable selector the interact tool can replay. An element matching more
// than one sub-selector (e.g. <button onclick>) appears once (dedup by node).
// The uniqueness index is built once (one walk) on the first control, replacing
// a full-document query per control on control-dense pages.
func extractInteractive(doc *html.Node) []page.Control {
	var out []page.Control
	seen := map[*html.Node]bool{}
	var ix *selIndex
	for _, n := range selInteractive.MatchAll(doc) {
		if seen[n] {
			continue
		}
		seen[n] = true
		if ix == nil {
			ix = buildSelIndex(doc)
		}
		out = append(out, page.Control{
			Text:     controlLabel(n),
			Selector: selectorFor(ix, doc, n),
			Kind:     controlKind(n),
			Role:     attr(n, "role"),
			Disabled: hasAttr(n, "disabled"),
		})
	}
	return out
}

func controlLabel(n *html.Node) string {
	if l := strings.TrimSpace(attr(n, "aria-label")); l != "" {
		return l
	}
	if n.Data == "input" {
		if v := strings.TrimSpace(attr(n, "value")); v != "" {
			return v
		}
	}
	return collapsedText(n)
}

func controlKind(n *html.Node) string {
	switch n.Data {
	case "button":
		return "button"
	case "summary":
		return "summary"
	case "input":
		switch strings.ToLower(attr(n, "type")) {
		case "submit":
			return "submit"
		case "reset":
			return "reset"
		default:
			return "button"
		}
	}
	switch strings.ToLower(attr(n, "role")) {
	case "button":
		return "role-button"
	case "tab":
		return "tab"
	}
	return "interactive"
}

// selectorFor builds a stable CSS selector that resolves uniquely to n within doc:
// an id first, then a tag+class combination, then an :nth-of-type path from the
// nearest id-bearing ancestor (or the document root). The result always compiles,
// so the JS engine's query() can replay it.
//
// The index proves most selectors unique in O(1); anything it can't prove (or
// any identifier too exotic to embed unescaped) falls back to uniqueMatch's
// compile+query, so the output is identical to the pre-index implementation.
func selectorFor(ix *selIndex, doc, n *html.Node) string {
	rawID := attr(n, "id")
	if id := strings.TrimSpace(rawID); id != "" {
		if rawID == id && cssSafeIdent(id) {
			if ix.idCount[id] == 1 {
				return "#" + id
			}
			// Duplicated id: "#id" can't resolve uniquely; try tag+class.
		} else if sel := "#" + id; uniqueMatch(doc, sel, n) {
			return sel
		}
	}
	if sel, ok, proven := ix.uniqueTagClass(n); proven {
		if ok {
			return sel
		}
	} else if sel := tagClassSelector(n); uniqueMatch(doc, sel, n) {
		return sel
	}
	return nthOfTypePath(n)
}

// selIndex holds one walk's worth of uniqueness facts about a document, so
// selectorFor can prove selectors unique without a full-document query per
// control. Counts are keyed by raw attribute values, matching cascadia's exact
// id semantics; class tokens are deduped per element (class="a a" counts once).
type selIndex struct {
	idCount   map[string]int // elements per raw id value
	tagCount  map[string]int // elements per tag name
	pairCount map[string]int // elements per tag+"\x00"+class token
	sigCount  map[string]int // elements per tag+"\x00"+sorted-deduped-class signature
}

func buildSelIndex(doc *html.Node) *selIndex {
	ix := &selIndex{
		idCount: map[string]int{}, tagCount: map[string]int{},
		pairCount: map[string]int{}, sigCount: map[string]int{},
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id := attr(n, "id"); id != "" {
				ix.idCount[id]++
			}
			ix.tagCount[n.Data]++
			if classes := dedupSorted(strings.Fields(attr(n, "class"))); len(classes) > 0 {
				for _, c := range classes {
					ix.pairCount[n.Data+"\x00"+c]++
				}
				ix.sigCount[n.Data+"\x00"+strings.Join(classes, "\x00")]++
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return ix
}

// dedupSorted returns the unique class tokens in sorted order (a canonical
// signature: class order is irrelevant to selector matching).
func dedupSorted(fields []string) []string {
	if len(fields) < 2 {
		return fields
	}
	sort.Strings(fields)
	out := fields[:1]
	for _, c := range fields[1:] {
		if c != out[len(out)-1] {
			out = append(out, c)
		}
	}
	return out
}

// uniqueTagClass decides n's tag+class selector from the index alone.
// proven=false means the index can't answer (exotic identifiers, or every
// class shared) and the caller must fall back to compile+query. With classes,
// uniqueness is provable only when some class of n appears on exactly one
// element of n's tag: class selectors match supersets (div.a matches
// class="a b"), so an exact-signature count alone would over-claim.
func (ix *selIndex) uniqueTagClass(n *html.Node) (sel string, ok, proven bool) {
	// The selector parser lowercases tag names, so a camelCase foreign element
	// (svg foreignObject) can't be proven from Data-keyed counts — fall back.
	if !cssSafeIdent(n.Data) || strings.ToLower(n.Data) != n.Data {
		return "", false, false
	}
	classes := strings.Fields(attr(n, "class"))
	if len(classes) == 0 {
		return n.Data, ix.tagCount[n.Data] == 1, true
	}
	for _, c := range classes {
		if !cssSafeIdent(c) {
			return "", false, false
		}
	}
	canon := dedupSorted(append([]string(nil), classes...))
	for _, c := range canon {
		if ix.pairCount[n.Data+"\x00"+c] == 1 {
			// Some class of n appears on exactly one element of n's tag (n
			// itself): no other element can match the full selector.
			return tagClassSelector(n), true, true
		}
	}
	if ix.sigCount[n.Data+"\x00"+strings.Join(canon, "\x00")] >= 2 {
		// Another element has the identical tag+class set, and it matches n's
		// selector too — provably not unique, skip straight to nth-of-type.
		return "", false, true
	}
	// Every class is shared but no identical twin exists; a superset match may
	// or may not exist — not provable from counts.
	return "", false, false
}

// cssSafeIdent reports whether s can be embedded in a selector without
// escaping: ASCII letters, digits, hyphen, underscore; no leading digit; no
// leading hyphen followed by a digit or hyphen. Exotic identifiers take the
// compile+query fallback instead.
func cssSafeIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '-':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	if s[0] == '-' && (len(s) == 1 || s[1] == '-' || (s[1] >= '0' && s[1] <= '9')) {
		return false
	}
	return true
}

// uniqueMatch reports whether selector compiles and resolves to exactly n.
func uniqueMatch(doc *html.Node, selector string, want *html.Node) bool {
	sel, err := cascadia.Compile(selector)
	if err != nil {
		return false
	}
	matches := cascadia.QueryAll(doc, sel)
	return len(matches) == 1 && matches[0] == want
}

func tagClassSelector(n *html.Node) string {
	sel := n.Data
	for _, c := range strings.Fields(attr(n, "class")) {
		sel += "." + c
	}
	return sel
}

// nthOfTypePath builds a child-combinator path of :nth-of-type steps from n up to
// the nearest id-bearing ancestor (anchored as #id), or to the document root.
func nthOfTypePath(n *html.Node) string {
	var steps []string
	for cur := n; cur != nil && cur.Type == html.ElementNode; cur = cur.Parent {
		if cur != n {
			if id := strings.TrimSpace(attr(cur, "id")); id != "" {
				return strings.Join(append([]string{"#" + id}, steps...), " > ")
			}
		}
		steps = append([]string{cur.Data + nthOfType(cur)}, steps...)
	}
	return strings.Join(steps, " > ")
}

// nthOfType returns the 1-based :nth-of-type position of n among its same-tag
// siblings, or "" when it has no element parent.
func nthOfType(n *html.Node) string {
	if n.Parent == nil {
		return ""
	}
	idx := 0
	for c := n.Parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == n.Data {
			idx++
			if c == n {
				return fmt.Sprintf(":nth-of-type(%d)", idx)
			}
		}
	}
	return ""
}

// --- small node helpers (shared across the dom package) ---

func attr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func setAbs(n *html.Node, key string, base *url.URL) {
	for i, a := range n.Attr {
		if a.Key == key {
			if abs := resolveURL(base, a.Val); abs != "" {
				n.Attr[i].Val = abs
			}
			return
		}
	}
}

// collapsedText returns the concatenated descendant text of n with runs of
// whitespace collapsed to single spaces.
func collapsedText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.TextNode {
			sb.WriteString(nd.Data)
			sb.WriteByte(' ')
			return
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

func resolveURL(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if base == nil {
		if u.IsAbs() {
			return u.String()
		}
		return ""
	}
	return base.ResolveReference(u).String()
}

func sameRegistrableDomain(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	ha, hb := strings.ToLower(a.Hostname()), strings.ToLower(b.Hostname())
	if ha == "" || hb == "" {
		return false
	}
	if ha == hb {
		return true
	}
	da, err1 := publicsuffix.EffectiveTLDPlusOne(ha)
	db, err2 := publicsuffix.EffectiveTLDPlusOne(hb)
	if err1 != nil || err2 != nil {
		return false
	}
	return da == db
}

func headingLevel(tag string) int {
	if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
		return int(tag[1] - '0')
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}
