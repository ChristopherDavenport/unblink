package js

import (
	"os"
	"strings"
	"testing"
)

// esbuild's bundler (api.Build) resolves non-literal relative imports as globs
// against the real filesystem, outside any plugin's control, so page-supplied code
// must only ever go through api.Transform (see runModules, ADR 0018). This is a
// tripwire for reintroducing the bundler in this package; the behavioural proof is
// TestModuleGlobImportCannotReadLocalFS.
func TestNoEsbuildBundlerOnPageCode(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"esbuild.Build(", "esbuild.BuildOptions", "esbuild.Context("} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s uses %s: esbuild's bundler reads the filesystem for glob imports; use esbuild.Transform", name, banned)
			}
		}
	}
}
