//go:build corpus

package corpus

import "testing"

func TestVerifyShapeCells(t *testing.T) {
	n := func(v int) *int { return &v }
	have := &oracle{Tables: n(2), Details: n(0), SVGTexts: n(5), HandlerAttrs: n(1), Forms: 1, Dir: "rtl", Lang: "ar"}
	old := &oracle{} // a capture made before the counts existed
	for _, tc := range []struct {
		cell string
		o    *oracle
		want string
	}{
		{"feature:tables", have, verdictSupported},
		{"feature:details", have, verdictContradicted},
		{"feature:svg-chart", have, verdictSupported},
		{"feature:event-handler-attrs", have, verdictSupported},
		{"feature:forms", have, verdictSupported},
		{"feature:forms", old, verdictContradicted},
		{"shape:rtl", have, verdictSupported},
		{"shape:non-english", have, verdictSupported},
		{"feature:tables", old, verdictUnchecked},
		{"shape:rtl", old, verdictUnchecked},
		{"shape:non-english", &oracle{Lang: "en-GB"}, verdictContradicted},
		{"shape:rtl", &oracle{Dir: "ltr"}, verdictContradicted},
	} {
		got := verifyCell(tc.cell, &evidence{stacks: map[string]string{}, own: map[string]bool{}, oracle: tc.o}, 0, false)
		if got.Verdict != tc.want {
			t.Errorf("%s: verdict %q (%s), want %q", tc.cell, got.Verdict, got.Evidence, tc.want)
		}
	}
	for cell, stack := range map[string]string{"bundler:turbopack": "turbopack", "framework:astro": "astro"} {
		ev := &evidence{stacks: map[string]string{stack: "marker"}, own: map[string]bool{}, oracle: old}
		if got := verifyCell(cell, ev, 0, false); got.Verdict != verdictSupported {
			t.Errorf("%s: verdict %q, want supported", cell, got.Verdict)
		}
	}
}
