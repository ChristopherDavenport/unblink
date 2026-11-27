package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/content/pdf"
)

// buildPDF assembles a classic-xref PDF from object bodies (object i+1 is
// objs[i]). trailerExtra is appended inside the trailer dictionary; the token
// XREFOFF in it is replaced by the byte offset of the xref table.
func buildPDF(objs []string, trailerExtra string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	extra := strings.ReplaceAll(trailerExtra, "XREFOFF", fmt.Sprint(xref))
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R %s >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, extra, xref)
	return b.Bytes()
}

// xrefPrevCycle is a document whose trailer names its own xref table as the
// previous one. The library follows /Prev without a visited set, so opening
// the document never finishes.
func xrefPrevCycle() []byte {
	return buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 0 /Kids [] >>",
	}, "/Prev XREFOFF")
}

// quadraticPageTree is a document whose page tree lists one page object
// `pages` times. The library finds page i by rescanning the kids from the
// start and re-reading the kid object each step, so extraction costs
// pages²/2 object reads — 2×10¹⁰ at this size, from a file of about 1.2 MB,
// well under the 10 MiB fetch cap.
func quadraticPageTree() []byte {
	const pages = 200_000
	content := "BT /F1 12 Tf (x) Tj ET"
	return buildPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", pages, strings.Repeat("3 0 R ", pages)),
		"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, "")
}

// TestExtractionStopsWhenContextEnds is the guarantee Convert documents: the
// extraction itself — not just the request waiting on it — stops soon after
// its context ends. Each input keeps the library busy far beyond the test's
// patience, in a different part of it (opening the document; walking the page
// tree), so Extract returning at all shows the work was abandoned, and the
// context error shows why.
func TestExtractionStopsWhenContextEnds(t *testing.T) {
	const (
		runFor   = 150 * time.Millisecond // extraction must still be busy when this ends
		stopSlop = 5 * time.Second        // generous: loaded CI, -race
	)
	for name, raw := range map[string][]byte{
		"xref Prev cycle":     xrefPrevCycle(),
		"quadratic page tree": quadraticPageTree(),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), runFor)
			defer cancel()

			done := make(chan error, 1)
			go func() {
				_, _, err := pdf.Extract(ctx, raw)
				done <- err
			}()

			<-ctx.Done()
			ended := time.Now()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Extract error = %v, want context.DeadlineExceeded", err)
				}
				t.Logf("extraction stopped %v after its context ended", time.Since(ended))
			case <-time.After(stopSlop):
				t.Fatalf("extraction still running %v after its context ended", stopSlop)
			}
		})
	}
}

// TestConvertReturnsContextError checks the same inputs through the public
// entry point: Convert reports the cancellation rather than a manifest or a
// recovered-panic error.
func TestConvertReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	md, _, err := pdf.Convert(ctx, xrefPrevCycle())
	if !errors.Is(err, context.DeadlineExceeded) || md != "" {
		t.Fatalf("Convert = (%q, %v), want empty markdown and context.DeadlineExceeded", md, err)
	}
}
