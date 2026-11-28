// Package content converts non-HTML page bodies — JSON, plain text, Markdown,
// RSS/Atom/JSON feeds, PDF, and images — into Markdown for the read pipeline. It is dispatched
// by page.Kind from the browser orchestrator, mirroring how reduce/emit run for
// HTML pages.
//
// Boundary: converters operate on raw bytes and must NOT import the HTML parser
// (golang.org/x/net/html) or cascadia — dom keeps that monopoly. Feeds are parsed
// as XML/JSON via gofeed; the stdlib "html" entity decoder is fine (it is not the
// x/net/html parser).
package content

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/christopherdavenport/unblink/internal/content/feed"
	img "github.com/christopherdavenport/unblink/internal/content/image"
	"github.com/christopherdavenport/unblink/internal/content/pdf"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Render converts a non-HTML page into Markdown in place, setting p.Markdown and
// upgrading p.Meta.Title when a converter yields a better one. It is safe to call
// for any non-HTML Kind; HTML pages flow through reduce/emit instead.
func Render(ctx context.Context, p *page.Page) error {
	switch p.Kind {
	case page.KindJSON:
		p.Markdown = renderJSON(p.Raw)
	case page.KindText:
		p.Markdown = renderText(p.Raw)
	case page.KindMarkdown:
		p.Markdown = renderMarkdown(p.Raw)
		upgradeTitle(p, MarkdownTitle(p.Raw))
	case page.KindFeed:
		md, title, err := feed.Convert(p.Raw, p.FinalURL)
		if err != nil {
			// A mislabeled or malformed feed still has usable bytes — show as text.
			p.Markdown = renderText(p.Raw)
			return nil
		}
		p.Markdown = md
		upgradeTitle(p, title)
	case page.KindPDF:
		md, title, err := pdf.Convert(ctx, p.Raw)
		if err != nil {
			if ctx.Err() != nil {
				return err // cancellation/timeout propagates
			}
			// Unparseable PDF — still give the agent a manifest.
			p.Markdown = renderBinaryManifest(p)
			return nil
		}
		p.Markdown = md
		upgradeTitle(p, title)
	case page.KindImage:
		md, title, err := img.Convert(p.Raw, p.FinalURL, p.ContentType)
		if err != nil {
			p.Markdown = renderBinaryManifest(p)
			return nil
		}
		p.Markdown = md
		upgradeTitle(p, title)
	default:
		// KindBinary / unknown.
		p.Markdown = renderBinaryManifest(p)
	}
	return nil
}

func renderJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// Not valid JSON despite the content type — fall back to text.
		return renderText(raw)
	}
	return fencedBlock("json", buf.String())
}

func renderText(raw []byte) string {
	return fencedBlock("", string(raw))
}

// renderMarkdown passes a Markdown body through as-is: it is already the output
// format, so fencing it (as renderText would) only costs the reader its
// structure. The one edit is normalisation — a leading BOM is dropped and CRLF/CR
// line endings become LF — so pagination and the cursor fingerprint see the same
// text regardless of the origin's platform.
func renderMarkdown(raw []byte) string {
	s := strings.TrimPrefix(string(raw), "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// maxTitleScanLines bounds MarkdownTitle's scan: a title sits at the top of a
// document, and the body is untrusted and may be huge.
const maxTitleScanLines = 200

// MarkdownTitle returns a Markdown document's title: its first ATX level-1
// heading ("# Title") outside a fenced code block, else the `title:` key of a
// leading YAML front-matter block. It returns "" when neither is present within
// the first maxTitleScanLines lines.
func MarkdownTitle(raw []byte) string {
	s := strings.TrimPrefix(string(raw), "\uFEFF")
	var frontTitle string
	inFront, inFence := false, false
	var fence string
	line := 0
	for len(s) > 0 && line < maxTitleScanLines {
		var l string
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			l, s = s[:i], s[i+1:]
		} else {
			l, s = s, ""
		}
		l = strings.TrimRight(l, "\r")
		line++
		trimmed := strings.TrimSpace(l)
		if line == 1 && trimmed == "---" {
			inFront = true
			continue
		}
		if inFront {
			if trimmed == "---" || trimmed == "..." {
				inFront = false
				continue
			}
			if v, ok := strings.CutPrefix(l, "title:"); ok && frontTitle == "" {
				frontTitle = strings.Trim(strings.TrimSpace(v), `"'`)
			}
			continue
		}
		if inFence {
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			n := len(trimmed) - len(strings.TrimLeft(trimmed, trimmed[:1]))
			fence, inFence = trimmed[:n], true
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "# "); ok {
			// Drop an optional closing sequence ("# Title #").
			rest = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), "#"))
			if rest != "" {
				return rest
			}
		}
	}
	return frontTitle
}

func renderBinaryManifest(p *page.Page) string {
	var b strings.Builder
	b.WriteString("**Binary content**\n\n")
	if p.Meta.Title != "" {
		fmt.Fprintf(&b, "- name: %s\n", p.Meta.Title)
	}
	if p.ContentType != "" {
		fmt.Fprintf(&b, "- type: `%s`\n", p.ContentType)
	}
	fmt.Fprintf(&b, "- bytes: %d\n\n", len(p.Raw))
	b.WriteString("_This content is not text-extractable._\n")
	return b.String()
}

// upgradeTitle replaces the page title when the converter produced a real one
// (e.g. a feed's own title beats the URL filename set at fetch time).
func upgradeTitle(p *page.Page, title string) {
	if strings.TrimSpace(title) != "" {
		p.Meta.Title = title
	}
}

// fencedBlock wraps s in a code fence whose backtick run is longer than any run
// inside s, so embedded fences cannot break out.
func fencedBlock(lang, s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	n := 3
	if longest >= 3 {
		n = longest + 1
	}
	ticks := strings.Repeat("`", n)
	return ticks + lang + "\n" + s + "\n" + ticks + "\n"
}
