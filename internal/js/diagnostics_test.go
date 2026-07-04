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

func TestDiagnosticsCapturesConsole(t *testing.T) {
	diag := renderDiag(t, `<html><body><script>
		console.log('hello', 42);
		console.warn('careful');
		console.error('boom');
		console.info({a:1});
	</script></body></html>`)
	byLevel := map[string]string{}
	for _, m := range diag.Console {
		byLevel[m.Level] = m.Text
	}
	if byLevel["log"] != "hello 42" {
		t.Errorf("console.log = %q, want 'hello 42'", byLevel["log"])
	}
	if byLevel["warn"] != "careful" {
		t.Errorf("console.warn = %q", byLevel["warn"])
	}
	if byLevel["error"] != "boom" {
		t.Errorf("console.error = %q", byLevel["error"])
	}
	if byLevel["info"] != `{"a":1}` {
		t.Errorf("console.info(object) = %q, want JSON", byLevel["info"])
	}
	// console.error still also feeds the error diagnostics.
	if !strings.Contains(strings.Join(diag.Errors, "\n"), "boom") {
		t.Errorf("console.error should also feed Errors, got %v", diag.Errors)
	}
}

func TestDiagnosticsCapturesRequests(t *testing.T) {
	_, diag := renderApp(t, `<html><body><script>
		fetch('/api/data.json');
	</script></body></html>`, map[string]string{"/api/data.json": `{"ok":true}`})
	var found bool
	for _, r := range diag.Requests {
		if strings.Contains(r.URL, "/api/data.json") && r.Method == "GET" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /api/data.json GET in requests, got %+v", diag.Requests)
	}
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
