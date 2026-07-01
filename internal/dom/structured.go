package dom

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/page"
)

const (
	maxJSONLDItems    = 256
	maxMicrodataItems = 128
	maxMicrodataDepth = 6
	maxPropValueLen   = 1000 // runes
)

var (
	selScriptTag = cascadia.MustCompile("script")
	selItemscope = cascadia.MustCompile("[itemscope]")
)

// JSONLD returns the parsed <script type="application/ld+json"> payloads,
// flattened for agent consumption: a top-level array yields its elements; an
// object with an "@graph" array yields the graph entries (the wrapper is
// dropped); a bare object yields itself. Malformed blocks are skipped rather
// than failing the whole call. Capped at maxJSONLDItems.
func JSONLD(doc *html.Node) []any {
	var out []any
	for _, s := range selScriptTag.MatchAll(doc) {
		if strings.ToLower(strings.TrimSpace(attr(s, "type"))) != "application/ld+json" {
			continue
		}
		txt := strings.TrimSpace(rawScriptText(s))
		if txt == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(txt), &v); err != nil {
			continue // skip malformed block
		}
		out = appendJSONLD(out, v)
		if len(out) >= maxJSONLDItems {
			return out[:maxJSONLDItems]
		}
	}
	return out
}

// appendJSONLD flattens one decoded JSON-LD value into out (see JSONLD).
func appendJSONLD(out []any, v any) []any {
	if len(out) >= maxJSONLDItems {
		return out
	}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if out = appendJSONLD(out, e); len(out) >= maxJSONLDItems {
				return out
			}
		}
	case map[string]any:
		if g, ok := t["@graph"].([]any); ok {
			for _, e := range g {
				if out = appendJSONLD(out, e); len(out) >= maxJSONLDItems {
					return out
				}
			}
			return out
		}
		out = append(out, t)
	default:
		out = append(out, t)
	}
	return out
}

// rawScriptText concatenates a <script> node's text children verbatim (no
// whitespace collapse, so JSON string contents survive intact).
func rawScriptText(n *html.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return sb.String()
}

// Microdata returns the document's top-level microdata items (itemscope elements
// with no itemscope ancestor). A property that is itself an itemscope becomes a
// nested MicrodataItem value; URLs are resolved against base. itemref is not
// supported (a documented limitation). Capped at maxMicrodataItems / depth.
func Microdata(doc *html.Node, base *url.URL) []page.MicrodataItem {
	var out []page.MicrodataItem
	for _, scope := range selItemscope.MatchAll(doc) {
		if hasAncestorItemscope(scope) {
			continue // nested item; captured under its parent
		}
		out = append(out, buildItem(scope, base, 0))
		if len(out) >= maxMicrodataItems {
			break
		}
	}
	return out
}

// buildItem collects the itemprops that belong to scope. It descends through
// non-itemscope descendants (their itemprops belong to this scope too) but stops
// at a nested itemscope, which owns its own subtree.
func buildItem(scope *html.Node, base *url.URL, depth int) page.MicrodataItem {
	item := page.MicrodataItem{
		Type: strings.TrimSpace(attr(scope, "itemtype")),
		ID:   strings.TrimSpace(attr(scope, "itemid")),
	}
	if depth >= maxMicrodataDepth {
		return item
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			isScope := hasAttr(c, "itemscope")
			if prop := strings.TrimSpace(attr(c, "itemprop")); prop != "" {
				var val any
				if isScope {
					val = buildItem(c, base, depth+1)
				} else {
					val = microdataValue(c, base)
				}
				if item.Properties == nil {
					item.Properties = make(map[string][]any)
				}
				for _, name := range strings.Fields(prop) {
					item.Properties[name] = append(item.Properties[name], val)
				}
			}
			if !isScope {
				walk(c) // nested scopes own their descendants
			}
		}
	}
	walk(scope)
	return item
}

// microdataValue extracts the value of a (non-itemscope) itemprop element,
// following the HTML microdata rules for url/date/meta-bearing elements.
func microdataValue(n *html.Node, base *url.URL) any {
	var v string
	switch n.Data {
	case "a", "area", "link":
		v = resolveURL(base, attr(n, "href"))
	case "img", "audio", "video", "source", "track", "iframe", "embed":
		v = resolveURL(base, attr(n, "src"))
	case "object":
		v = resolveURL(base, attr(n, "data"))
	case "meta":
		v = attr(n, "content")
	case "time":
		if dt := strings.TrimSpace(attr(n, "datetime")); dt != "" {
			v = dt
		} else {
			v = collapsedText(n)
		}
	case "data", "meter":
		if val := strings.TrimSpace(attr(n, "value")); val != "" {
			v = val
		} else {
			v = collapsedText(n)
		}
	default:
		v = collapsedText(n)
	}
	return truncate(v, maxPropValueLen)
}

// hasAncestorItemscope reports whether n has an itemscope element ancestor.
func hasAncestorItemscope(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && hasAttr(p, "itemscope") {
			return true
		}
	}
	return false
}
