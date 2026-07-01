package dom_test

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/dom"
)

// parseDoc parses a full HTML string into a document node (no Extract pass),
// shared by the render/structured/tables tests.
func parseDoc(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader([]byte(s)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

const rawDocFixture = `<!doctype html><html><head>
<script>console.log('should be stripped');</script>
</head><body>
<h1>Hello</h1>
<form id="signup"><input name="email"></form>
</body></html>`

func TestRenderWholeDoc(t *testing.T) {
	out, err := dom.Render(parseDoc(t, rawDocFixture))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"<script>", "console.log", "<form", "<h1>Hello</h1>"} {
		if !strings.Contains(out, want) {
			t.Errorf("render output missing %q\n%s", want, out)
		}
	}
}

func TestRenderSelector(t *testing.T) {
	out, n, err := dom.RenderSelector(parseDoc(t, rawDocFixture), "script")
	if err != nil {
		t.Fatalf("render selector: %v", err)
	}
	if n != 1 {
		t.Fatalf("matched = %d, want 1", n)
	}
	if !strings.Contains(out, "console.log('should be stripped')") {
		t.Errorf("selector output missing script text: %q", out)
	}
	if strings.Contains(out, "<form") {
		t.Errorf("selector output should not contain the form: %q", out)
	}
}

func TestRenderSelectorNoMatch(t *testing.T) {
	out, n, err := dom.RenderSelector(parseDoc(t, rawDocFixture), ".does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 || out != "" {
		t.Errorf("want empty/0, got %q / %d", out, n)
	}
}

func TestRenderSelectorInvalid(t *testing.T) {
	if _, _, err := dom.RenderSelector(parseDoc(t, rawDocFixture), "!!!bad"); err == nil {
		t.Error("expected error for invalid selector")
	}
}

func TestText(t *testing.T) {
	doc := parseDoc(t, `<!doctype html><html><head>
<style>h1{color:red}</style>
<script>var secret=1</script>
</head><body>
<h1>Title Here</h1>
<p>First paragraph.</p>
<p>Second   paragraph with  spaces.</p>
</body></html>`)
	out := dom.Text(doc, false)
	for _, want := range []string{"Title Here", "First paragraph.", "Second paragraph with spaces."} {
		if !strings.Contains(out, want) {
			t.Errorf("text missing %q\n%q", want, out)
		}
	}
	for _, bad := range []string{"secret", "color:red", "<h1>", "var secret"} {
		if strings.Contains(out, bad) {
			t.Errorf("text should not contain %q\n%q", bad, out)
		}
	}
	// block boundaries produce a blank line between the two paragraphs
	if !strings.Contains(out, "First paragraph.\n\nSecond paragraph") {
		t.Errorf("expected paragraph break between paragraphs:\n%q", out)
	}
}
