package main

import (
	"context"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// The Markdown variant: one extra request per page asking the origin for
// text/markdown (the header unblink's --prefer-markdown sends, ADR 0021). It is
// stored beside the capture and never used for scoring. It exists to answer,
// with data, whether an origin's Markdown says what its HTML shows.

// markdownAccept mirrors internal/browser's markdownAccept.
const markdownAccept = "text/markdown, text/html;q=0.9, application/xhtml+xml;q=0.9, */*;q=0.8"

const maxMarkdownBody = 8 << 20

// markdownVariant is <out>/<id>/markdown.json. Body, when present, names a file
// in bodies/ like an archive entry's.
type markdownVariant struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url,omitempty"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Vary        string `json:"vary,omitempty"`
	// TokensHeader is the origin's own x-markdown-tokens estimate, when sent.
	TokensHeader string `json:"x_markdown_tokens,omitempty"`
	// Served is true when the response was text/markdown: the origin negotiated.
	Served bool   `json:"served_markdown"`
	Body   string `json:"body,omitempty"`
	Size   int    `json:"size,omitempty"`
	Error  string `json:"error,omitempty"`
}

// fetchMarkdown makes the one request. The body is stored only when the origin
// actually served Markdown; an HTML answer is the page already captured.
func fetchMarkdown(ctx context.Context, client *http.Client, url, userAgent, dir string) *markdownVariant {
	v := &markdownVariant{URL: url}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	req.Header.Set("Accept", markdownAccept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	defer resp.Body.Close()
	v.Status, v.FinalURL = resp.StatusCode, resp.Request.URL.String()
	v.ContentType, v.Vary = resp.Header.Get("Content-Type"), resp.Header.Get("Vary")
	v.TokensHeader = resp.Header.Get("X-Markdown-Tokens")
	v.Served = isMarkdownType(v.ContentType) && resp.StatusCode >= 200 && resp.StatusCode < 300
	if !v.Served {
		return v
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMarkdownBody+1))
	if err != nil {
		v.Error = err.Error()
		return v
	}
	if len(b) > maxMarkdownBody {
		v.Error = "markdown body over the size cap"
		return v
	}
	name, err := writeBody(dir, b)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Body, v.Size = name, len(b)
	return v
}

func isMarkdownType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	}
	return mt == "text/markdown" || mt == "text/x-markdown"
}

func writeMarkdown(dir string, v *markdownVariant) error {
	return writeJSON(filepath.Join(dir, "markdown.json"), v)
}
