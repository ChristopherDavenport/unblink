package dom_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

// navBlock builds a link-dense block big enough to qualify for duplicate
// suppression, tagged so occurrences are countable in the output.
func navBlock(tag string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<nav><p>%s section menu with world news, politics, sports and more.</p>`, tag)
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, `<a href="/section-%d">Section %d headline about ongoing events %s</a> `, i, i, tag)
	}
	b.WriteString(`</nav>`)
	return b.String()
}

func TestStripDuplicateBlocksRemovesRepeatedNav(t *testing.T) {
	nav := navBlock("NAVDUP")
	doc := parse(t, `<html><body>
		<div class="desktop-header">`+nav+`</div>
		<main><p>Actual page content stays.</p></main>
		<div class="mobile-drawer">`+nav+`</div>
	</body></html>`)
	dom.StripDuplicateBlocks(doc)
	out, err := dom.Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.Count(out, "NAVDUP section menu"); got != 1 {
		t.Errorf("duplicated nav should survive exactly once, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "Actual page content stays.") {
		t.Errorf("non-duplicate content was removed:\n%s", out)
	}
}

func TestStripDuplicateBlocksKeepsWrapperOfItsOwnContent(t *testing.T) {
	// A wrapper whose only content is the nav shares the nav's signature; that
	// nesting must not count as a duplicate and empty the block out of the page.
	doc := parse(t, `<html><body><div><div>`+navBlock("NESTED")+`</div></div></body></html>`)
	dom.StripDuplicateBlocks(doc)
	out, err := dom.Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.Count(out, "NESTED section menu"); got != 1 {
		t.Errorf("sole nav wrapped in equal-signature divs should survive, got %d occurrences:\n%s", got, out)
	}
}

func TestStripDuplicateBlocksKeepsNonQualifyingRepeats(t *testing.T) {
	chorus := `<p>CHORUS-START ` + strings.Repeat("Repeated chorus line of a song, sung again in full. ", 6) + `</p>`
	smallNav := `<nav><a href="/a">Home</a> <a href="/b">News</a> <a href="/c">About</a></nav>`
	doc := parse(t, `<html><body>`+chorus+`<main>verse</main>`+chorus+smallNav+`<footer>`+smallNav+`</footer></body></html>`)
	dom.StripDuplicateBlocks(doc)
	out, err := dom.Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.Count(out, "CHORUS-START"); got != 2 {
		t.Errorf("linkless repeated prose must be kept, got %d occurrences, want 2:\n%s", got, out)
	}
	if got := strings.Count(out, `href="/a"`); got != 2 {
		t.Errorf("small repeated nav (under text threshold) must be kept, got %d, want 2:\n%s", got, out)
	}
}

func TestStripDuplicateBlocksSignatureDetails(t *testing.T) {
	long := strings.Repeat("Identical visible words in both blocks, listing many stories. ", 5)
	block := func(hrefPrefix, script string) string {
		var b strings.Builder
		b.WriteString(`<div><p>` + long + `</p>`)
		for i := 0; i < 4; i++ {
			fmt.Fprintf(&b, `<a href="%s/%d">Story %d</a> `, hrefPrefix, i, i)
		}
		b.WriteString(script + `</div>`)
		return b.String()
	}

	// Same text, different link targets: not a duplicate.
	doc := parse(t, `<html><body>`+block("/en", "")+block("/fr", "")+`</body></html>`)
	dom.StripDuplicateBlocks(doc)
	out, err := dom.Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, `href="/en/0"`) || !strings.Contains(out, `href="/fr/0"`) {
		t.Errorf("blocks with same text but different links must both survive:\n%s", out)
	}

	// Differing embedded scripts must not disguise otherwise identical blocks.
	doc = parse(t, `<html><body>`+block("/en", `<script>var a=1;</script>`)+block("/en", `<script>var b=2;</script>`)+`</body></html>`)
	dom.StripDuplicateBlocks(doc)
	out, err = dom.Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.Count(out, `href="/en/0"`); got != 1 {
		t.Errorf("script content must not differentiate duplicate blocks, got %d occurrences:\n%s", got, out)
	}
}
