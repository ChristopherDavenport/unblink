package js_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// Module-loading benchmarks: a real ESM framework build (Lit, one large module)
// and a synthetic code-split graph (many small modules), each with the asset
// cache on (the production default: repeat renders reuse fetched sources and their
// transforms) and off (every render pays the full fetch + transform).

func benchModules(b *testing.B, page string, routes map[string]string, cache bool) {
	b.Helper()
	opts := []js.Option{js.WithTimeout(10 * time.Second)}
	if cache {
		opts = append(opts, js.WithAssetCache(time.Minute))
	}
	eng := js.New(opts...)
	defer eng.Close()
	tr := &exactTransport{routes: routes}
	base, _ := url.Parse("https://example.com/page")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := html.Parse(strings.NewReader(page))
		if err != nil {
			b.Fatalf("parse: %v", err)
		}
		tr.mu.Lock()
		tr.fetched = tr.fetched[:0]
		tr.mu.Unlock()
		if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
			b.Fatalf("render: %v", err)
		}
	}
}

func litModulePage() string {
	bt := "`"
	return `<html><body><my-counter></my-counter>
		<script type="module">
		  import { LitElement, html } from '/fw/lit-all.min.js';
		  class MyCounter extends LitElement {
		    static properties = { n: { type: Number } };
		    constructor() { super(); this.n = 3; }
		    render() { return html` + bt + `<p class="c">lit-marker count:${this.n}</p>` + bt + `; }
		  }
		  customElements.define('my-counter', MyCounter);
		</script></body></html>`
}

func BenchmarkRenderLitModuleCached(b *testing.B) {
	benchModules(b, litModulePage(), map[string]string{origin + "/fw/lit-all.min.js": loadBundleBench(b, "lit-all.min.js")}, true)
}

func BenchmarkRenderLitModuleUncached(b *testing.B) {
	benchModules(b, litModulePage(), map[string]string{origin + "/fw/lit-all.min.js": loadBundleBench(b, "lit-all.min.js")}, false)
}

// moduleGraph is a code-split shape: an entry importing `width` chunks, each
// importing one shared module and a leaf of its own.
func moduleGraph(width int) (string, map[string]string) {
	routes := map[string]string{origin + "/g/shared.js": `export let hits = 0; export function hit() { return ++hits; }`}
	var entry strings.Builder
	for i := 0; i < width; i++ {
		fmt.Fprintf(&entry, "import { v%d } from './c%d.js';\n", i, i)
		routes[fmt.Sprintf("%s/g/c%d.js", origin, i)] = fmt.Sprintf(
			"import { hit } from './shared.js'; import { leaf } from './l%d.js'; export const v%d = hit() + leaf;", i, i)
		routes[fmt.Sprintf("%s/g/l%d.js", origin, i)] = fmt.Sprintf(
			"export const leaf = %d; export class K%d { constructor() { this.x = %d; } get y() { return this.x ?? 0; } }", i, i, i)
	}
	fmt.Fprintf(&entry, "document.getElementById('out').textContent = 'graph ' + v%d;\n", width-1)
	routes[origin+"/g/entry.js"] = entry.String()
	return `<html><body><div id="out"></div><script type="module" src="/g/entry.js"></script></body></html>`, routes
}

func BenchmarkRenderModuleGraphCached(b *testing.B) {
	page, routes := moduleGraph(100)
	benchModules(b, page, routes, true)
}

func BenchmarkRenderModuleGraphUncached(b *testing.B) {
	page, routes := moduleGraph(100)
	benchModules(b, page, routes, false)
}
