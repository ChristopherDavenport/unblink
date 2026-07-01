// Package pdf extracts text from PDF documents and renders it as Markdown. The
// underlying library (dslipak/pdf) can panic on malformed input and has no OCR,
// so extraction is wrapped in a recover, bounded by the caller's context, and
// falls back to a manifest when a PDF yields no extractable text.
//
// It does not import the HTML parser — dom keeps that monopoly.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	dpdf "github.com/dslipak/pdf"
)

// Convert extracts text from a PDF and renders it as Markdown, returning the
// document title when present in the PDF metadata. It honours ctx so a
// pathological PDF cannot hang past the request deadline, and never panics.
func Convert(ctx context.Context, raw []byte) (markdown, title string, err error) {
	type result struct {
		md, title string
		err       error
	}
	ch := make(chan result, 1) // buffered so a late extract goroutine never blocks
	go func() {
		md, t, e := extract(raw)
		ch <- result{md, t, e}
	}()
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case r := <-ch:
		return r.md, r.title, r.err
	}
}

func extract(raw []byte) (md, title string, err error) {
	// dslipak/pdf panics on some malformed PDFs — contain it as a clean error.
	defer func() {
		if r := recover(); r != nil {
			md, title, err = "", "", fmt.Errorf("content/pdf: recovered from panic: %v", r)
		}
	}()

	r, err := dpdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", "", fmt.Errorf("content/pdf: open: %w", err)
	}
	title = pdfTitle(r)
	pages := r.NumPage()

	tr, err := r.GetPlainText()
	if err != nil {
		// Encrypted or otherwise not extractable — manifest, not an error.
		return noTextManifest(title, pages), title, nil
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, tr); err != nil {
		return noTextManifest(title, pages), title, nil
	}
	text := strings.TrimSpace(normalizeText(buf.String()))
	if text == "" {
		// Scanned/image-only PDF (no OCR) — manifest fallback.
		return noTextManifest(title, pages), title, nil
	}

	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	fmt.Fprintf(&b, "_PDF document, %d page(s)._\n\n", pages)
	b.WriteString(text)
	b.WriteString("\n")
	return b.String(), title, nil
}

// pdfTitle reads the /Info /Title metadata, best-effort (never panics).
func pdfTitle(r *dpdf.Reader) (title string) {
	defer func() { _ = recover() }()
	return strings.TrimSpace(r.Trailer().Key("Info").Key("Title").Text())
}

func noTextManifest(title string, pages int) string {
	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	fmt.Fprintf(&b, "_PDF document, %d page(s), no extractable text (likely scanned or image-only)._\n", pages)
	return b.String()
}

// normalizeText trims trailing whitespace per line and collapses runs of blank
// lines, taming the noisy output of PDF text extraction.
func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t\r")
	}
	out := strings.Join(lines, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}
