package browser

import "testing"

func TestRenderCollapsedRule(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after int
		want          bool
	}{
		{"hydration crash: a 9 KB page becomes an apology", 9149, 140, true},
		{"body wiped", 4500, 0, true},
		{"client shell: nothing was there to lose", 40, 0, false},
		{"just under the floor", collapseMinBefore - 1, 0, false},
		{"at the floor", collapseMinBefore, 0, true},
		{"route change to a shorter view", 4500, 600, false},
		{"paginated table keeps a tenth", 20000, 2000, false},
		{"just under a tenth", 20000, 1999, true},
		{"the render added content", 3000, 9000, false},
		{"unchanged", 3000, 3000, false},
	} {
		if got := renderCollapsed(tc.before, tc.after); got != tc.want {
			t.Errorf("%s: renderCollapsed(%d, %d) = %v, want %v", tc.name, tc.before, tc.after, got, tc.want)
		}
	}
}
