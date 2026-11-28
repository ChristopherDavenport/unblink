package js

import "testing"

// The program cache is content-only within a compile pathway: the same bytes
// delivered under a different name (URL, position, query-busted variant) must
// return the identical cached Program, not recompile.
func TestClassicProgramContentKeyed(t *testing.T) {
	src := "window.__unblinkContentKeyProbe = (window.__unblinkContentKeyProbe || 0) + 1;"
	p1, err1 := classicProgram("https://a.example/app.js", src)
	p2, err2 := classicProgram("https://b.example/app.js?v=2", src)
	if err1 != nil || err2 != nil {
		t.Fatalf("compile: %v / %v", err1, err2)
	}
	if p1 != p2 {
		t.Fatal("identical source under different names compiled to distinct Programs — cache key is not content-only")
	}
}

// The first-seen name is embedded in the shared Program; a later hit under a
// different name reuses it (diagnostic-only tradeoff, recorded in the cache's
// package comment).
func TestClassicProgramCachesCompileErrors(t *testing.T) {
	src := "this is not javascript ]["
	_, err1 := classicProgram("https://a.example/broken.js", src)
	_, err2 := classicProgram("https://b.example/broken.js", src)
	if err1 == nil || err2 == nil {
		t.Fatal("expected compile errors")
	}
	if err1.Error() != err2.Error() {
		t.Fatalf("cached error mismatch:\n%v\n%v", err1, err2)
	}
}
