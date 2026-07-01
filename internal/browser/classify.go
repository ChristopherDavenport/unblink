package browser

import (
	"bytes"
	"mime"
	"net/http"
	"strings"

	"github.com/christopherdavenport/unblink/internal/page"
)

// classify determines the content Kind of a fetched body from its Content-Type,
// corroborated by magic-byte sniffing so a mislabeled type is still classified
// correctly (e.g. a PDF served as text/html). Classification lives here in the
// orchestrator — fetch stays content-type-ignorant beyond charset.
//
// This is distinct from site.go's isHTML/plausibleTextType, which detect soft-404
// HTML shells for the site-hint (robots/llms) probes and answer a narrower
// question; the two intentionally stay separate.
func classify(contentType string, body []byte) page.Kind {
	// Magic bytes win over a (possibly mislabeled) header.
	if k, ok := classifyMagic(body); ok {
		return k
	}

	// Resolve a usable media essence, sniffing when the header is missing or the
	// generic octet-stream (mirrors fetch's textual gate for the same cases).
	essence := mediaEssence(contentType)
	if essence == "" || essence == "application/octet-stream" {
		essence = mediaEssence(http.DetectContentType(body))
	}

	switch {
	case essence == "text/html", essence == "application/xhtml+xml":
		return page.KindHTML
	case essence == "application/pdf":
		return page.KindPDF
	case essence == "image/svg+xml":
		// SVG is XML source, not a raster image — surface the markup as text
		// rather than a dimensionless pixel manifest.
		return page.KindText
	case strings.HasPrefix(essence, "image/"):
		return page.KindImage
	case essence == "application/rss+xml", essence == "application/atom+xml", essence == "application/feed+json":
		return page.KindFeed
	case essence == "application/json", strings.HasSuffix(essence, "+json"):
		if looksLikeJSONFeed(body) {
			return page.KindFeed
		}
		return page.KindJSON
	case essence == "application/xml", essence == "text/xml", strings.HasSuffix(essence, "+xml"):
		if looksLikeXMLFeed(body) {
			return page.KindFeed
		}
		return page.KindText
	case strings.HasPrefix(essence, "text/"):
		return page.KindText
	default:
		return page.KindBinary
	}
}

// classifyMagic returns a Kind derived purely from a leading binary signature.
func classifyMagic(body []byte) (page.Kind, bool) {
	switch {
	case bytes.HasPrefix(body, []byte("%PDF-")):
		return page.KindPDF, true
	case bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")),
		bytes.HasPrefix(body, []byte("\xFF\xD8\xFF")),
		bytes.HasPrefix(body, []byte("GIF87a")),
		bytes.HasPrefix(body, []byte("GIF89a")):
		return page.KindImage, true
	case len(body) >= 12 && bytes.HasPrefix(body, []byte("RIFF")) && bytes.Equal(body[8:12], []byte("WEBP")):
		return page.KindImage, true
	}
	return "", false
}

// mediaEssence extracts the lowercase "type/subtype" from a Content-Type header,
// dropping parameters. Returns "" for an empty header.
func mediaEssence(contentType string) string {
	if contentType == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		return mt
	}
	ct := contentType
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// looksLikeJSONFeed reports whether a JSON body advertises the JSON Feed spec.
func looksLikeJSONFeed(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("jsonfeed.org"))
}

// looksLikeXMLFeed reports whether an XML body's root looks like RSS/Atom/RDF.
func looksLikeXMLFeed(body []byte) bool {
	head := body
	if len(head) > 1024 {
		head = head[:1024]
	}
	lower := strings.ToLower(string(head))
	return strings.Contains(lower, "<rss") ||
		strings.Contains(lower, "<feed") ||
		strings.Contains(lower, "<rdf:rdf")
}
