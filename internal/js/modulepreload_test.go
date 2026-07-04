package js_test

import (
	"strings"
	"testing"
)

// TestModulePreloadWarmsDynamicImport covers the modulepreload warming path: a page
// declares its code-split chunks via <link rel=modulepreload> and then lazy-imports
// them at runtime. All chunks must render, and each must be fetched exactly once —
// the concurrent warm and the serial import() share a single fetch (via the
// prefetched() join in the module OnLoad), never a duplicate. renderWith uses an
// engine with no asset cache, so this exercises the per-render prefetch map directly.
func TestModulePreloadWarmsDynamicImport(t *testing.T) {
	tr := &urlCountTransport{routes: map[string]string{
		"/chunk-a.mjs": "export default 'A';",
		"/chunk-b.mjs": "export default 'B';",
		"/chunk-c.mjs": "export default 'C';",
	}}
	out := renderWith(t, `<html><head>
		<link rel="modulepreload" href="/chunk-a.mjs">
		<link rel="modulepreload" href="/chunk-b.mjs">
		<link rel="modulepreload" href="/chunk-c.mjs">
		</head><body><div id="out">before</div>
		<script>
		  Promise.all([
		    import('/chunk-a.mjs'),
		    import('/chunk-b.mjs'),
		    import('/chunk-c.mjs')
		  ]).then(function (m) {
		    document.getElementById("out").textContent =
		      'cache-marker:' + m[0].default + m[1].default + m[2].default;
		  });
		</script></body></html>`, tr)

	if !strings.Contains(out, "cache-marker:ABC") {
		t.Errorf("lazy-imported chunks did not all render:\n%s", out)
	}
	for _, c := range []string{"/chunk-a.mjs", "/chunk-b.mjs", "/chunk-c.mjs"} {
		if got := tr.count(c); got != 1 {
			t.Errorf("%s fetched %d times, want 1 (warmed once, no duplicate import fetch)", c, got)
		}
	}
}

// TestDynamicImportWithoutPreloadStillLoads is the fallback: chunks with no
// modulepreload hint still load correctly through the serial import() path (just
// without the concurrency benefit), each fetched exactly once.
func TestDynamicImportWithoutPreloadStillLoads(t *testing.T) {
	tr := &urlCountTransport{routes: map[string]string{
		"/lazy.mjs": "export default 'LAZY';",
	}}
	out := renderWith(t, `<html><body><div id="out">before</div>
		<script>
		  import('/lazy.mjs').then(function (m) {
		    document.getElementById("out").textContent = 'cache-marker:' + m.default;
		  });
		</script></body></html>`, tr)

	if !strings.Contains(out, "cache-marker:LAZY") {
		t.Errorf("un-preloaded lazy chunk did not render:\n%s", out)
	}
	if got := tr.count("/lazy.mjs"); got != 1 {
		t.Errorf("/lazy.mjs fetched %d times, want 1", got)
	}
}
