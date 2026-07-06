package js

import "strings"

// fixClassRangeHyphen escapes a '-' that immediately follows a regex
// character-class shorthand (\s \S \d \D \w \W) in JS source, turning e.g.
// /[\s-_]/ into /[\s\-_]/ before goja compiles it.
//
// Why: goja translates a JS regex to a Go (re2) pattern via parser.TransformRegExp,
// which expands a shorthand like \s into its literal character set (ending at
// U+FEFF) and leaves the following '-' as a range operator, yielding a
// descending, invalid range (U+FEFF-'_'). Go's regexp then rejects it with "invalid character class
// range", and goja only falls back to its ECMAScript-accurate regexp2 engine on
// ErrInvalidRepeatSize — so the whole regex throws a SyntaxError at compile time.
// That kills the enclosing script/module: a single such regex in a bundle (the
// ubiquitous camelCase/slugify helper /[\s-_]+/ among them) silently aborts SPA
// hydration. See docs/decisions and the astro-island regression tests.
//
// Correctness: in ECMAScript a '-' immediately after a class shorthand is already
// a literal (a shorthand can't be a range endpoint), and '\-' is equivalent to
// '-' in every JS lexical context — regex class, regex body, string, template,
// comment — so inserting the backslash preserves semantics wherever it lands, and
// this needs no JS tokenizer. The one case a '-' after a shorthand *letter* is a
// real range is when the leading backslash is itself escaped (`[\\s-z]` = literal
// '\' then the range s-z); shorthandEscapeBefore excludes it by requiring the
// backslash run before the letter to be odd (a genuine shorthand escape).
func fixClassRangeHyphen(src string) string {
	// Detection pass: avoid allocating when there's nothing to rewrite (the
	// overwhelmingly common case), which keeps this off the hot path for the
	// multi-MB bundles that flow through here.
	rewrite := false
	for i := 1; i < len(src); i++ {
		if src[i] == '-' && shorthandEscapeBefore(src, i) {
			rewrite = true
			break
		}
	}
	if !rewrite {
		return src
	}
	var b strings.Builder
	b.Grow(len(src) + 16)
	for i := 0; i < len(src); i++ {
		if src[i] == '-' && shorthandEscapeBefore(src, i) {
			b.WriteByte('\\')
		}
		b.WriteByte(src[i])
	}
	return b.String()
}

// shorthandEscapeBefore reports whether src[i] (which the caller has checked is a
// '-') is immediately preceded by a genuine class-shorthand escape (\s \S \d \D
// \w \W): the letter at i-1 is a shorthand letter and the run of backslashes
// ending at i-2 is odd, so the letter is escaped rather than a literal following
// an escaped backslash.
func shorthandEscapeBefore(src string, i int) bool {
	if i < 2 {
		return false
	}
	switch src[i-1] {
	case 's', 'S', 'd', 'D', 'w', 'W':
	default:
		return false
	}
	backslashes := 0
	for j := i - 2; j >= 0 && src[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 1
}
