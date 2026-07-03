package js

import (
	"net/url"
	"testing"
)

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

// Bundle keys ignore the base URL's query and fragment (relative specifiers
// resolve against scheme/host/path only) but stay sensitive to the path.
func TestBundleKeyIgnoresBaseQueryAndFragment(t *testing.T) {
	b1, _ := url.Parse("https://example.com/page?bust=1#frag")
	b2, _ := url.Parse("https://example.com/page?bust=2")
	b3, _ := url.Parse("https://example.com/other")
	if bundleKey(b1, "entry", nil) != bundleKey(b2, "entry", nil) {
		t.Error("base query/fragment split the bundle key — query-busted pages rebuild the module graph")
	}
	if bundleKey(b1, "entry", nil) == bundleKey(b3, "entry", nil) {
		t.Error("base path must affect the bundle key — relative imports resolve against it")
	}
}
