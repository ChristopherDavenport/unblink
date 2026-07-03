package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// tokenizerNote is printed under the token table so every copy of the numbers
// carries its methodology.
const tokenizerNote = "Tokens are offline o200k_base BPE counts (Claude's tokenizer is not public; ratios are stable across modern BPE tokenizers); bytes are the tokenizer-independent ground truth."

var (
	encOnce sync.Once
	enc     *tiktoken.Tiktoken
)

// countTokens is the token cost of text an agent would ingest: a real BPE
// count (tiktoken o200k_base) with the dictionaries embedded in the loader —
// no network, reproducible anywhere. Unlike a chars/4 heuristic it sees how
// BPE prices punctuation-dense output (a11y-tree YAML, structured JSON), which
// is exactly the effect a cross-tool token comparison must capture.
func countTokens(s string) int {
	encOnce.Do(func() {
		tiktoken.SetBpeLoader(&tiktoken_loader.OfflineLoader{})
		var err error
		enc, err = tiktoken.GetEncoding("o200k_base")
		if err != nil {
			fmt.Fprintf(os.Stderr, "membench: tokenizer init: %v — falling back to chars/4\n", err)
		}
	})
	if enc == nil {
		return (len(s) + 3) / 4
	}
	return len(enc.Encode(s, nil, nil))
}
