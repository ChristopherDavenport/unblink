package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

func TestMarkCollapsedDetails(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"closed: marked, content kept",
			`<details><summary>caches</summary><a href="/a">Archive</a></details>`,
			`<details><summary>▸ caches</summary><a href="/a">Archive</a></details>`},
		{"open: untouched",
			`<details open=""><summary>caches</summary><p>x</p></details>`,
			`<details open=""><summary>caches</summary><p>x</p></details>`},
		{"marker goes inside a heading summary",
			`<details><summary><h3>How do I reset?</h3></summary><p>Hold the button.</p></details>`,
			`<summary><h3>▸ How do I reset?</h3></summary><p>Hold the button.</p>`},
		{"no summary: the browser's default label",
			`<details><p>body</p></details>`,
			`<details><summary>▸ Details</summary><p>body</p></details>`},
		{"nested",
			`<details><summary>outer</summary><details><summary>inner</summary>deep</details></details>`,
			`<summary>▸ outer</summary><details><summary>▸ inner</summary>deep</details>`},
		{"summary starting with an empty icon element",
			`<details><summary><span class="icon"></span>More</summary>x</details>`,
			`<summary>▸ <span class="icon"></span>More</summary>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := parse(t, `<html><body>`+c.in+`</body></html>`)
			dom.MarkCollapsedDetails(doc)
			if got := renderString(t, doc); !strings.Contains(got, c.want) {
				t.Errorf("got %s\nwant it to contain %s", got, c.want)
			}
		})
	}
	dom.MarkCollapsedDetails(nil)
}
