package dom_test

import (
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/dom"
)

func parse(t *testing.T, s string) *html.Node {
	t.Helper()
	n, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return n
}

func TestStripHiddenRemovesInjectionChannels(t *testing.T) {
	doc := parse(t, `<html><body>
		<p>Visible content.</p>
		<div style="display:none">INJECT-display-none</div>
		<div style="visibility: hidden">INJECT-visibility</div>
		<div hidden>INJECT-hidden-attr</div>
		<div aria-hidden="true">INJECT-aria</div>
		<span style="position:absolute;left:-9999px">INJECT-offscreen</span>
		<!-- INJECT-comment -->
		<p>More visible.</p>
	</body></html>`)
	dom.StripHidden(doc)
	out, err := dom.Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "Visible content.") || !strings.Contains(out, "More visible.") {
		t.Errorf("visible content was removed:\n%s", out)
	}
	for _, bad := range []string{"INJECT-display-none", "INJECT-visibility", "INJECT-hidden-attr",
		"INJECT-aria", "INJECT-offscreen", "INJECT-comment"} {
		if strings.Contains(out, bad) {
			t.Errorf("hidden channel %q survived StripHidden:\n%s", bad, out)
		}
	}
}

func TestTextSkipHidden(t *testing.T) {
	doc := parse(t, `<html><body>
		<p>Shown text.</p>
		<div style="display:none">INJECT-hidden</div>
	</body></html>`)
	if got := dom.Text(doc, false); !strings.Contains(got, "INJECT-hidden") {
		t.Errorf("skipHidden=false should keep hidden text, got %q", got)
	}
	got := dom.Text(doc, true)
	if strings.Contains(got, "INJECT-hidden") {
		t.Errorf("skipHidden=true should drop hidden text, got %q", got)
	}
	if !strings.Contains(got, "Shown text.") {
		t.Errorf("skipHidden=true dropped visible text, got %q", got)
	}
}
