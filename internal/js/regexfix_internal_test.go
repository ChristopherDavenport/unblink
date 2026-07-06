package js

import (
	"strconv"
	"testing"

	"github.com/dop251/goja"
)

func TestFixClassRangeHyphen(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The bug: a hyphen right after a shorthand is escaped.
		{"s-underscore", `/[\s-_]/`, `/[\s\-_]/`},
		{"the camelCase helper", `/^([A-Z])|[\s-_]+(\w)/g`, `/^([A-Z])|[\s\-_]+(\w)/g`},
		{"S negated", `/[\S-x]/`, `/[\S\-x]/`},
		{"d digit", `/[\d-9]/`, `/[\d\-9]/`},
		{"D negated", `/[\D-a]/`, `/[\D\-a]/`},
		{"w word", `/[\w-z]/`, `/[\w\-z]/`},
		{"W negated", `/[\W-b]/`, `/[\W\-b]/`},
		{"multiple in one source", `x=/[\s-a]/; y=/[\d-b]/`, `x=/[\s\-a]/; y=/[\d\-b]/`},

		// Must NOT touch these.
		{"plain range a-z", `/[a-z]/`, `/[a-z]/`},
		{"digit range 0-9", `/[0-9]/`, `/[0-9]/`},
		{"hyphen last already", `/[\s_-]/`, `/[\s_-]/`},
		{"no hyphen", `/[\s_]/`, `/[\s_]/`},
		{"trailing hyphen after shorthand", `/[a\s-]/`, `/[a\s\-]/`}, // trailing '-' after \s is literal in JS; escaping is a no-op
		{"non-shorthand letter", `/[q-z]/`, `/[q-z]/`},
		// Escaped backslash before the letter → a genuine range, leave alone.
		{"escaped-backslash range", `/[\\s-z]/`, `/[\\s-z]/`},
		{"escaped-backslash digit range", `/[\\d-9]/`, `/[\\d-9]/`},
		// Triple backslash: \\ (literal) + \s (shorthand) → the '-' is literal, escape it.
		{"triple backslash shorthand", `/[\\\s-_]/`, `/[\\\s\-_]/`},
		{"empty", ``, ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fixClassRangeHyphen(tc.in); got != tc.want {
				t.Errorf("fixClassRangeHyphen(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFixClassRangeHyphenCompiles is the end-to-end guarantee: the rewritten
// regexes compile in goja (the raw /[\s-_]/ does not), and the escaped-backslash
// range /[\\s-z]/ keeps its range semantics (matches a letter in s..z, not '-').
func TestFixClassRangeHyphenCompiles(t *testing.T) {
	vm := goja.New()

	// Raw form throws; fixed form compiles and behaves.
	if _, err := vm.RunString(`(/[\s-_]/)`); err == nil {
		t.Skip("goja unexpectedly accepts /[\\s-_]/ — the bug this guards is gone")
	}
	v, err := vm.RunString("(" + fixClassRangeHyphen(`/[\s-_]/`) + `).test("_")`)
	if err != nil {
		t.Fatalf("fixed regex failed to compile: %v", err)
	}
	if !v.ToBoolean() {
		t.Errorf("fixed /[\\s\\-_]/ should match '_'")
	}
	// Behaviour preserved: matches space and hyphen too.
	for _, ch := range []string{" ", "-", "_"} {
		res, _ := vm.RunString("(" + fixClassRangeHyphen(`/[\s-_]/`) + ").test(" + strconv.Quote(ch) + ")")
		if !res.ToBoolean() {
			t.Errorf("fixed regex should match %q", ch)
		}
	}

	// The escaped-backslash form is a real range and must be left intact: it
	// matches a backslash or s..z, but NOT '0' and NOT a bare '-'.
	fixed := fixClassRangeHyphen(`/[\\s-z]/`)
	if fixed != `/[\\s-z]/` {
		t.Fatalf("escaped-backslash range was altered: %q", fixed)
	}
	got, err := vm.RunString(`var r=` + fixed + `; [r.test("t"), r.test("0")]`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	arr := got.Export().([]interface{})
	if arr[0] != true || arr[1] != false {
		t.Errorf("range semantics broken: test('t')=%v test('0')=%v, want true,false", arr[0], arr[1])
	}
}
