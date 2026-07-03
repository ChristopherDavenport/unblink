package js_test

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

const moduleDepPage = `<html><body><div id="out"></div>
	<script type="module">
	import { msg } from './dep.js';
	document.getElementById('out').textContent = msg;
	</script></body></html>`

// Module pages must render identically when the page URL carries a varying
// query (?utm=, cache busting): the bundle cache is keyed on the resolution
// base with query/fragment cleared, so the rebuilt-per-query pathology stays
// gone. Correctness net for TestBundleKeyIgnoresBaseQueryAndFragment.
func TestModuleRenderStableAcrossBustedBase(t *testing.T) {
	tr := newRoutedTransport(map[string]routedScript{
		"https://example.com/dep.js": {body: "export const msg = 'dep-ran';"},
	})
	eng := js.New(js.WithTimeout(5*time.Second), js.WithAssetCache(time.Minute))
	defer eng.Close()
	for i := 1; i <= 2; i++ {
		doc, err := html.Parse(strings.NewReader(moduleDepPage))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		base, _ := url.Parse(fmt.Sprintf("https://example.com/page?bust=%d", i))
		if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		var buf bytes.Buffer
		if err := html.Render(&buf, doc); err != nil {
			t.Fatalf("serialize: %v", err)
		}
		if !strings.Contains(buf.String(), "dep-ran") {
			t.Fatalf("render %d missing module content:\n%s", i, buf.String())
		}
	}
}

// BenchmarkRenderModuleBustedBase renders a module page under a unique page
// query per iteration — the crossbench methodology and the `?utm=` reality.
// Before the bundle-key canonicalization every iteration re-ran the whole
// esbuild build; now iteration 2+ hits the cached bundle program.
func BenchmarkRenderModuleBustedBase(b *testing.B) {
	tr := newRoutedTransport(map[string]routedScript{
		"https://example.com/dep.js": {body: "export const msg = 'dep-ran';"},
	})
	eng := js.New(js.WithTimeout(5*time.Second), js.WithAssetCache(time.Minute))
	defer eng.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := html.Parse(strings.NewReader(moduleDepPage))
		if err != nil {
			b.Fatalf("parse: %v", err)
		}
		base, _ := url.Parse(fmt.Sprintf("https://example.com/page?bust=%d", i))
		if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
			b.Fatalf("render: %v", err)
		}
	}
}
