package js_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// End-to-end render benchmarks. Provably-idle pages settle in ~1-3ms (ADR
// 0004); a page with anything armed pays the ~60ms quiet window after its last
// activity. The in-package benchmarks in internal_bench_test.go isolate the
// per-render setup costs.

func loadBundleBench(b *testing.B, name string) string {
	b.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "frameworks", name))
	if err != nil {
		b.Fatalf("read bundle %s: %v", name, err)
	}
	return string(data)
}

// benchRender renders pageHTML once per iteration on a fresh doc (Render
// mutates the tree, so docs are single-use — parse cost is negligible next to
// a render).
func benchRender(b *testing.B, pageHTML string, tr js.Transport) {
	b.Helper()
	base, err := url.Parse("https://example.com/app")
	if err != nil {
		b.Fatalf("base: %v", err)
	}
	eng := js.New(js.WithTimeout(5 * time.Second))
	defer eng.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := html.Parse(strings.NewReader(pageHTML))
		if err != nil {
			b.Fatalf("parse: %v", err)
		}
		if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
			b.Fatalf("render: %v", err)
		}
	}
}

// BenchmarkRenderMinimal is the fixed per-render floor: loop + bridge +
// prelude + one trivial script + settle.
func BenchmarkRenderMinimal(b *testing.B) {
	benchRender(b, `<html><body><div id="out"></div>
		<script>document.getElementById('out').textContent = 'done';</script>
		</body></html>`, nil)
}

// BenchmarkRenderReact runs the real pinned react + react-dom production
// bundles (the compile-cost showcase: react-dom alone is ~131KB of source).
func BenchmarkRenderReact(b *testing.B) {
	routes := map[string]string{
		"react.production.min.js":     loadBundleBench(b, "react.production.min.js"),
		"react-dom.production.min.js": loadBundleBench(b, "react-dom.production.min.js"),
	}
	benchRender(b, `<html><body><div id="root"></div>
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
		</script></body></html>`, &stubTransport{routes: routes})
}

func BenchmarkRenderVue(b *testing.B) {
	routes := map[string]string{"vue.global.prod.js": loadBundleBench(b, "vue.global.prod.js")}
	benchRender(b, `<html><body><div id="app"></div>
		<script src="/fw/vue.global.prod.js"></script>
		<script>
		  const { createApp } = Vue;
		  createApp({
		    data() { return { msg: 'vue-marker', items: ['v-1', 'v-2', 'v-3'] }; },
		    template: '<div class="app"><h1>{{ msg }}</h1><ul><li v-for="i in items">{{ i }}</li></ul></div>'
		  }).mount('#app');
		</script></body></html>`, &stubTransport{routes: routes})
}

func BenchmarkRenderPreact(b *testing.B) {
	routes := map[string]string{"preact.umd.js": loadBundleBench(b, "preact.umd.js")}
	benchRender(b, `<html><body><div id="root"></div>
		<script src="/fw/preact.umd.js"></script>
		<script>
		  var h = preact.h;
		  function App() {
		    return h('div', { class: 'app' },
		      h('h1', null, 'preact-marker'),
		      h('ul', null, [h('li', null, 'p-1'), h('li', null, 'p-2'), h('li', null, 'p-3')]));
		  }
		  preact.render(h(App), document.getElementById('root'));
		</script></body></html>`, &stubTransport{routes: routes})
}

// httpBenchTransport is a js.Transport over a real net/http client, so
// external-script fetches pay genuine TCP round-trips against the loopback
// fixture server.
type httpBenchTransport struct{ c *http.Client }

func (h *httpBenchTransport) Do(ctx context.Context, method, urlStr string, headers map[string]string, body []byte) (*js.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	hdrs := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		hdrs[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return &js.Response{Status: resp.StatusCode, Headers: hdrs, Body: data, FinalURL: resp.Request.URL.String()}, nil
}

// BenchmarkRenderExternalScript fetches the page's bundle over real HTTP with a
// fresh client per render — mirroring production, where every render builds its
// own transport — so script-byte caching and connection reuse both show here.
func BenchmarkRenderExternalScript(b *testing.B) {
	bundle := []byte(loadBundleBench(b, "preact.umd.js"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(bundle)
	}))
	defer srv.Close()

	pageHTML := `<html><body><div id="root"></div>
		<script src="` + srv.URL + `/preact.umd.js"></script>
		<script>
		  preact.render(preact.h('h1', null, 'ext-marker'), document.getElementById('root'));
		</script></body></html>`
	base, _ := url.Parse(srv.URL + "/app")
	eng := js.New(js.WithTimeout(5 * time.Second))
	defer eng.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := html.Parse(strings.NewReader(pageHTML))
		if err != nil {
			b.Fatalf("parse: %v", err)
		}
		rt := &http.Transport{}
		tr := &httpBenchTransport{c: &http.Client{Transport: rt}}
		if err := eng.Render(context.Background(), doc, base, js.Env{Transport: tr}); err != nil {
			b.Fatalf("render: %v", err)
		}
		rt.CloseIdleConnections()
	}
}

// BenchmarkDispatchQuerySelector hammers querySelectorAll/querySelector from
// page JS — the per-call cascadia.Compile cost on the bridge's query paths.
func BenchmarkDispatchQuerySelector(b *testing.B) {
	var items strings.Builder
	for i := 0; i < 200; i++ {
		items.WriteString(`<div class="item"><a href="#">x</a></div>`)
	}
	benchRender(b, `<html><body><div id="out"></div><div id="list">`+items.String()+`</div>
		<script>
		  var n = 0;
		  for (var i = 0; i < 300; i++) {
		    n += document.querySelectorAll('.item a').length;
		    if (document.querySelector('#list')) { n++; }
		  }
		  document.getElementById('out').textContent = 'n=' + n;
		</script></body></html>`, nil)
}
