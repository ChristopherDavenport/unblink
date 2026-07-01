// Package tokens provides cheap token estimation and Markdown pagination so the
// browser never returns an unbounded blob to the model. The estimator is a
// deliberate heuristic (~4 characters per token); it sits behind an interface so
// a real offline tokenizer can replace it later without touching callers.
package tokens

import "unicode/utf8"

// Estimator estimates the number of tokens in a string.
type Estimator interface {
	Estimate(string) int
}

// Heuristic estimates ~1 token per 4 runes. It is offline, deterministic, and
// good enough to bound page sizes (the consumer is Claude, for which no exact
// pure-Go tokenizer exists anyway).
type Heuristic struct{}

// Estimate returns the approximate token count of s.
func (Heuristic) Estimate(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// Default is the estimator used by the package-level Estimate.
var Default Estimator = Heuristic{}

// Estimate returns the approximate token count of s using the Default estimator.
func Estimate(s string) int { return Default.Estimate(s) }
