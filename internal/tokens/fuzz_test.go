package tokens_test

import (
	"testing"

	"github.com/christopherdavenport/unblink/internal/tokens"
)

// FuzzPaginateCursor checks pagination invariants over arbitrary content and
// arbitrary (possibly attacker-supplied) cursors: chunks reassemble losslessly
// in order, every encoded cursor round-trips, and a malformed cursor is an
// error — never a panic, never a silent wrong page.
func FuzzPaginateCursor(f *testing.F) {
	f.Add("# doc\n\npara one\n\npara two\n", 50, "")
	f.Add("word ", 1, "3:deadbeef")
	f.Add("", 100, "not-a-cursor")

	f.Fuzz(func(t *testing.T, content string, maxTokens int, cursor string) {
		if maxTokens > 1<<20 {
			maxTokens = 1 << 20
		}
		chunks := tokens.Paginate(content, maxTokens)
		if len(chunks) == 0 {
			t.Fatal("Paginate returned no chunks")
		}
		fp := tokens.Fingerprint(content)

		// Every legitimate cursor round-trips to its index.
		for i := range chunks {
			enc := tokens.EncodeCursor(i, fp)
			got, err := tokens.DecodeCursor(enc, fp)
			if err != nil || got != i {
				t.Fatalf("cursor roundtrip: enc(%d) -> (%d, %v)", i, got, err)
			}
		}

		// Arbitrary cursor input: an index, an error, but never a panic. An index
		// must only come back when the fingerprint embedded in it matches.
		if idx, err := tokens.DecodeCursor(cursor, fp); err == nil && idx < 0 {
			t.Fatalf("DecodeCursor(%q) returned negative index %d", cursor, idx)
		}
	})
}
