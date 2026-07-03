//go:build eval

package eval

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
	"github.com/christopherdavenport/unblink/internal/search"
)

// --- corpus host builders ---

// htmlHost serves a single HTML fixture (read from path, relative to the eval
// package directory) on every non-metadata path, and 404s the origin-root
// metadata files so a single-page fixture's site-hint probes don't error.
func htmlHost(path string) Host {
	return func() (http.Handler, error) {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read fixture %s: %w", path, err)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(body)
		}), nil
	}
}

// frameworkHost serves an SPA shell (appHTML) on every page path plus the vendored
// framework bundles at their paths, 404ing the metadata files. Used to render real
// React/Vue/Preact/Lit apps end-to-end through the MCP server.
func frameworkHost(appHTML string, bundles map[string]string) Host {
	return func() (http.Handler, error) {
		loaded := map[string][]byte{}
		for path, file := range bundles {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("read bundle %s: %w", file, err)
			}
			loaded[path] = b
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			if b, ok := loaded[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "application/javascript")
				_, _ = w.Write(b)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, appHTML)
		}), nil
	}
}

// delayedContentHost serves a page whose real content arrives only after a
// server-side delay via an in-page fetch, so a case can exercise the render settle +
// wait_for path. The awaited node carries id="answer".
func delayedContentHost(delay time.Duration) Host {
	return func() (http.Handler, error) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(delay)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"msg":"delayed-answer-payload"}`)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body><div id="app">loading</div>
<script>
fetch('/api').then(function(r){ return r.json(); }).then(function(j){
  document.getElementById('app').innerHTML =
    '<article id="answer"><h1>' + j.msg + '</h1><p>The ' + j.msg +
    ' arrived after a delay, with enough prose for the reducer to keep the article body intact and present.</p></article>';
});
</script></body></html>`)
		})
		return mux, nil
	}
}

// starvedRenderHost serves a page whose script mutates the DOM forever on a
// tight setInterval, so a short render budget always closes mid-work — the
// starved-render diagnostics (render_budget_hit + dom_busy) must fire. Static
// content present before the loop still survives (best-effort render).
func starvedRenderHost() Host {
	return func() (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body>
<article><h1>Static Headline</h1><p>static-sentinel-content present before the busy loop, with enough prose to survive reduction and stay in the output.</p></article>
<div id="feed"></div>
<script>
var n = 0;
setInterval(function () {
  var d = document.createElement('p');
  d.textContent = 'appended row ' + (n++);
  document.getElementById('feed').appendChild(d);
}, 5);
</script></body></html>`)
		}), nil
	}
}

// budgetDeniedHost serves a page that fires more subrequests than the per-render
// request budget allows. With a low --js-max-requests the excess requests are
// budget-denied, so net_denied ≥ 1. Static content still survives.
func budgetDeniedHost() Host {
	return func() (http.Handler, error) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body>
<article><h1>Guarded</h1><p>guarded-page-sentinel body with sufficient prose to survive reduction and stay present.</p></article>
<script>
// Fire well past the request budget; the excess are budget-denied.
for (var i = 0; i < 12; i++) { fetch('/api?n=' + i).catch(function(){}); }
</script></body></html>`)
		})
		return mux, nil
	}
}

// keydownSearchHost serves a search box whose Enter keydown handler reads
// e.key and renders a results region — exercising interact event=keydown with
// a key and typed value end to end.
func keydownSearchHost() Host {
	return func() (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body>
<h1>Search</h1>
<input id="q" type="text">
<div id="results">placeholder</div>
<script>
document.getElementById('q').addEventListener('keydown', function (e) {
  if (e.key === 'Enter') {
    document.getElementById('results').textContent =
      'keydown-results-sentinel for "' + e.target.value + '": the Enter keydown carried its key field and rendered a results region with enough prose to survive reduction.';
  }
});
</script></body></html>`)
		}), nil
	}
}

// deferredTimerHost serves a page that injects its real content from a
// setTimeout well beyond the render budget — the timer clamp must pull it in so
// the content still materializes.
func deferredTimerHost() Host {
	return func() (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body><div id="app">loading</div>
<script>
setTimeout(function () {
  document.getElementById('app').innerHTML =
    '<article><h1>Deferred</h1><p>deferred-timer-sentinel content injected from a 7s timeout, clamped into the budget so it still appears, with enough prose to survive reduction.</p></article>';
}, 7000);
</script></body></html>`)
		}), nil
	}
}

// apiSmokeHost serves a page whose inline script exercises every Phase-21
// tier-1 API cluster and appends an api-N-ok marker for each that works — the
// MCP-level regression net for the whole browser-API surface.
func apiSmokeHost() Host {
	return func() (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body>
<article><h1>API Smoke</h1><div id="out"></div></article>
<script>
var ok = [];
function mark(n, cond) { if (cond) ok.push('api-' + n + '-ok'); }
mark(1, atob(btoa('x')) === 'x');
mark(2, new TextDecoder().decode(new TextEncoder().encode('café €')) === 'café €');
mark(3, typeof performance.now() === 'number');
mark(4, matchMedia('(min-width: 1000px)').matches && !matchMedia('(max-width: 600px)').matches);
mark(5, new Intl.NumberFormat('en-US').format(1234567) === '1,234,567');
mark(6, new WeakRef({v:1}).deref().v === 1);
mark(7, new DOMParser().parseFromString('<p>hi</p>', 'text/html').querySelector('p').textContent === 'hi');
mark(8, (function(){ var fd = new FormData(); fd.append('a','b'); return fd.get('a') === 'b'; })());
new Response('resp-body').text().then(function (t) {
  mark(9, t === 'resp-body');
  window.postMessage({ ping: 1 }, '*');
});
window.addEventListener('message', function (e) {
  mark(10, e.data.ping === 1);
  document.getElementById('out').textContent = ok.join(' ');
});
</script></body></html>`)
		}), nil
	}
}

// navHost serves a page whose button navigates via location.href, so a case can
// exercise interact surfacing a pending (cross-document) navigation.
func navHost() Host {
	return func() (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><body>
<button id="go">go</button>
<article><h1>Home</h1><p>home page body with sufficient words to survive reduction and remain present in the output.</p></article>
<script>document.getElementById('go').addEventListener('click', function () { location.href = 'https://example.com/dest'; });</script>
</body></html>`)
		}), nil
	}
}

// loginHost is a tiny cookie-gated login flow: GET /login serves a form, POST
// /login sets a session cookie and links to /dashboard, and /dashboard requires
// that cookie. Lifted from internal/browser's statefulServer.
func loginHost() Host {
	return func() (http.Handler, error) {
		mux := http.NewServeMux()
		mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Method == http.MethodPost {
				_ = r.ParseForm()
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
				fmt.Fprintf(w, `<!doctype html><html><head><title>Welcome</title></head><body>`+
					`<h1>Welcome %s</h1><p>You are now signed in to your account dashboard area `+
					`with a session cookie carried on every subsequent request.</p>`+
					`<a href="/dashboard">Go to Dashboard</a></body></html>`,
					r.PostFormValue("username"))
				return
			}
			io.WriteString(w, `<!doctype html><html><head><title>Login</title></head><body>`+
				`<form id="login" action="/login" method="post">`+
				`<input type="text" name="username"><input type="password" name="password">`+
				`<input type="submit" value="Sign in"></form></body></html>`)
		})
		mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if c, err := r.Cookie("session"); err != nil || c.Value != "ok" {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `<!doctype html><html><head><title>Forbidden</title></head>`+
					`<body><p>Please log in.</p></body></html>`)
				return
			}
			io.WriteString(w, `<!doctype html><html><head><title>Dashboard</title></head><body>`+
				`<h1>Dashboard</h1><p>Secret dashboard content for members only.</p></body></html>`)
		})
		return mux, nil
	}
}

// siteHost is a well-behaved host with agent-facing metadata: a text robots.txt
// (Disallow /private, Crawl-delay), a Markdown llms.txt carrying a marker, and a
// HEAD-able llms-full.txt. Mirrors internal/browser's siteServer.
func siteHost() Host {
	return func() (http.Handler, error) {
		const page = `<!doctype html><html><head><title>Home</title></head><body>` +
			`<h1>Home</h1><p>A real page with enough prose to look like content.</p>` +
			`<a href="/page">More</a></body></html>`
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page)
		})
		mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "User-agent: *\nDisallow: /private\nAllow: /private/public\n"+
				"Crawl-delay: 2\nSitemap: https://example.com/sitemap.xml\n")
		})
		mux.HandleFunc("/llms.txt", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = io.WriteString(w, "# Example Docs\n\n> A short summary with an llms-guide-marker inside.\n\n"+
				"## Guides\n- [Intro](/intro): start here\n")
		})
		mux.HandleFunc("/llms-full.txt", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			if r.Method == http.MethodHead {
				return
			}
			_, _ = io.WriteString(w, "# Full Docs\n\nlots of content\n")
		})
		return mux, nil
	}
}

// sitemapHost exercises the map tool: /robots.txt advertises a sitemap, the
// sitemap lists two same-origin URLs plus one cross-origin loc (which must be
// dropped), and the interlinked pages let the same-origin crawl reach a page the
// sitemap omits (/c via /a). URLs are built from r.Host since the base URL is
// unknown until the server starts.
func sitemapHost() Host {
	return func() (http.Handler, error) {
		page := func(title, body string) string {
			return `<!doctype html><html><head><title>` + title + `</title></head><body>` + body + `</body></html>`
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "User-agent: *\nDisallow: /private\nSitemap: http://%s/sitemap.xml\n", r.Host)
		})
		mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
				`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+
				`<url><loc>http://%s/a</loc></url>`+
				`<url><loc>http://%s/b</loc></url>`+
				`<url><loc>https://other.example/x</loc></url>`+ // cross-origin: dropped
				`</urlset>`, r.Host, r.Host)
		})
		mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page("A", `<h1>Page A</h1><a href="/c">to C</a>`))
		})
		mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page("B", `<h1>Page B</h1><p>a leaf page</p>`))
		})
		mux.HandleFunc("/c", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page("C", `<h1>Page C</h1><p>reached only by crawling</p>`))
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page("Home",
				`<h1>Home</h1><a href="/a">A</a><a href="/b">B</a>`+
					`<a href="https://other.example/ext">external</a>`))
		})
		return mux, nil
	}
}

// fakeSearch is a deterministic search.Provider for the eval: it returns canned
// results (count-truncated) so the search tool can be exercised offline.
type fakeSearch struct{ results []search.Result }

func (fakeSearch) Name() string { return "fake" }

func (f fakeSearch) Search(_ context.Context, _ string, o search.Options) ([]search.Result, error) {
	if o.Count > 0 && o.Count < len(f.results) {
		return f.results[:o.Count], nil
	}
	return f.results, nil
}

// contentHost serves a single fixture with an explicit Content-Type (raw bytes,
// so binary fixtures stay byte-exact) on every non-metadata path, 404ing the
// origin-root metadata files exactly like htmlHost. Used for the non-HTML corpus.
func contentHost(path, mime string) Host {
	return func() (http.Handler, error) {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read fixture %s: %w", path, err)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt", "/llms.txt", "/llms-full.txt":
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", mime)
			_, _ = w.Write(body)
		}), nil
	}
}

// uploadHost serves a multipart upload form and echoes back the submitted
// field, filename, and file content — proving submit_form's multipart path
// end-to-end through MCP.
func uploadHost() Host {
	return func() (http.Handler, error) {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<!doctype html><html><head><title>Upload</title></head><body>`+
				`<form id="up" action="/upload" method="post" enctype="multipart/form-data">`+
				`<input type="text" name="note"><input type="file" name="doc">`+
				`<input type="submit" value="Send"></form></body></html>`)
		})
		mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><head><title>Bad</title></head><body>parse error: %v</body></html>", err)
				return
			}
			f, fh, err := r.FormFile("doc")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><head><title>Bad</title></head><body>no file: %v</body></html>", err)
				return
			}
			defer f.Close()
			data, _ := io.ReadAll(f)
			fmt.Fprintf(w, `<!doctype html><html><head><title>Received</title></head><body>`+
				`<p>note=%s filename=%s content=%s</p></body></html>`,
				r.FormValue("note"), fh.Filename, data)
		})
		return mux, nil
	}
}

// authHost gates content behind credentials: /protected needs Authorization:
// Bearer letmein, /api.json needs X-Api-Key: k-9000. Unmatched paths (incl. the
// metadata probes) 404 via the mux default. Proves credentialed reads.
func authHost() Host {
	return func() (http.Handler, error) {
		mux := http.NewServeMux()
		mux.HandleFunc("/protected", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Header.Get("Authorization") != "Bearer letmein" {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `<!doctype html><html><head><title>Unauthorized</title></head><body>`+
					`<p>You must supply a valid bearer token to view this protected resource.</p></body></html>`)
				return
			}
			io.WriteString(w, `<!doctype html><html><head><title>Secret</title></head><body>`+
				`<h1>Members Area</h1><p>The magic-token-payload is visible only to authenticated `+
				`callers who presented a valid bearer token on this request.</p></body></html>`)
		})
		mux.HandleFunc("/api.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("X-Api-Key") != "k-9000" {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":"unauthorized"}`)
				return
			}
			io.WriteString(w, `{"balance":4242,"currency":"usd"}`)
		})
		return mux, nil
	}
}

// crossOriginRedirectHost proves credentials never cross an origin boundary: the
// primary origin's /redirect 302s to a SECOND origin (a distinct httptest server)
// that echoes back the auth headers it received. The downstream server is left
// running for the eval process's lifetime — bounded and offline, no teardown hook
// on Host — so the redirect target stays reachable while the case runs.
func crossOriginRedirectHost() Host {
	return func() (http.Handler, error) {
		downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><html><head><title>Echo</title></head><body>`+
				`<h1>echo-endpoint</h1><p>This downstream origin received these request headers, `+
				`shown so the eval can confirm no credential crossed the origin boundary.</p>`+
				`<pre>authorization=[%s] x-api-key=[%s]</pre></body></html>`,
				r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
		}))
		mux := http.NewServeMux()
		mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, downstream.URL+"/sink", http.StatusFound)
		})
		return mux, nil
	}
}

// Corpus fixture paths, relative to the eval package directory (the test cwd).
const (
	fxArticle    = "../testdata/article.input.html"
	fxStructured = "../testdata/structured.input.html"
	fxJSRender   = "../testdata/js_render.input.html"
	fxLongread   = "corpus/longread.input.html"
	fxJunky      = "corpus/junky.input.html"
	fxJSON       = "corpus/data.input.json"
	fxText       = "corpus/note.input.txt"
	fxFeed       = "corpus/feed.input.xml"
	fxPNG        = "corpus/pixel.input.png"
	fxPDF        = "corpus/sample.pdf"
	fxData       = "corpus/structured-data.input.html"
	fxUnsafe     = "corpus/unsafe.input.html"
)

// SPA shells rendered by real framework bundles in the eval cases below. Each
// mounts an <article> with a stable title marker into an empty root.
const (
	preactApp = `<!doctype html><html><body><div id="root">loading</div>
<script src="/fw/preact.umd.js"></script>
<script>
  var h = preact.h;
  function App() { return h('article', null,
    h('h1', null, 'Preact Rendered Title'),
    h('p', null, 'A real Preact bundle rendered this article under unblink, with enough prose for the reducer to keep it as the page content.')); }
  preact.render(h(App), document.getElementById('root'));
</script></body></html>`

	reactApp = `<!doctype html><html><body><div id="root">loading</div>
<script src="/fw/react.production.min.js"></script>
<script src="/fw/react-dom.production.min.js"></script>
<script>
  var e = React.createElement;
  function App() { return e('article', null,
    e('h1', null, 'React Rendered Title'),
    e('p', null, 'A real React bundle rendered this article under unblink, with enough prose for the reducer to keep it as the page content.')); }
  ReactDOM.createRoot(document.getElementById('root')).render(e(App));
</script></body></html>`

	vueApp = `<!doctype html><html><body><div id="app">loading</div>
<script src="/fw/vue.global.prod.js"></script>
<script>
  Vue.createApp({ template: '<article><h1>{{ t }}</h1><p>{{ p }}</p></article>',
    data() { return { t: 'Vue Rendered Title', p: 'A real Vue bundle rendered this article under unblink, with enough prose for the reducer to keep it as the page content.' }; } }).mount('#app');
</script></body></html>`

	// svelteApp mounts a precompiled Svelte bundle (see testdata/frameworks).
	svelteApp = `<!doctype html><html><body><div id="app">loading</div>
<script src="/fw/svelte-app.iife.js"></script></body></html>`

	// litApp defines a LitElement rendering shadow content (flattened + visible to
	// extraction) and exercises the constructable-stylesheet degradation path.
	litApp = "<!doctype html><html><body><my-article></my-article>\n" +
		`<script type="module">
  import { LitElement, html, css } from '/fw/lit-all.min.js';
  class MyArticle extends LitElement {
    static styles = css` + "`p { color: rebeccapurple; }`" + `;
    render() { return html` + "`<article><h1>Lit Rendered Title</h1><p>A real Lit component rendered this article under unblink, with enough prose for the reducer to keep it as the page content.</p></article>`" + `; }
  }
  customElements.define('my-article', MyArticle);
</script></body></html>`

	// shadowSlotApp is a vanilla web component that renders a shadow scaffold with named +
	// default <slot>s. The scaffold's own text ("Featured Article") appears only after JS
	// runs; the slotted light content ("Shadow Post Title" / body) is composed into the slots.
	shadowSlotApp = `<!doctype html><html><body><x-post><span slot="title">Shadow Post Title</span><p>Shadow post body with enough prose for the reducer to keep it as the page content here today.</p></x-post>
<script>
  class XPost extends HTMLElement {
    connectedCallback() {
      this.attachShadow({mode:'open'}).innerHTML =
        '<article><p class="tag">Featured Article</p><h1><slot name="title"></slot></h1><div class="body"><slot></slot></div></article>';
    }
  }
  customElements.define('x-post', XPost);
</script></body></html>`
)

// allToolNames is the full set of tools the server must register.
var allToolNames = []string{
	"read", "browse", "links", "forms", "find", "click", "submit_form",
	"controls", "interact", "data", "site", "session", "map", "search",
}

// cases is the seed eval corpus: ~12 cases spanning all the tools and every
// quality axis. Each case is one flow scored over its whole transcript.
func cases() []Case {
	return []Case{
		{
			Name:     "registration-smoke",
			Host:     htmlHost(fxArticle),
			Scorers:  []Scorer{ToolsRegistered(len(allToolNames), allToolNames...)},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name: "read-article-clean",
			Host: htmlHost(fxArticle),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "article", "max_tokens": 6000}},
			},
			Scorers: []Scorer{
				NoJunk(0, "<script", "console.log", "hotpink", "Skip to navigation"),
				Recall(0, "The Quiet Decline of the Visual Web", "semantic meaning"),
				TokenBudget(0, 6000),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "read-junky-strip",
			Host: htmlHost(fxJunky),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "article"}},
			},
			Scorers: []Scorer{
				NoJunk(0, "Accept all cookies", "Subscribe", "ADVERTISEMENT", "share on twitter"),
				Recall(0, "Edmund Hale", "Saltmarsh Point", "logged the tide"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name: "browse-structure",
			Host: htmlHost(fxStructured),
			Steps: []Step{
				{Tool: "browse", Path: "/"},
			},
			Scorers: []Scorer{
				BrowseTitle(0, "Structured Test Page"),
				BrowseCounts(0, 1, 4),
				Recall(0, "Installation", "Usage"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "links-internal",
			Host: htmlHost(fxStructured),
			Steps: []Step{
				{Tool: "links", Path: "/", Args: map[string]any{"internal_only": true}},
				{Tool: "links", Path: "/", Args: map[string]any{"filter": "about"}},
			},
			Scorers: []Scorer{
				LinksInternal(0, []string{"/about", "/privacy"}, []string{"external-site.example.org"}),
				LinksCount(1, 1),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "forms-fields",
			Host: htmlHost(fxStructured),
			Steps: []Step{
				{Tool: "forms", Path: "/"},
			},
			Scorers: []Scorer{
				FormShape(0, "POST", []FieldSpec{
					{Name: "fullname", Type: "text", Required: true},
					{Name: "email", Type: "email", Required: true},
					{Name: "plan", Type: "select", Options: []string{"free", "pro"}},
					{Name: "newsletter", Type: "checkbox"},
				}),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name: "find-pomegranate",
			Host: htmlHost(fxStructured),
			Steps: []Step{
				{Tool: "find", Path: "/", Args: map[string]any{"query": "pomegranate"}},
			},
			Scorers:  []Scorer{FindHit(0, "pomegranate", "Requirements", 1)},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "read-pagination-continuity",
			Host: htmlHost(fxLongread),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"max_tokens": 800}},
			},
			Scorers: []Scorer{
				PaginationRoundTrip(0, 800, "alpha-sentinel", "omega-sentinel"),
				Recall(0, "alpha-sentinel"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name: "session-login-flow",
			Host: loginHost(),
			Steps: []Step{
				{Tool: "session", Args: map[string]any{"action": "new", "session": "s"}},
				{Tool: "browse", Path: "/login", Args: map[string]any{"session": "s"}},
				{Tool: "submit_form", Args: map[string]any{"session": "s", "form": "login",
					"values": map[string]any{"username": "alice", "password": "secret"}}},
				{Tool: "read", Args: map[string]any{"session": "s", "use_current": true, "mode": "full"}},
				{Tool: "click", Args: map[string]any{"session": "s", "match": "dashboard"}},
			},
			Scorers: []Scorer{
				SessionTitle(2, "Welcome"),
				Recall(3, "alice"),
				SessionTitle(4, "Dashboard"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "session-history-back",
			Host: loginHost(),
			Steps: []Step{
				{Tool: "session", Args: map[string]any{"action": "new", "session": "h"}},
				{Tool: "browse", Path: "/login", Args: map[string]any{"session": "h"}},
				{Tool: "submit_form", Args: map[string]any{"session": "h", "form": "login",
					"values": map[string]any{"username": "bob"}}},
				{Tool: "click", Args: map[string]any{"session": "h", "match": "dashboard"}},
				{Tool: "session", Args: map[string]any{"action": "history", "session": "h"}},
				{Tool: "session", Args: map[string]any{"action": "back", "session": "h"}},
				{Tool: "session", Args: map[string]any{"action": "state", "session": "h"}},
			},
			Scorers: []Scorer{
				HistoryShape(4, 3, 2),
				SessionCurrentTitle(5, "Welcome"),
				StateURLContains(6, "/login"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			// A session-scoped bearer unlocks gated content; the token is never
			// echoed back in the session state (redaction).
			Name: "auth-session-bearer",
			Host: authHost(),
			Steps: []Step{
				{Tool: "session", Path: "/", Args: map[string]any{"action": "new", "session": "auth",
					"auth": map[string]any{"type": "bearer", "token": "letmein"}}},
				{Tool: "read", Path: "/protected", Args: map[string]any{"session": "auth", "mode": "full"}},
				{Tool: "session", Args: map[string]any{"action": "state", "session": "auth"}},
			},
			Scorers: []Scorer{
				Recall(1, "magic-token-payload"),
				NotRecall(2, "letmein"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// One-shot header auth on a stateless read of a gated JSON API.
			Name: "auth-oneshot-apikey",
			Host: authHost(),
			Steps: []Step{
				{Tool: "read", Path: "/api.json", Args: map[string]any{
					"headers": map[string]any{"X-Api-Key": "k-9000"}}},
			},
			Scorers: []Scorer{
				Recall(0, "4242", "currency"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// Without credentials the gate holds: the unauthorized page is returned,
			// never the secret. Proves the bearer case above isn't trivially passing.
			Name: "auth-missing-is-unauthorized",
			Host: authHost(),
			Steps: []Step{
				{Tool: "read", Path: "/protected", Args: map[string]any{"mode": "full"}},
			},
			Scorers: []Scorer{
				Recall(0, "valid bearer token"),
				NotRecall(0, "magic-token-payload"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// Security: a session's bearer + custom header are scoped to their origin.
			// Reading a path that 302s to a DIFFERENT origin must strip both — the
			// downstream echo shows neither credential crossed the boundary.
			Name: "auth-no-cross-origin-leak",
			Host: crossOriginRedirectHost(),
			Steps: []Step{
				{Tool: "session", Path: "/", Args: map[string]any{"action": "new", "session": "sec",
					"auth":    map[string]any{"type": "bearer", "token": "letmein"},
					"headers": map[string]any{"X-Api-Key": "supersecretkey"}}},
				{Tool: "read", Path: "/redirect", Args: map[string]any{"session": "sec", "mode": "full"}},
			},
			Scorers: []Scorer{
				Recall(1, "echo-endpoint"),
				NotRecall(1, "letmein", "supersecretkey"),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name: "site-metadata",
			Host: siteHost(),
			Steps: []Step{
				{Tool: "site", Path: "/"},
			},
			Scorers: []Scorer{
				SiteRobots(0, []string{"/private"}, true),
				LLMsInline(0, "llms-guide-marker", true),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name:    "js-render-content",
			Host:    htmlHost(fxJSRender),
			Browser: []browser.Option{browser.WithJS(2 * time.Second)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": false}},
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers: []Scorer{
				RenderPresence(0, []string{"JS Rendered Title"}, 1, []string{"JS Rendered Title", "clientsiderendered"}),
			},
			Floor:    0.9,
			MustPass: true, // the JS engine is a headline capability; regressions gate
		},
		{
			Name:    "preact-render",
			Host:    frameworkHost(preactApp, map[string]string{"/fw/preact.umd.js": "../testdata/frameworks/preact.umd.js"}),
			Browser: []browser.Option{browser.WithJS(5 * time.Second), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": false}},
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers:  []Scorer{RenderPresence(0, []string{"Preact Rendered Title"}, 1, []string{"Preact Rendered Title"})},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name: "react-render",
			Host: frameworkHost(reactApp, map[string]string{
				"/fw/react.production.min.js":     "../testdata/frameworks/react.production.min.js",
				"/fw/react-dom.production.min.js": "../testdata/frameworks/react-dom.production.min.js",
			}),
			Browser: []browser.Option{browser.WithJS(5 * time.Second), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": false}},
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers:  []Scorer{RenderPresence(0, []string{"React Rendered Title"}, 1, []string{"React Rendered Title"})},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name:    "vue-render",
			Host:    frameworkHost(vueApp, map[string]string{"/fw/vue.global.prod.js": "../testdata/frameworks/vue.global.prod.js"}),
			Browser: []browser.Option{browser.WithJS(5 * time.Second), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": false}},
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers:  []Scorer{RenderPresence(0, []string{"Vue Rendered Title"}, 1, []string{"Vue Rendered Title"})},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name:    "svelte-render",
			Host:    frameworkHost(svelteApp, map[string]string{"/fw/svelte-app.iife.js": "../testdata/frameworks/svelte-app.iife.js"}),
			Browser: []browser.Option{browser.WithJS(5 * time.Second), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": false}},
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers:  []Scorer{RenderPresence(0, []string{"Svelte Rendered Title"}, 1, []string{"Svelte Rendered Title"})},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name:    "lit-render",
			Host:    frameworkHost(litApp, map[string]string{"/fw/lit-all.min.js": "../testdata/frameworks/lit-all.min.js"}),
			Browser: []browser.Option{browser.WithJS(5 * time.Second), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": false}},
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			// The shadow content (flattened) must survive extraction after render.
			Scorers:  []Scorer{RenderPresence(0, []string{"Lit Rendered Title"}, 1, []string{"Lit Rendered Title"})},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// Slot composition: without JS the shadow scaffold text is absent (and slotted
			// light content is plain light DOM); after render the compose pass flattens the
			// scaffold AND distributes the slotted light content into its <slot> positions.
			Name:    "shadow-slots-render",
			Host:    frameworkHost(shadowSlotApp, nil),
			Browser: []browser.Option{browser.WithJS(5 * time.Second), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": false}},
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers:  []Scorer{RenderPresence(0, []string{"Featured Article"}, 1, []string{"Featured Article", "Shadow Post Title", "Shadow post body"})},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// A tight setInterval mutates the DOM forever, so a short budget always
			// closes mid-work: the starved-render diagnostics (render_budget_hit +
			// dom_busy) must fire, and static content still survives.
			Name:    "render-starved-diagnostics",
			Host:    starvedRenderHost(),
			Browser: []browser.Option{browser.WithJS(300 * time.Millisecond), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers: []Scorer{
				RenderSaturation(0, true, true, 0),
				Recall(0, "static-sentinel-content"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// The page fires 12 subrequests against a --js-max-requests budget of 3,
			// so the excess are budget-denied: net_denied ≥ 1. Static content survives.
			Name:    "render-net-denied",
			Host:    budgetDeniedHost(),
			Browser: []browser.Option{browser.WithJS(2 * time.Second), browser.WithJSAllowPrivate(true), browser.WithJSMaxRequests(3)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers: []Scorer{
				RenderSaturation(0, false, false, 1),
				Recall(0, "guarded-page-sentinel"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// The full search gesture through MCP: type a query into a box and press
			// Enter; the keydown handler (reading e.key) renders the results region.
			Name:    "interact-keydown-search",
			Host:    keydownSearchHost(),
			Browser: []browser.Option{browser.WithJS(2 * time.Second)},
			Steps: []Step{
				{Tool: "session", Args: map[string]any{"action": "new", "session": "kd"}},
				{Tool: "browse", Path: "/", Args: map[string]any{"session": "kd"}},
				{Tool: "interact", Args: map[string]any{"session": "kd", "selector": "#q", "event": "keydown", "value": "pomegranate", "key": "Enter"}},
				{Tool: "read", Args: map[string]any{"session": "kd", "use_current": true, "mode": "full"}},
			},
			Scorers: []Scorer{
				InteractChanged(2, true),
				Recall(3, "keydown-results-sentinel", "pomegranate"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// A setTimeout 7s past a 2s budget: the timer clamp pulls it in so the
			// deferred content still materializes (pre-Phase-21 it vanished).
			Name:    "timer-deferred-content",
			Host:    deferredTimerHost(),
			Browser: []browser.Option{browser.WithJS(2 * time.Second), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers: []Scorer{
				Recall(0, "deferred-timer-sentinel"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// One page exercising every Phase-21 tier-1 API cluster; each cluster that
			// works appends an api-N-ok marker. The MCP-level regression net.
			Name:    "js-api-smoke",
			Host:    apiSmokeHost(),
			Browser: []browser.Option{browser.WithJS(2 * time.Second)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full", "render": true}},
			},
			Scorers: []Scorer{
				Recall(0, "api-1-ok", "api-2-ok", "api-3-ok", "api-4-ok", "api-5-ok",
					"api-6-ok", "api-7-ok", "api-8-ok", "api-9-ok", "api-10-ok"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// wait_for holds the render open for content that a delayed in-page fetch
			// injects after the (short) engine budget; wait_timeout extends it and
			// wait_met reports success. Proves the settle + wait_for path end-to-end.
			Name:    "read-wait-for",
			Host:    delayedContentHost(250 * time.Millisecond),
			Browser: []browser.Option{browser.WithJS(150 * time.Millisecond), browser.WithJSAllowPrivate(true)},
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full",
					"wait_for": "#answer", "wait_timeout": 3}},
			},
			Scorers: []Scorer{
				WaitMet(0, true),
				Recall(0, "delayed-answer-payload"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// interact drives a handler that navigates via location.href; the target
			// surfaces as pending_navigation (interact never navigates itself). This is
			// the first end-to-end scoring of the interact tool through MCP.
			Name:    "interact-navigation",
			Host:    navHost(),
			Browser: []browser.Option{browser.WithJS(2 * time.Second)},
			Steps: []Step{
				{Tool: "session", Args: map[string]any{"action": "new", "session": "nav"}},
				{Tool: "browse", Path: "/", Args: map[string]any{"session": "nav"}},
				{Tool: "interact", Args: map[string]any{"session": "nav", "selector": "#go", "event": "click"}},
			},
			Scorers: []Scorer{
				PendingNavigation(2, "https://example.com/dest"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			Name: "read-error",
			Host: htmlHost(fxArticle),
			Steps: []Step{
				{Tool: "read", Args: map[string]any{}},
			},
			Scorers:  []Scorer{IsErrorIs(0, true)},
			Floor:    1.0,
			MustPass: true,
		},
		{
			Name: "read-json",
			Host: contentHost(fxJSON, "application/json"),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{}},
			},
			Scorers: []Scorer{
				Recall(0, "```json", `"project": "unblink"`, `"license": "MIT"`),
				KindIs(0, "json"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "read-text",
			Host: contentHost(fxText, "text/plain; charset=utf-8"),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{}},
			},
			Scorers: []Scorer{
				Recall(0, "TEXT-SENTINEL-OK"),
				KindIs(0, "text"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "read-feed",
			Host: contentHost(fxFeed, "application/rss+xml"),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{}},
			},
			Scorers: []Scorer{
				Recall(0, "Example Feed", "First Post", "Second Post", "example.com/posts/1"),
				KindIs(0, "feed"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "read-image-manifest",
			Host: contentHost(fxPNG, "image/png"),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{}},
			},
			Scorers: []Scorer{
				Recall(0, "Image", "4×4", "image/png"),
				KindIs(0, "image"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "read-image-bytes",
			Host: contentHost(fxPNG, "image/png"),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"include_bytes": true}},
			},
			Scorers: []Scorer{
				BinaryIntegrity(0, fxPNG),
				KindIs(0, "image"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "read-pdf",
			Host: contentHost(fxPDF, "application/pdf"),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{}},
			},
			Scorers: []Scorer{
				Recall(0, "Hello unblink PDF sentinel"),
				KindIs(0, "pdf"),
			},
			Floor:    0.9,
			MustPass: true, // the fixture is deterministic; extraction regressions gate
		},
		{
			// raw_html escape hatch: markdown strips the script/form, raw_html
			// surfaces them verbatim. fxStructured already carries a stripped
			// <script>console.log('should be stripped')</script> and <form id=signup>.
			Name: "read-raw-html",
			Host: htmlHost(fxStructured),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "article"}},
				{Tool: "read", Path: "/", Args: map[string]any{"format": "raw_html"}},
			},
			Scorers: []Scorer{
				NoJunk(0, "<script", "console.log('should be stripped')"),
				Recall(1, "<script", "console.log('should be stripped')", "<form", `id="signup"`),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			// A selector scopes raw_html to just the matching subtree(s).
			Name: "read-raw-html-selector",
			Host: htmlHost(fxStructured),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"format": "raw_html", "selector": "script"}},
			},
			Scorers: []Scorer{
				Recall(0, "console.log('should be stripped')"),
				NoJunk(0, "<form", "Structured Test Page"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			// format=text returns visible plain text (no markup, no scripts).
			// Named read-format-text to avoid colliding with the read-text case.
			Name: "read-format-text",
			Host: htmlHost(fxStructured),
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"format": "text"}},
			},
			Scorers: []Scorer{
				Recall(0, "Structured Test Page", "Installation"),
				NoJunk(0, "<script", "console.log", "<h1>"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "data-jsonld",
			Host: htmlHost(fxData),
			Steps: []Step{
				{Tool: "data", Path: "/", Args: map[string]any{"kind": "jsonld"}},
			},
			Scorers: []Scorer{
				DataContains(0, "@type", "Acme Widget", "AggregateRating", "Widgets Explained"),
				DataCounts(0, 3, 0, 0), // Product + @graph{Article,Organization}; malformed skipped
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "data-tables",
			Host: htmlHost(fxData),
			Steps: []Step{
				{Tool: "data", Path: "/", Args: map[string]any{"kind": "tables"}},
			},
			Scorers: []Scorer{
				DataContains(0, "Quarterly Sales", "Region", "North", "pending"),
				DataCounts(0, 0, 1, 0),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			Name: "data-microdata",
			Host: htmlHost(fxData),
			Steps: []Step{
				{Tool: "data", Path: "/", Args: map[string]any{"kind": "microdata"}},
			},
			Scorers: []Scorer{
				DataContains(0, "schema.org/Person", "Grace Hopper", "PostalAddress", "Arlington"),
				DataCounts(0, 0, 0, 1),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			// map: sitemap-declared (/a, /b) + crawl-discovered (/c via /a) URLs,
			// all same-origin (the cross-origin sitemap loc and external link are dropped).
			Name: "map-sitemap-crawl",
			Host: sitemapHost(),
			Steps: []Step{
				{Tool: "map", Path: "/", Args: map[string]any{"max_urls": 50, "max_depth": 3}},
			},
			Scorers: []Scorer{
				MapSameOrigin(0),
				MapContains(0, "/a", "/b", "/c"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// map: a tight max_urls binds and is reported as truncated.
			Name: "map-respects-cap",
			Host: sitemapHost(),
			Steps: []Step{
				{Tool: "map", Path: "/", Args: map[string]any{"max_urls": 1, "max_depth": 3}},
			},
			Scorers: []Scorer{
				MapRespectsCap(0, 1),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// search: with a provider configured, canned results come back.
			Name: "search-configured",
			Host: htmlHost(fxArticle),
			Browser: []browser.Option{browser.WithSearchProvider(fakeSearch{results: []search.Result{
				{Title: "First", URL: "https://a.example/1", Snippet: "one"},
				{Title: "Second", URL: "https://a.example/2", Snippet: "two"},
			}})},
			Steps: []Step{
				{Tool: "search", Args: map[string]any{"query": "unblink", "count": 5}},
			},
			Scorers: []Scorer{
				SearchReturns(0, "https://a.example/1", "https://a.example/2"),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// search: with no provider, the tool is still registered but errors cleanly.
			Name: "search-unconfigured",
			Host: htmlHost(fxArticle),
			Steps: []Step{
				{Tool: "search", Args: map[string]any{"query": "unblink"}},
			},
			Scorers: []Scorer{
				SearchUnconfigured(0),
			},
			Floor:    0.9,
			MustPass: true,
		},
		{
			// The production safe-output pipeline end-to-end through MCP: the result
			// is framed as untrusted, human-hidden injection text is stripped, the
			// image beacon is defanged, and the visible content still comes through.
			Name: "safe-output-fencing",
			Host: htmlHost(fxUnsafe),
			Safe: true,
			Steps: []Step{
				{Tool: "read", Path: "/", Args: map[string]any{"mode": "full"}},
			},
			Scorers: []Scorer{
				FramedUntrusted(0),
				NoJunk(0, "hidden-injection-payload", "offscreen-injection-payload", "!["),
				Recall(0, "Visible Headline", "second visible paragraph", "[image: beacon-alt"),
			},
			Floor:    0.95,
			MustPass: true,
		},
		{
			// submit_form uploads a file: the multipart-declared form switches the
			// encoding and the inline file content arrives as a real file part.
			Name: "submit-multipart-upload",
			Host: uploadHost(),
			Steps: []Step{
				{Tool: "session", Args: map[string]any{"action": "new", "session": "up"}},
				{Tool: "browse", Path: "/", Args: map[string]any{"session": "up"}},
				{Tool: "submit_form", Args: map[string]any{"session": "up", "form": "up",
					"values": map[string]any{"note": "hello"},
					"files": []map[string]any{{"field": "doc", "filename": "notes.txt",
						"mime": "text/plain", "content": "upload-payload"}}}},
				{Tool: "read", Args: map[string]any{"session": "up", "use_current": true, "mode": "full"}},
			},
			Scorers: []Scorer{
				SessionTitle(2, "Received"),
				Recall(3, "note=hello", "filename=notes.txt", "content=upload-payload"),
			},
			Floor:    0.95,
			MustPass: true,
		},
	}
}
