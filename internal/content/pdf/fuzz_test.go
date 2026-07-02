package pdf_test

import (
	"context"
	"os"
	"testing"

	"github.com/christopherdavenport/unblink/internal/content/pdf"
)

// FuzzConvert hammers the PDF extractor with malformed input. dslipak/pdf is
// the project's riskiest dependency (pre-1.0, parses untrusted bytes, known to
// panic) — Convert's contract is that the recover+manifest containment holds
// for ANY input: an error or a manifest, never a panic.
func FuzzConvert(f *testing.F) {
	if b, err := os.ReadFile("testdata/sample.pdf"); err == nil {
		f.Add(b)
	}
	f.Add([]byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF"))
	f.Add([]byte("%PDF-"))
	f.Add([]byte("not a pdf at all"))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = pdf.Convert(context.Background(), data)
	})
}
