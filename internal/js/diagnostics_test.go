package js_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// renderDiag runs scripts over pageHTML and returns the engine's diagnostics.
func renderDiag(t *testing.T, pageHTML string) js.RenderResult {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/")
	eng := js.New(js.WithTimeout(2 * time.Second))
	var diag js.RenderResult
	if err := eng.Render(context.Background(), doc, base, js.Env{Diag: &diag}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return diag
}

func TestDiagnosticsDetectsReactAndCapturesError(t *testing.T) {
	diag := renderDiag(t, `<html><body><div data-reactroot></div>
		<script>window.React = {}; throw new Error('boom-from-script');</script>
		</body></html>`)
	if diag.Framework != "react" {
		t.Errorf("framework = %q, want react", diag.Framework)
	}
	if len(diag.Errors) == 0 || !strings.Contains(strings.Join(diag.Errors, "\n"), "boom-from-script") {
		t.Errorf("expected captured script error, got %v", diag.Errors)
	}
}

func TestDiagnosticsDetectsWebComponents(t *testing.T) {
	diag := renderDiag(t, `<html><body><x-thing></x-thing>
		<script>
		  class XThing extends HTMLElement { connectedCallback() { this.textContent = 'x'; } }
		  customElements.define('x-thing', XThing);
		</script></body></html>`)
	if diag.Framework != "webcomponents" {
		t.Errorf("framework = %q, want webcomponents", diag.Framework)
	}
	if diag.Upgrades < 1 {
		t.Errorf("upgrades = %d, want >= 1", diag.Upgrades)
	}
}
