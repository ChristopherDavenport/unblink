package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

func TestMeasureContentExcludesChrome(t *testing.T) {
	doc := parse(t, `<html><head><title>ignored</title><style>x{}</style></head><body>
		<header>BANNER</header>
		<nav>NAVNAV</nav>
		<div role="search">SEARCH</div>
		<main>
			<header>SCOPED</header>
			<p>Body <a href="/x">LINK</a></p>
			<aside>ASIDE</aside>
			<div style="display:none">HIDDEN</div>
			<script>var js = 1</script>
		</main>
		<footer>FOOTER</footer>
	</body></html>`)
	m := dom.MeasureContent(doc)
	if m.Main == nil || m.Main.Data != "main" {
		t.Fatalf("Main = %v, want the <main> element", m.Main)
	}
	// Region: "SCOPED" + "Body" + "LINK" — the header inside <main> is content
	// (ARIA scoping), the aside is chrome, hidden and script text never count.
	if want := len("SCOPED") + len("Body") + len("LINK"); m.Region != want {
		t.Errorf("Region = %d, want %d", m.Region, want)
	}
	if m.RegionLinks != len("LINK") {
		t.Errorf("RegionLinks = %d, want %d", m.RegionLinks, len("LINK"))
	}
	all := len("BANNER") + len("NAVNAV") + len("SEARCH") + len("SCOPED") + len("Body") + len("LINK") + len("ASIDE") + len("FOOTER")
	if m.Page != all {
		t.Errorf("Page = %d, want %d (every visible byte, chrome included)", m.Page, all)
	}
}

func TestMeasureContentWithoutMainUsesBody(t *testing.T) {
	doc := parse(t, `<html><body>
		<div role="banner">BANNER</div>
		<div role="navigation">NAV</div>
		<p>Content text</p>
		<section><footer>SECTIONFOOT</footer></section>
		<div role="contentinfo">INFO</div>
	</body></html>`)
	m := dom.MeasureContent(doc)
	if m.Main != nil {
		t.Errorf("Main = %v, want nil", m.Main)
	}
	if want := len("Contenttext") + len("SECTIONFOOT"); m.Region != want {
		t.Errorf("Region = %d, want %d", m.Region, want)
	}
}

// Two main landmarks is an authoring error: neither is "the" content region,
// so the measure falls back to the body rather than guessing.
func TestMeasureContentAmbiguousMain(t *testing.T) {
	doc := parse(t, `<html><body><main>ONE</main><div role="main">TWO</div></body></html>`)
	m := dom.MeasureContent(doc)
	if m.Main != nil {
		t.Errorf("Main = %v, want nil for two main landmarks", m.Main)
	}
	if m.Region != len("ONETWO") {
		t.Errorf("Region = %d, want %d", m.Region, len("ONETWO"))
	}
}

// A hidden <main> (a closed dialog's, a template's) is not the content region.
func TestMeasureContentHiddenMainIgnored(t *testing.T) {
	doc := parse(t, `<html><body><main hidden>GONE</main><main>REAL</main></body></html>`)
	m := dom.MeasureContent(doc)
	if m.Main == nil || m.Region != len("REAL") {
		t.Errorf("Main=%v Region=%d, want the visible main with %d bytes", m.Main, m.Region, len("REAL"))
	}
}

func TestMeasureContentIgnoresWhitespace(t *testing.T) {
	a := dom.MeasureContent(parse(t, `<html><body><p>one two</p></body></html>`))
	b := dom.MeasureContent(parse(t, "<html><body>\n\t<p>\n   one\n\n   two   </p>\n\n</body></html>"))
	if a.Region != b.Region || a.Region != len("onetwo") {
		t.Errorf("Region %d vs %d, want both %d", a.Region, b.Region, len("onetwo"))
	}
}

func TestContentText(t *testing.T) {
	doc := parse(t, `<html><body><div id="art"><header>HEAD</header><p>Para <a href="#">LNK</a></p><nav>SKIP</nav></div></body></html>`)
	text, links, all := dom.ContentText(doc)
	if want := len("HEAD") + len("Para") + len("LNK"); text != want || links != len("LNK") || all != want+len("SKIP") {
		t.Errorf("ContentText = %d, %d, %d; want %d, %d, %d", text, links, all, want, len("LNK"), want+len("SKIP"))
	}
	if text, links, all := dom.ContentText(nil); text != 0 || links != 0 || all != 0 {
		t.Errorf("ContentText(nil) = %d, %d, %d", text, links, all)
	}
}

func TestMeasureContentReadOnly(t *testing.T) {
	src := `<html><head></head><body><main><p>keep <a href="/a">me</a></p></main><footer>f</footer></body></html>`
	doc := parse(t, src)
	before, _ := dom.Render(doc)
	dom.MeasureContent(doc)
	after, _ := dom.Render(doc)
	if before != after || !strings.Contains(after, "keep") {
		t.Errorf("MeasureContent mutated the document:\n%s\n%s", before, after)
	}
}

// MeasureDocument counts what the document holds, hidden or not: a script that
// hides a tab panel has not removed its text.
func TestMeasureDocumentCountsHidden(t *testing.T) {
	doc := parse(t, `<html><body><main><p>SHOWN</p><div hidden>TUCKED</div><div style="display:none">AWAY</div></main><nav>NAV</nav></body></html>`)
	if got := dom.MeasureContent(doc).Region; got != len("SHOWN") {
		t.Errorf("MeasureContent.Region = %d, want %d", got, len("SHOWN"))
	}
	m := dom.MeasureDocument(doc)
	if want := len("SHOWN") + len("TUCKED") + len("AWAY"); m.Region != want {
		t.Errorf("MeasureDocument.Region = %d, want %d", m.Region, want)
	}
	if want := len("SHOWN") + len("TUCKED") + len("AWAY") + len("NAV"); m.Page != want {
		t.Errorf("MeasureDocument.Page = %d, want %d", m.Page, want)
	}
}
