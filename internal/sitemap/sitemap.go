// Package sitemap parses the sitemaps.org XML formats — a <urlset> of page URLs
// or a <sitemapindex> of child sitemaps — using only the standard library. It is
// a pure data decoder: it never fetches anything (the caller supplies the bytes)
// and it deliberately does not import x/net/html or cascadia, so internal/dom
// remains the sole importer of the HTML parser.
//
// Parse transparently gunzips gzip-magic input so a static ".xml.gz" sitemap
// (typically served as application/gzip with no transport Content-Encoding, hence
// not inflated by the fetch layer) decodes without special handling by the caller.
package sitemap

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
)

// Kind is the sitemap document's root type.
type Kind int

const (
	// KindUnknown is the zero value; Parse never returns it (it errors instead).
	KindUnknown Kind = iota
	// KindURLSet is a <urlset> listing page URLs.
	KindURLSet
	// KindIndex is a <sitemapindex> listing child sitemaps.
	KindIndex
)

// Loc is one entry: an absolute URL plus its optional, raw <lastmod> string
// (left unparsed — callers rarely need it and the formats vary).
type Loc struct {
	URL     string `json:"url"`
	LastMod string `json:"lastmod,omitempty"`
}

// Doc is a parsed sitemap document. Exactly one of URLs / Sitemaps is populated,
// according to Kind.
type Doc struct {
	Kind     Kind
	URLs     []Loc // page URLs      (Kind == KindURLSet)
	Sitemaps []Loc // child sitemaps (Kind == KindIndex)
}

const (
	// DefaultMaxEntries is the sitemaps.org per-file URL ceiling; used when the
	// caller passes maxEntries <= 0.
	DefaultMaxEntries = 50000

	// maxDecompressed bounds a gunzipped sitemap to defeat gzip bombs.
	maxDecompressed = 32 << 20 // 32 MiB
)

// Parse decodes a sitemap or sitemap-index from data. It transparently gunzips
// gzip-magic input (a static .xml.gz). Parsing stops after maxEntries entries
// (<= 0 uses DefaultMaxEntries) and returns whatever was collected so far. An
// unrecognized root element yields (nil, error); a truncated/malformed tail is
// tolerated (lenient decoding) and the entries gathered before it are returned.
func Parse(data []byte, maxEntries int) (*Doc, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}

	// A static .xml.gz arrives as raw gzip (gzip magic 0x1f 0x8b) because the
	// fetch layer only inflates transport-level Content-Encoding. Inflate here,
	// bounded, so both plain and gzipped sitemaps share one path.
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("sitemap: gunzip: %w", err)
		}
		defer zr.Close()
		inflated, err := io.ReadAll(io.LimitReader(zr, maxDecompressed))
		if err != nil {
			return nil, fmt.Errorf("sitemap: gunzip: %w", err)
		}
		data = inflated
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false

	doc := &Doc{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Lenient: return what we have rather than failing a partial doc,
			// but only once the root has been identified.
			if doc.Kind == KindUnknown {
				return nil, fmt.Errorf("sitemap: parse: %w", err)
			}
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		// Match on the local name only, so the default xmlns doesn't matter.
		switch se.Name.Local {
		case "urlset":
			if doc.Kind == KindUnknown {
				doc.Kind = KindURLSet
			}
		case "sitemapindex":
			if doc.Kind == KindUnknown {
				doc.Kind = KindIndex
			}
		case "url":
			if doc.Kind != KindURLSet {
				continue
			}
			if doc.total() >= maxEntries {
				return doc, nil
			}
			if loc, ok := decodeLoc(dec, &se); ok {
				doc.URLs = append(doc.URLs, loc)
			}
		case "sitemap":
			if doc.Kind != KindIndex {
				continue
			}
			if doc.total() >= maxEntries {
				return doc, nil
			}
			if loc, ok := decodeLoc(dec, &se); ok {
				doc.Sitemaps = append(doc.Sitemaps, loc)
			}
		}
	}

	if doc.Kind == KindUnknown {
		return nil, fmt.Errorf("sitemap: not a sitemap (no <urlset> or <sitemapindex> root)")
	}
	return doc, nil
}

func (d *Doc) total() int { return len(d.URLs) + len(d.Sitemaps) }

// locElem mirrors a <url>/<sitemap> child: a <loc> and optional <lastmod>.
type locElem struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// decodeLoc consumes the element started by se and returns its Loc. A blank loc
// (malformed entry) yields ok=false so it is skipped.
func decodeLoc(dec *xml.Decoder, se *xml.StartElement) (Loc, bool) {
	var el locElem
	if err := dec.DecodeElement(&el, se); err != nil {
		return Loc{}, false
	}
	url := trimSpace(el.Loc)
	if url == "" {
		return Loc{}, false
	}
	return Loc{URL: url, LastMod: trimSpace(el.LastMod)}, true
}

// trimSpace trims ASCII/Unicode whitespace without pulling in strings just for
// one call site elsewhere; kept local for clarity.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}
