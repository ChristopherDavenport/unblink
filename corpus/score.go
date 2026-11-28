//go:build corpus

package corpus

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	mdLinkTarget = regexp.MustCompile(`\]\([^)\s]*(?:\s+"[^"]*")?\)`)
	bareURL      = regexp.MustCompile(`https?://\S+`)
)

// stripMarkdown removes what Markdown adds that a browser's innerText never
// shows — link and image targets, bare URLs — so they don't count as junk.
// Remaining syntax characters are punctuation and fall out in tokenize.
func stripMarkdown(md string) string {
	md = mdLinkTarget.ReplaceAllString(md, "] ")
	return bareURL.ReplaceAllString(md, " ")
}

// tokenize lower-cases s and splits it into word tokens on anything that is not
// a letter or digit. Scripts written without spaces (Han, kana, Hangul, Thai)
// yield one token per rune so recall is still meaningful for them.
func tokenize(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Thai):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
	return out
}

const shingleN = 3

func shingleSet(toks []string) map[string]struct{} {
	set := map[string]struct{}{}
	for i := 0; i+shingleN <= len(toks); i++ {
		set[strings.Join(toks[i:i+shingleN], " ")] = struct{}{}
	}
	return set
}

// shingleRecall is the fraction of want's distinct 3-token shingles present in
// got. Shingles are order-sensitive only locally, so a block that moved (a
// sidebar emitted after the article) costs a couple of boundary shingles, not
// the block — while a bag of the right words in the wrong places still fails.
// Texts too short to shingle fall back to tokenRecall.
func shingleRecall(want, got []string) float64 {
	ws := shingleSet(want)
	if len(ws) == 0 {
		return tokenRecall(want, got)
	}
	gs := shingleSet(got)
	hit := 0
	for s := range ws {
		if _, ok := gs[s]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(ws))
}

// tokenRecall is multiset recall: how much of want's token mass got covers.
func tokenRecall(want, got []string) float64 {
	if len(want) == 0 {
		return 0
	}
	have := map[string]int{}
	for _, t := range got {
		have[t]++
	}
	hit := 0
	for _, t := range want {
		if have[t] > 0 {
			have[t]--
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

// tokenPrecision is the share of got's token mass that the reference also
// contains: 1 - precision is the junk signal (text unblink emitted that the
// browser never displayed).
func tokenPrecision(got, reference []string) float64 {
	if len(got) == 0 {
		return 0
	}
	return tokenRecall(got, reference)
}

// ratio is got/want. ok is false when want is zero: a count the oracle did not
// observe has no ratio, and a raw count standing in for one (94 links / 0)
// would swamp any mean it entered.
func ratio(got, want int) (r float64, ok bool) {
	if want <= 0 {
		return 0, false
	}
	return float64(got) / float64(want), true
}

// missingShingles returns up to limit of want's shingles absent from got, in
// document order — the text the engine lost, for a human to read.
func missingShingles(want, got []string, limit int) []string {
	gs := shingleSet(got)
	seen := map[string]struct{}{}
	var out []string
	for i := 0; i+shingleN <= len(want) && len(out) < limit; i++ {
		s := strings.Join(want[i:i+shingleN], " ")
		if _, ok := gs[s]; ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
