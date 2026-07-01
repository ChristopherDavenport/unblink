package tokens_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/tokens"
)

func buildMarkdown(blocks int) string {
	var b strings.Builder
	for i := 0; i < blocks; i++ {
		b.WriteString("This is paragraph block number with a fair amount of filler text so each ")
		b.WriteString("block carries a meaningful token weight for the paginator to account for.\n\n")
	}
	return b.String()
}

func TestPaginateRoundTrip(t *testing.T) {
	md := buildMarkdown(20)
	chunks := tokens.Paginate(md, 40)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// Round-trip: joining chunks reproduces the document's blocks.
	wantBlocks := strings.Split(strings.TrimSpace(md), "\n\n")
	var want []string
	for _, blk := range wantBlocks {
		if strings.TrimSpace(blk) != "" {
			want = append(want, strings.TrimSpace(blk))
		}
	}
	if got := strings.Join(chunks, "\n\n"); got != strings.Join(want, "\n\n") {
		t.Errorf("round-trip mismatch:\n got: %q\nwant: %q", got, strings.Join(want, "\n\n"))
	}
}

func TestPaginateBudget(t *testing.T) {
	md := buildMarkdown(20)
	const budget = 40
	for i, c := range tokens.Paginate(md, budget) {
		// Each chunk should respect the budget (blocks here are smaller than it).
		if got := tokens.Estimate(c); got > budget {
			t.Errorf("chunk %d over budget: %d > %d", i, got, budget)
		}
	}
}

func TestPaginateSingleChunk(t *testing.T) {
	chunks := tokens.Paginate("short content", 6000)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
}

func TestCursorRoundTrip(t *testing.T) {
	fp := tokens.Fingerprint("hello world")
	cur := tokens.EncodeCursor(2, fp)
	if got := tokens.DecodeCursor(cur, fp); got != 2 {
		t.Errorf("decode = %d, want 2", got)
	}
	// Stale fingerprint resets to page 0.
	if got := tokens.DecodeCursor(cur, "deadbeef"); got != 0 {
		t.Errorf("stale decode = %d, want 0", got)
	}
	// Empty / garbage cursors reset to 0.
	if got := tokens.DecodeCursor("", fp); got != 0 {
		t.Errorf("empty decode = %d, want 0", got)
	}
	if got := tokens.DecodeCursor("!!!not-base64!!!", fp); got != 0 {
		t.Errorf("garbage decode = %d, want 0", got)
	}
}
