package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
)

// fixture is one page every engine processes, for an apples-to-apples corpus.
type fixture struct {
	name     string
	path     string // URL path
	html     string
	sentinel string // body-content substring every counted read output must contain
	title    string // page title, the bar an orientation output must clear
	// endSentinel is document-tail content a counted read-full output must
	// also contain — read-full promises the whole document, and a tool that
	// hard-truncates would otherwise score its cap as token efficiency
	// (Obscura's browser_snapshot cuts at ~4 KB with the top-of-page sentinel
	// intact). Empty = no tail check.
	endSentinel string
	// tokenOnly fixtures feed only the token pass — the footprint/latency
	// passes keep the original 4-fixture corpus so published numbers stay
	// comparable across runs.
	tokenOnly bool
}

// renderFixtures is the original 4-fixture corpus for the footprint and
// latency passes: real framework SPAs (bundles from testdata/frameworks,
// served locally) plus a static article.
func renderFixtures() []fixture {
	return []fixture{
		{name: "static-article", path: "/static", html: staticArticle, sentinel: "spins up a rendering pipeline", title: "Static Article"},
		{name: "react", path: "/react", html: reactApp, sentinel: "React Rendered", title: "React"},
		{name: "vue", path: "/vue", html: vueApp, sentinel: "Vue Rendered", title: "Vue"},
		{name: "lit", path: "/lit", html: litApp, sentinel: "Lit Rendered", title: "Lit"},
	}
}

// allFixtures is the full corpus: the render fixtures plus the token-pass
// pages (nav-heavy portals and a long article, loaded from eval/corpus or
// generated). root is the repo root.
func allFixtures(root string) ([]fixture, error) {
	fx := renderFixtures()
	tok, err := tokenFixtures(root)
	if err != nil {
		return nil, err
	}
	return append(fx, tok...), nil
}

// serveFixtures starts an httptest server that serves each fixture page and the
// vendored framework bundles from testdata/frameworks. Returns the base URL.
func serveFixtures(root string, fx []fixture) (*httptest.Server, error) {
	fxDir := filepath.Join(root, "testdata", "frameworks")
	mux := http.NewServeMux()
	for _, f := range fx {
		html := f.html
		mux.HandleFunc(f.path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, html)
		})
	}
	mux.HandleFunc("/fw/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		b, err := os.ReadFile(filepath.Join(fxDir, name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(b)
	})
	// 404 the site-hint probes so they don't count as work.
	mux.HandleFunc("/robots.txt", http.NotFound)
	mux.HandleFunc("/llms.txt", http.NotFound)
	mux.HandleFunc("/llms-full.txt", http.NotFound)
	return httptest.NewServer(mux), nil
}

const staticArticle = `<!doctype html><html><head><title>Static Article</title></head><body>
<article><h1>A Static Article</h1>
<p>This is a plain server-rendered article with several paragraphs of prose so the
reducer keeps it as real content. No JavaScript runs here — it is the cheapest case
and the floor for both engines.</p>
<p>Both unblink and a headless browser must fetch and parse it; only one of them
spins up a rendering pipeline, a compositor, and a GPU process to do so.</p>
</article></body></html>`

const reactApp = `<!doctype html><html><head><title>React</title></head><body><div id="root">loading</div>
<script src="/fw/react.production.min.js"></script>
<script src="/fw/react-dom.production.min.js"></script>
<script>
  var e = React.createElement;
  function App() { return e('article', null,
    e('h1', null, 'React Rendered'),
    e('p', null, 'A real React bundle rendered this article, with enough prose for the reducer to keep it as page content.')); }
  ReactDOM.createRoot(document.getElementById('root')).render(e(App));
</script></body></html>`

const vueApp = `<!doctype html><html><head><title>Vue</title></head><body><div id="app">loading</div>
<script src="/fw/vue.global.prod.js"></script>
<script>
  Vue.createApp({ template: '<article><h1>{{ t }}</h1><p>{{ p }}</p></article>',
    data() { return { t: 'Vue Rendered', p: 'A real Vue bundle rendered this article, with enough prose for the reducer to keep it as page content.' }; } }).mount('#app');
</script></body></html>`

const litApp = `<!doctype html><html><head><title>Lit</title></head><body><my-article></my-article>
<script type="module">
  import { LitElement, html } from '/fw/lit-all.min.js';
  class MyArticle extends LitElement {
    render() { return html` + "`<article><h1>Lit Rendered</h1><p>A real Lit component rendered this article, with enough prose for the reducer to keep it as page content.</p></article>`" + `; }
  }
  customElements.define('my-article', MyArticle);
</script></body></html>`
