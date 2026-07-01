package dom

import (
	"fmt"
	"net/url"
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
			ID:     attr(f, "id"),
			Name:   attr(f, "name"),
			Action: action,
			Method: method,
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
func extractInteractive(doc *html.Node) []page.Control {
	var out []page.Control
	seen := map[*html.Node]bool{}
	for _, n := range selInteractive.MatchAll(doc) {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, page.Control{
			Text:     controlLabel(n),
			Selector: selectorFor(doc, n),
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
func selectorFor(doc, n *html.Node) string {
	if id := strings.TrimSpace(attr(n, "id")); id != "" {
		if sel := "#" + id; uniqueMatch(doc, sel, n) {
			return sel
		}
	}
	if sel := tagClassSelector(n); uniqueMatch(doc, sel, n) {
		return sel
	}
	return nthOfTypePath(n)
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
