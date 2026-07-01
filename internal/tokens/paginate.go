package tokens

import (
	"encoding/base64"
	"hash/fnv"
	"strconv"
	"strings"
)

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

// Fingerprint is a short content hash used to detect a stale cursor (the page
// changed between paginated calls).
func Fingerprint(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.Itoa(len(s))))
	head := s
	if len(head) > 64 {
		head = head[:64]
	}
	_, _ = h.Write([]byte(head))
	return strconv.FormatUint(uint64(h.Sum32()), 16)
}

// EncodeCursor produces an opaque cursor pointing at chunk nextIndex of content
// identified by fp.
func EncodeCursor(nextIndex int, fp string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fp + ":" + strconv.Itoa(nextIndex)))
}

// DecodeCursor returns the chunk index encoded in cursor, or 0 if the cursor is
// empty, malformed, or stale (its fingerprint does not match fp).
func DecodeCursor(cursor, fp string) int {
	if cursor == "" {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 || parts[0] != fp {
		return 0
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil || i < 0 {
		return 0
	}
	return i
}
