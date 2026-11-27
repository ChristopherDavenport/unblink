// Package pdf extracts text from PDF documents and renders it as Markdown. The
// underlying library (dslipak/pdf, vendored with loop bounds at third_party/pdf
// — see docs/decisions/0001) can panic on malformed input and has no OCR, so
// extraction is wrapped in a recover, cancelled by the caller's context or a
// hard wall-clock (whichever ends first), and falls back to a manifest when a
// PDF yields no extractable text.
//
// It does not import the HTML parser — dom keeps that monopoly.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	dpdf "github.com/christopherdavenport/unblink/third_party/pdf"
)

// maxExtractTime bounds one extraction regardless of the caller's context —
// an MCP request context may carry no deadline, and a hostile PDF must not
// hold a request hostage. Any legitimate 10 MiB PDF extracts orders of
// magnitude faster.
const maxExtractTime = 20 * time.Second

// Convert extracts text from a PDF and renders it as Markdown, returning the
// document title when present in the PDF metadata. It never panics.
//
// What ending ctx (or exceeding maxExtractTime) guarantees:
//
//   - Convert returns the context's error immediately. That is unconditional:
//     extraction runs on its own goroutine and Convert does not wait for it.
//   - The extraction goroutine then stops at the vendored library's next
//     cancellation checkpoint — any read of the document bytes, any object
//     lookup, any CMap-decoded character (third_party/pdf/cancel.go) — and
//     its CPU is released. Every loop we know of in the library reaches one
//     within microseconds to milliseconds.
//
// What it does not guarantee: the cancellation is cooperative, so a library
// loop that reaches no checkpoint would keep its goroutine (and one CPU)
// running after Convert has returned, until the loop ends by itself. None is
// known; the fuzz target is how we look for one.
func Convert(ctx context.Context, raw []byte) (markdown, title string, err error) {
	ctx, cancel := context.WithTimeout(ctx, maxExtractTime)
	defer cancel() // also stops the goroutine if we return for any other reason
	type result struct {
		md, title string
		err       error
	}
	ch := make(chan result, 1) // buffered so a late extract goroutine never blocks
	go func() {
		md, t, e := extract(ctx, raw)
		ch <- result{md, t, e}
	}()
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case r := <-ch:
		return r.md, r.title, r.err
	}
}

// extract runs synchronously and returns ctx's error soon after ctx ends (see
// Convert for how soon, and the limit of that promise).
func extract(ctx context.Context, raw []byte) (md, title string, err error) {
	defer func() {
		// dslipak/pdf panics on some malformed PDFs — contain it as a clean
		// error. Its cancellation checkpoints panic too.
		if r := recover(); r != nil {
			md, title, err = "", "", fmt.Errorf("content/pdf: recovered from panic: %v", r)
		}
		// Once ctx has ended, whatever came back is an artefact of the
		// cancellation (a failed read, a checkpoint panic, a manifest built
		// from a half-read document) — report the cancellation itself.
		if cerr := ctx.Err(); cerr != nil {
			md, title, err = "", "", cerr
		}
	}()

	r, err := dpdf.NewReaderContext(ctx, bytes.NewReader(raw), int64(len(raw)))
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
