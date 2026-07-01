package tokens

import (
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// ErrStaleCursor reports a cursor whose fingerprint no longer matches the
// content: the page changed between paginated calls, so the cursor's chunk
// index is meaningless. Callers should restart without a cursor.
var ErrStaleCursor = errors.New("cursor is stale: the content changed since it was issued")

// Paginate splits markdown into chunks that each fit within maxTokens, breaking
// only at blank-line block boundaries. A single block larger than the budget is
// hard-split on line boundaries. Joining the returned chunks with "\n\n"
// reproduces the document's blocks in order (round-trip invariant).
func Paginate(markdown string, maxTokens int) []string {
	blocks := splitBlocks(markdown)
	if len(blocks) == 0 {
		return []string{""}
	}
	if maxTokens <= 0 {
		return []string{strings.Join(blocks, "\n\n")}
	}

	var chunks []string
	var cur []string
	curTokens := 0
	flush := func() {
		if len(cur) > 0 {
			chunks = append(chunks, strings.Join(cur, "\n\n"))
			cur = nil
			curTokens = 0
		}
	}

	for _, blk := range blocks {
		bt := Estimate(blk)
		if bt > maxTokens {
			flush()
			chunks = append(chunks, hardSplit(blk, maxTokens)...)
			continue
		}
		if curTokens > 0 && curTokens+bt > maxTokens {
			flush()
		}
		cur = append(cur, blk)
		curTokens += bt
	}
	flush()

	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

func splitBlocks(md string) []string {
	var out []string
	for _, p := range strings.Split(md, "\n\n") {
		if t := strings.Trim(p, "\n"); strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

func hardSplit(block string, maxTokens int) []string {
	var out []string
	var cur []string
	ct := 0
	for _, ln := range strings.Split(block, "\n") {
		lt := Estimate(ln)
		if ct > 0 && ct+lt > maxTokens {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
			ct = 0
		}
		cur = append(cur, ln)
		ct += lt
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}

// Fingerprint is a short hash over the entire content, used to detect a stale
// cursor (the page changed between paginated calls).
func Fingerprint(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return strconv.FormatUint(h.Sum64(), 16)
}

// EncodeCursor produces an opaque cursor pointing at chunk nextIndex of content
// identified by fp.
func EncodeCursor(nextIndex int, fp string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fp + ":" + strconv.Itoa(nextIndex)))
}

// DecodeCursor returns the chunk index encoded in cursor. An empty cursor is
// page 0. A cursor whose fingerprint does not match fp returns ErrStaleCursor;
// a malformed cursor returns a descriptive error. Callers surface both rather
// than silently restarting at page 1.
func DecodeCursor(cursor, fp string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("malformed cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return 0, errors.New("malformed cursor")
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil || i < 0 {
		return 0, errors.New("malformed cursor")
	}
	if parts[0] != fp {
		return 0, ErrStaleCursor
	}
	return i, nil
}
