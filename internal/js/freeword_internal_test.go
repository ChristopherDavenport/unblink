package js

import "testing"

func TestFreeWordIn(t *testing.T) {
	for _, c := range []struct {
		src  string
		want bool
	}{
		{`typeof require<"u"?require:x`, true},
		{`require("a")`, true},
		{`x ? require : y`, true},
		{`[...require]`, true},
		{`{require}`, true},
		{`a.require("x")`, false},
		{`a?.require`, false},
		{`{require:1}`, false},
		{`{ require : 1 }`, false},
		{`requirejs("x"); __require("y"); required`, false},
		{`(t={exports:{}}).exports`, false},
		{``, false},
	} {
		word := "require"
		if c.src == `(t={exports:{}}).exports` {
			word = "exports"
		}
		if got := freeWordIn(c.src, word); got != c.want {
			t.Errorf("freeWordIn(%q, %q) = %v, want %v", c.src, word, got, c.want)
		}
	}
}
