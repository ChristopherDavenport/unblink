// Package content converts non-HTML page bodies — JSON, plain text, RSS/Atom/JSON
// feeds, PDF, and images — into Markdown for the read pipeline. It is dispatched
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
