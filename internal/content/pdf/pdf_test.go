package pdf

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConvertExtractsText(t *testing.T) {
	raw, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	md, title, err := Convert(context.Background(), raw)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(md, "Hello unblink PDF sentinel") {
		t.Errorf("extracted text missing sentinel:\n%s", md)
	}
	if title != "Sentinel Report" {
		t.Errorf("title = %q, want Sentinel Report", title)
	}
	if !strings.Contains(md, "page(s)") {
		t.Errorf("expected page count in output:\n%s", md)
	}
}

func TestConvertMalformedDoesNotPanic(t *testing.T) {
	// Garbage that starts like a PDF but is not a valid document.
	md, _, err := Convert(context.Background(), []byte("%PDF-1.4\n garbage \xff\xfe not a real pdf"))
	if err == nil && !strings.Contains(md, "PDF") {
		t.Errorf("expected an error or manifest for malformed pdf, got md=%q err=%v", md, err)
	}
	// The point is that it returned rather than panicking.
}

func TestConvertHonoursContext(t *testing.T) {
	raw, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline has passed
	if _, _, err := Convert(ctx, raw); err == nil {
		t.Error("expected context error on expired deadline")
	}
}
