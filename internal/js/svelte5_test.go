package js_test

import (
	"strings"
	"testing"
)

// TestSvelte5DegradesGracefully guards the graceful-degradation path with the real
// trigger. Svelte 5's runtime instantiates a private-field-heavy Boundary class
// that trips a goja VM bug (definePrivateProp asserts the field-init frame is a
// classFuncObject; for this class it is a plain object → Go panic). Before the
// bridge.runProgram recover, that panic skipped the rest of the pipeline and left a
// SILENT blank render. Now it must instead: not crash or hang the engine, complete
// the render, and surface a js_error. (If a future goja bump fixes the bug, the
// bundle will simply render its title — the test accepts either, but never a crash.)
func TestSvelte5DegradesGracefully(t *testing.T) {
	bundle := loadBundle(t, "svelte5-app.iife.js")
	out, diag := renderApp(t, `<!doctype html><html><body><div id="app">loading</div>
		<script src="/fw/svelte5-app.iife.js"></script></body></html>`,
		map[string]string{"svelte5-app.iife.js": bundle})

	rendered := strings.Contains(out, "Svelte Rendered Title")
	degraded := false
	for _, e := range diag.Errors {
		if strings.Contains(e, "goja limitation") {
			degraded = true
		}
	}
	// The engine must have produced a serialized document either way (no crash/hang).
	if !strings.Contains(out, `id="app"`) {
		t.Fatalf("engine produced no document")
	}
	if !rendered && !degraded {
		t.Fatalf("svelte5 neither rendered nor degraded with a recorded goja error; errors=%v", diag.Errors)
	}
	t.Logf("svelte5: rendered=%v degraded=%v errors=%d", rendered, degraded, len(diag.Errors))
}
