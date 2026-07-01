// Package image produces a Markdown manifest for an image response — type,
// dimensions, byte size, and filename — without inlining the pixels. Returning
// raw bytes is the MCP layer's opt-in job (include_bytes). Dimensions come from
// image.DecodeConfig, which reads only the header.
//
// It does not import the HTML parser — dom keeps that monopoly.
package image

import (
	"bytes"
	"fmt"
	stdimage "image"
	"net/url"
	"path"
	"strings"

	// Register the standard raster decoders for DecodeConfig.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// WebP dimension support (pure-Go, no cgo).
	_ "golang.org/x/image/webp"
)

// Convert builds the image manifest. It never returns an error: an undecodable
// or unknown format simply yields a manifest without dimensions.
func Convert(raw []byte, u *url.URL, contentType string) (markdown, title string, err error) {
	name := ""
	if u != nil {
		name = path.Base(u.Path)
		if name == "/" || name == "." {
			name = ""
		}
	}

	cfg, format, cfgErr := stdimage.DecodeConfig(bytes.NewReader(raw))

	typ := strings.TrimSpace(contentType)
	if typ == "" {
		typ = format
	}

	var b strings.Builder
	b.WriteString("**Image**\n\n")
	if name != "" {
		fmt.Fprintf(&b, "- filename: %s\n", name)
	}
	if typ != "" {
		fmt.Fprintf(&b, "- type: `%s`\n", typ)
	}
	if cfgErr == nil {
		fmt.Fprintf(&b, "- dimensions: %d×%d\n", cfg.Width, cfg.Height)
	} else {
		b.WriteString("- dimensions: unknown\n")
	}
	fmt.Fprintf(&b, "- bytes: %d\n", len(raw))
	b.WriteString("\n_Pass include_bytes=true to retrieve the raw image._\n")

	return b.String(), name, nil
}
