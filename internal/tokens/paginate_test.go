package tokens_test

import (
	"errors"
	"fmt"
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
	if got, err := tokens.DecodeCursor(cur, fp); err != nil || got != 2 {
		t.Errorf("decode = %d, %v; want 2, nil", got, err)
	}
	// A stale fingerprint is an explicit error, never a silent page-1 restart.
	if _, err := tokens.DecodeCursor(cur, "deadbeef"); !errors.Is(err, tokens.ErrStaleCursor) {
		t.Errorf("stale decode err = %v, want ErrStaleCursor", err)
	}
	// An empty cursor is page 0; a garbage cursor is a distinct error.
	if got, err := tokens.DecodeCursor("", fp); err != nil || got != 0 {
		t.Errorf("empty decode = %d, %v; want 0, nil", got, err)
	}
	if _, err := tokens.DecodeCursor("!!!not-base64!!!", fp); err == nil || errors.Is(err, tokens.ErrStaleCursor) {
		t.Errorf("garbage decode err = %v, want a malformed-cursor error", err)
	}
}

func TestFingerprintCoversWholeContent(t *testing.T) {
	// Two documents sharing a 64-byte prefix and equal length must not collide:
	// the old fingerprint hashed only len + the first 64 bytes and served
	// wrong-page chunks silently.
	prefix := strings.Repeat("x", 64)
	a := tokens.Fingerprint(prefix + "tail-one")
	b := tokens.Fingerprint(prefix + "tail-two")
	if a == b {
		t.Error("fingerprints collide for same-prefix same-length content")
	}
}

// longBlock is one block (no blank lines) of n lines, far over any small budget.
func longBlock(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("- item %d of a long list with enough words to weigh something", i)
	}
	return strings.Join(lines, "\n")
}

// A block larger than the budget is packed into the running chunk, not flushed
// around: the small preamble before it does not become a page of its own, and
// the block's tail shares a chunk with what follows.
func TestPaginateOversizedBlockFillsChunks(t *testing.T) {
	const budget = 200
	md := "# Title\n\nA short preamble.\n\n" + longBlock(60) + "\n\nA closing paragraph."
	chunks := tokens.Paginate(md, budget)
	if len(chunks) < 3 {
		t.Fatalf("chunks = %d, want several", len(chunks))
	}
	first := chunks[0]
	if !strings.Contains(first, "A short preamble.") || !strings.Contains(first, "- item 0 ") {
		t.Errorf("first chunk does not run on into the long block:\n%s", first)
	}
	if got := tokens.Estimate(first); got < budget/2 {
		t.Errorf("first chunk is only %d tokens of a %d budget", got, budget)
	}
	last := chunks[len(chunks)-1]
	if !strings.Contains(last, "- item 59 ") || !strings.Contains(last, "A closing paragraph.") {
		t.Errorf("the block's tail and the following block are not together:\n%s", last)
	}
	for i, c := range chunks {
		if got := tokens.Estimate(c); got > budget+budget/10 {
			t.Errorf("chunk %d over budget: %d > %d", i, got, budget)
		}
	}
	// Nothing is lost or reordered: every line appears once, in order.
	joined := strings.Join(chunks, "\n")
	prev := -1
	for i := 0; i < 60; i++ {
		at := strings.Index(joined, fmt.Sprintf("- item %d of", i))
		if at <= prev {
			t.Fatalf("item %d missing or out of order", i)
		}
		prev = at
	}
	if strings.Count(joined, "- item ") != 60 {
		t.Errorf("lines duplicated or dropped")
	}
}

// A single line over the budget still gets through, alone.
func TestPaginateOversizedLine(t *testing.T) {
	line := strings.Repeat("word ", 400)
	chunks := tokens.Paginate("intro\n\n"+line+"\n\noutro", 50)
	if got := strings.Join(chunks, " "); !strings.Contains(got, "intro") || !strings.Contains(got, "outro") || strings.Count(got, "word") != 400 {
		t.Errorf("content lost: %d chunks", len(chunks))
	}
}
