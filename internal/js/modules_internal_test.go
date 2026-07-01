package js

import (
	"strings"
	"testing"
)

// TestLowerDynamicImportRewritesToLoader verifies the fallback lowers dynamic import()
// into a goja-parseable form AND redirects it to the __unblinkImportSync loader (not the
// disabled global require). This pins esbuild's `__toESM(require(` lowering shape.
func TestLowerDynamicImportRewritesToLoader(t *testing.T) {
	out, ok := lowerDynamicImport(`var l = x; import(l); import('./s.js');`)
	if !ok {
		t.Fatal("lowerDynamicImport returned ok=false")
	}
	if strings.Contains(out, "import(") {
		t.Errorf("output still contains import(:\n%s", out)
	}
	if !strings.Contains(out, "Promise.resolve()") || !strings.Contains(out, dynImportGlobal+"(") {
		t.Errorf("output missing lowered loader call:\n%s", out)
	}
	// The disabled global require must not be the lowering target.
	if strings.Contains(out, "__toESM(require(") {
		t.Errorf("dynamic import still routed through require():\n%s", out)
	}
}

// TestLowerDynamicImportKeepsPrivateFields proves Target ESNext leaves #private fields
// native (not lowered to WeakMap accessors), independent of goja — a future esbuild
// bump that starts downleveling them fails here with a clear signal.
func TestLowerDynamicImportKeepsPrivateFields(t *testing.T) {
	out, ok := lowerDynamicImport(`class C { #n = 0; bump() { this.#n++; return this.#n; } } import(x);`)
	if !ok {
		t.Fatal("lowerDynamicImport returned ok=false")
	}
	if !strings.Contains(out, "#n") {
		t.Errorf("private field #n was lowered away (WeakMap risk):\n%s", out)
	}
}
