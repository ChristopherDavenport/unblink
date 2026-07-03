package main

import "fmt"

// selfcheck runs directional sanity assertions over the token results before
// numbers are published (-selfcheck). A violation means a broken adapter or
// meter, not a surprising finding — investigate before publishing.
func selfcheck(toks []tokenResult) []string {
	var bad []string
	byEngine := map[string]tokenResult{}
	for _, t := range toks {
		byEngine[t.engine] = t
	}
	get := func(t tokenResult, task, fx string) (taskTokens, bool) {
		tt, ok := t.perTask[task][fx]
		return tt, ok && !tt.failed && !tt.na && tt.tokens > 0
	}

	// Meter wiring: text outputs land near 3–5 bytes/token; far outside [2, 8]
	// means the tokenizer or the text capture is broken.
	for _, t := range toks {
		for task, fxs := range t.perTask {
			for fx, tt := range fxs {
				if tt.failed || tt.na || tt.tokens == 0 {
					continue
				}
				r := float64(tt.bytes) / float64(tt.tokens)
				if r < 2 || r > 8 {
					bad = append(bad, fmt.Sprintf("%s %s %s: %.1f bytes/token — meter wiring suspect", t.engine, task, fx, r))
				}
			}
		}
	}

	ub, hasUB := byEngine["unblink"]
	if hasUB {
		// Orientation must be cheaper than exhaustively reading the page; on a
		// long article it must also beat the one-shot read. (Not asserted for
		// short pages, where a well-reduced article can legitimately cost less
		// than a structured orientation blob.)
		for _, fx := range []string{"junky-portal", "longread", "noisy-portal"} {
			or, ok1 := get(ub, taskOrient, fx)
			rf, ok2 := get(ub, taskReadFull, fx)
			if ok1 && ok2 && or.tokens >= rf.tokens {
				bad = append(bad, fmt.Sprintf("unblink orient (%d) ≥ read-full (%d) on %s", or.tokens, rf.tokens, fx))
			}
		}
		if or, ok1 := get(ub, taskOrient, "longread"); ok1 {
			if ra, ok2 := get(ub, taskReadArticle, "longread"); ok2 && or.tokens >= ra.tokens {
				bad = append(bad, fmt.Sprintf("unblink orient (%d) ≥ read-article (%d) on longread", or.tokens, ra.tokens))
			}
		}
		// A budget-capped read never exceeds the cursor-exhausted read.
		for _, fx := range []string{"junky-portal", "longread", "noisy-portal"} {
			ra, ok1 := get(ub, taskReadArticle, fx)
			rf, ok2 := get(ub, taskReadFull, fx)
			if ok1 && ok2 && rf.tokens < ra.tokens {
				bad = append(bad, fmt.Sprintf("unblink read-full (%d) < read-article (%d) on %s", rf.tokens, ra.tokens, fx))
			}
		}
		// The thesis direction: a full-page dump on a nav-heavy portal must
		// dwarf unblink's reduced article. If it doesn't, the competitor
		// adapter is likely capturing the wrong output.
		for _, other := range []string{"playwright", "charlotte", "obscura", "lightpanda"} {
			t, ok := byEngine[other]
			if !ok {
				continue
			}
			o, ok1 := get(t, taskReadArticle, "noisy-portal")
			u, ok2 := get(ub, taskReadArticle, "noisy-portal")
			if ok1 && ok2 && o.tokens <= u.tokens*5 {
				bad = append(bad, fmt.Sprintf("%s read-article on noisy-portal (%d) not ≫ unblink (%d) — adapter suspect", other, o.tokens, u.tokens))
			}
		}
	}
	return bad
}
