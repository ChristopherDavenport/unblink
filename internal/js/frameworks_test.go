package js_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// These run REAL, pinned framework bundles (testdata/frameworks) through the
// engine and assert the app's content lands in the serialized tree — the
// end-to-end proof that unblink renders mainstream SPAs, not hand-written stand-ins.

func loadBundle(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "frameworks", name))
	if err != nil {
		t.Fatalf("read bundle %s: %v", name, err)
	}
	return string(b)
}

// renderApp renders pageHTML with bundles served by URL substring, and returns the
// serialized DOM plus diagnostics (so a failed framework reports its error).
func renderApp(t *testing.T, pageHTML string, routes map[string]string) (string, js.RenderResult) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/app")
	eng := js.New(js.WithTimeout(5 * time.Second))
	var diag js.RenderResult
	tr := &stubTransport{routes: routes}
	if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr, Diag: &diag}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.String(), diag
}

func assertContains(t *testing.T, out string, diag js.RenderResult, markers ...string) {
	t.Helper()
	for _, m := range markers {
		if !strings.Contains(out, m) {
			t.Errorf("missing marker %q\nframework=%q errors=%v\n--- output ---\n%s",
				m, diag.Framework, diag.Errors, out)
			return
		}
	}
}

func TestPreactRenders(t *testing.T) {
	out, diag := renderApp(t, `<html><body><div id="root"></div>
		<script src="/fw/preact.umd.js"></script>
		<script>
		  var h = preact.h;
		  function App() {
		    return h('div', { class: 'app' },
		      h('h1', null, 'preact-marker'),
		      h('ul', null, [h('li', null, 'p-1'), h('li', null, 'p-2'), h('li', null, 'p-3')]));
		  }
		  preact.render(h(App), document.getElementById('root'));
		</script></body></html>`,
		map[string]string{"preact.umd.js": loadBundle(t, "preact.umd.js")})
	assertContains(t, out, diag, "preact-marker", "p-1", "p-2", "p-3")
}

func TestReactRenders(t *testing.T) {
	out, diag := renderApp(t, `<html><body><div id="root"></div>
		<script src="/fw/react.production.min.js"></script>
		<script src="/fw/react-dom.production.min.js"></script>
		<script>
		  var e = React.createElement;
		  function App() {
		    return e('div', { className: 'app' },
		      e('h1', null, 'react-marker'),
		      e('p', null, 'count:' + (1 + 2)));
		  }
		  var root = ReactDOM.createRoot(document.getElementById('root'));
		  root.render(e(App));
		</script></body></html>`,
		map[string]string{
			"react.production.min.js":     loadBundle(t, "react.production.min.js"),
			"react-dom.production.min.js": loadBundle(t, "react-dom.production.min.js"),
		})
	assertContains(t, out, diag, "react-marker", "count:3")
}

func TestVueRenders(t *testing.T) {
	out, diag := renderApp(t, `<html><body><div id="app"></div>
		<script src="/fw/vue.global.prod.js"></script>
		<script>
		  const { createApp } = Vue;
		  createApp({
		    data() { return { msg: 'vue-marker', items: ['v-1', 'v-2', 'v-3'] }; },
		    template: '<div class="app"><h1>{{ msg }}</h1><ul><li v-for="i in items">{{ i }}</li></ul></div>'
		  }).mount('#app');
		</script></body></html>`,
		map[string]string{"vue.global.prod.js": loadBundle(t, "vue.global.prod.js")})
	assertContains(t, out, diag, "vue-marker", "v-1", "v-2", "v-3")
}

func TestSvelteRenders(t *testing.T) {
	// A real (precompiled) Svelte bundle mounting an <article> into #app. The
	// compiled runtime uses conventional createElement/appendChild/setData DOM
	// manipulation, exercising the classic-script path end to end.
	out, diag := renderApp(t, `<!doctype html><html><body><div id="app">loading</div>
		<script src="/fw/svelte-app.iife.js"></script></body></html>`,
		map[string]string{"svelte-app.iife.js": loadBundle(t, "svelte-app.iife.js")})
	assertContains(t, out, diag, "Svelte Rendered Title")
}

func TestLitRenders(t *testing.T) {
	bt := "`" // JS template-literal backtick, kept out of the Go raw strings below
	page := `<html><body><my-counter></my-counter>
		<script type="module">
		  import { LitElement, html } from '/fw/lit-all.min.js';
		  class MyCounter extends LitElement {
		    static properties = { n: { type: Number } };
		    constructor() { super(); this.n = 3; }
		    render() { return html` + bt + `<p class="c">lit-marker count:${this.n}</p>` + bt + `; }
		  }
		  customElements.define('my-counter', MyCounter);
		</script></body></html>`
	out, diag := renderApp(t, page,
		map[string]string{"lit-all.min.js": loadBundle(t, "lit-all.min.js")})
	// lit-html renders the dynamic ${this.n} as a separate text node behind a comment
	// marker, so the raw serialization is "count:<!--marker-->3" (the comment is
	// stripped by reduce/emit). Assert the static text and the dynamic value separately.
	assertContains(t, out, diag, "lit-marker count:", "3</p>")
}
