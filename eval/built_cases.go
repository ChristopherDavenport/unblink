//go:build eval

package eval

import (
	"time"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// builtJS is the browser configuration every built-fixture case renders with:
// the engine on with the production default budget's order of magnitude, and
// page-JS subrequests allowed to reach the loopback fixture host.
func builtJS() []browser.Option {
	return []browser.Option{browser.WithJS(5 * time.Second), browser.WithJSAllowPrivate(true)}
}

// preactHooksHtmApp is a no-build Preact app: the UMD builds of preact, its hooks
// and htm as classic scripts, with a tagged-template view. The effect's text only
// appears if hooks ran after the first render; the click goes through a hook
// state setter.
const preactHooksHtmApp = `<!doctype html><html><head><title>Preact Hooks Htm</title></head><body><div id="root">loading shell</div>
<script src="/fw/preact.umd.js"></script>
<script src="/fw/preact.hooks.umd.js"></script>
<script src="/fw/htm.umd.js"></script>
<script>
  var html = htm.bind(preact.h);
  function Loom() {
    var woven = preactHooks.useState(0), picks = woven[0], setPicks = woven[1];
    var ready = preactHooks.useState('effect pending'), status = ready[0], setStatus = ready[1];
    preactHooks.useEffect(function () { setStatus('Hooks effect ran'); }, []);
    return html` + "`" + `<article>
      <h1>Preact Hooks Loom</h1>
      <p>A no-build Preact page: hooks and htm tagged templates from their UMD bundles.</p>
      <p id="status">${status}</p>
      <button id="weave" onClick=${function () { setPicks(picks + 1); }}>Weave</button>
      <p id="picks">Picks woven: ${picks}</p>
    </article>` + "`" + `;
  }
  preact.render(html` + "`<${Loom} />`" + `, document.getElementById('root'));
</script></body></html>`

// builtCases are the eval cases over the framework fixtures built from source
// (testdata/frameworks/built, rebuilt by `make eval-fixtures`). Each fixture is
// the unmodified production output of a real toolchain — hashed chunks, the
// bundler's own runtime, the framework's own hydration — served as a static
// site. Where the single-file fixtures in cases() prove "a framework can render
// one article", these prove the paths real sites take: module graphs with lazy
// chunks, bundler chunk-loading runtimes, history-mode routing, data fetched
// after mount, and server-rendered markup hydrated in place.
//
// Conventions: step 0 is the static read (render=false) and step 1 the rendered
// read, so RenderPresence can show what JavaScript added. A requests step comes
// straight after the first rendered read of a URL: it reports that render's log
// from the page cache, whereas a later re-render loads scripts from the asset
// cache and logs no request for them.
func builtCases() []Case {
	read := func(path string, render bool) Step {
		return Step{Tool: "read", Path: path, Args: map[string]any{"mode": "full", "render": render}}
	}
	// readWait is a rendered read that holds until text appears (or 4s pass).
	readWait := func(path, text string) Step {
		return Step{Tool: "read", Path: path, Args: map[string]any{"mode": "full", "wait_text": text, "wait_timeout": 4}}
	}
	requests := func(path string) Step {
		return Step{Tool: "requests", Path: path}
	}
	// A session flow: open path in a live session, then alternate interactions
	// and reads of the session's current page. Steps 0 and 1 are session/browse;
	// the interactions and reads follow in the order given.
	session := func(id, path string, rest ...Step) []Step {
		steps := []Step{
			{Tool: "session", Args: map[string]any{"action": "new", "session": id}},
			{Tool: "browse", Path: path, Args: map[string]any{"session": id}},
		}
		for _, st := range rest {
			args := map[string]any{"session": id}
			for k, v := range st.Args {
				args[k] = v
			}
			steps = append(steps, Step{Tool: st.Tool, Args: args})
		}
		return steps
	}
	click := func(selector string) Step {
		return Step{Tool: "interact", Args: map[string]any{"selector": selector, "event": "click"}}
	}
	change := func(selector, value string) Step {
		return Step{Tool: "interact", Args: map[string]any{"selector": selector, "event": "change", "value": value}}
	}
	current := Step{Tool: "read", Args: map[string]any{"use_current": true, "mode": "full"}}

	return []Case{
		{
			// Vite + React 19, client-only. The orchard chunk is lazily imported
			// and rendered; the cellar chunk is lazily imported and never rendered.
			// The read waits for the lazy section: React reveals a resolved
			// Suspense boundary on a ~300ms throttle timer, which a default read
			// does not sit through (the known gap below).
			Name:    "built-vite-react-lazy-chunks",
			Host:    builtHost("vite-react"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/"), readWait("/", "Lazy Orchard Chunk Loaded")},
			Scorers: []Scorer{
				RenderPresence(0, []string{"Vite React Orchard Ledger", "Lazy Orchard Chunk Loaded"},
					3, []string{"Vite React Orchard Ledger", "Lazy Orchard Chunk Loaded", "Quince and medlar rows"}),
				Recall(1, "Vite React Orchard Ledger"),
				WaitMet(3, true),
				NotRecall(3, "Cellar Chunk Never Requested", "orchard pending", "loading shell"),
				NoJSErrors(1),
				NoJSErrors(3),
				RenderedNotDiscarded(3),
				Requested(2, []string{"/assets/index-", "/assets/Orchard-"}, []string{"Cellar-"}),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
				TokenBudget(3, 400),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name:    "built-vite-react-suspense-reveal-default-read",
			Host:    builtHost("vite-react"),
			Browser: builtJS(),
			Steps:   []Step{read("/", true)},
			Scorers: []Scorer{
				Recall(0, "Lazy Orchard Chunk Loaded", "Quince and medlar rows"),
				NotRecall(0, "orchard pending"),
			},
			Floor: 0.99,
			KnownGap: "a default rendered read returns React 19's Suspense fallback (\"orchard pending\"): the lazy chunk has loaded, " +
				"but React commits the reveal from a ~300ms throttle setTimeout, and the settle closes after its 60ms quiet window " +
				"without waiting for a pending timer that short. wait_text gets the content; a plain read does not.",
		},
		{
			// The same build with the never-used chunk missing from the host: a
			// lazy chunk nobody imports must not be able to break the app.
			Name:    "built-vite-react-unused-chunk-404",
			Host:    builtHost("vite-react", withDenied("Cellar-")),
			Browser: builtJS(),
			Steps:   []Step{read("/", true), requests("/"), readWait("/", "Lazy Orchard Chunk Loaded")},
			Scorers: []Scorer{
				Recall(2, "Vite React Orchard Ledger", "Lazy Orchard Chunk Loaded"),
				NoJSErrors(0),
				NoJSErrors(2),
				Requested(1, []string{"/assets/Orchard-"}, []string{"Cellar-"}),
				NoFailedRequests(1),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// And with the chunk the app does use missing: the 404 is visible in
			// the request log, the lazy section does not appear, any script error
			// is about that chunk, and the unused chunk is still not requested.
			Name:    "built-vite-react-used-chunk-404",
			Host:    builtHost("vite-react", withDenied("Orchard-")),
			Browser: builtJS(),
			Steps:   []Step{read("/", true), requests("/")},
			Scorers: []Scorer{
				NotRecall(0, "Lazy Orchard Chunk Loaded"),
				JSErrorsOnly(0, "Orchard-", "dynamically imported module", "import("),
				Requested(1, []string{"/assets/index-"}, []string{"Cellar-"}),
				FailedRequest(1, "/assets/Orchard-", 404),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Vite + Vue 3 + vue-router in history mode, entered by deep link. The
			// route component is a code-split chunk and its data is fetched after
			// mount from a JSON file.
			Name:    "built-vite-vue-router-deep-link",
			Host:    builtHost("vite-vue", withSPAFallback("index.html")),
			Browser: builtJS(),
			Steps:   []Step{read("/tides/harbor", false), read("/tides/harbor", true), requests("/tides/harbor")},
			Scorers: []Scorer{
				RenderPresence(0, []string{"Vue Router Deep Link", "Fetched Station Wrenmouth"},
					1, []string{"Vue Router Deep Link: harbor", "Fetched Station Wrenmouth", "Dawn springtide reaches 4.2 metres", "Dusk neaptide reaches 1.7 metres"}),
				NotRecall(1, "tide table pending", "Vite Vue Home Route", "loading shell"),
				NoJSErrors(1),
				RenderedNotDiscarded(1),
				Requested(2, []string{"/assets/index-", "/assets/Tides-", "/tides.json"}, nil),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// The home route of the same build: the tides chunk and its data
			// belong to a route that was not entered.
			Name:    "built-vite-vue-home-route",
			Host:    builtHost("vite-vue", withSPAFallback("index.html")),
			Browser: builtJS(),
			Steps:   []Step{read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "Vite Vue Home Route", "Harbor tides"),
				NotRecall(0, "Vue Router Deep Link"),
				NoJSErrors(0),
				Requested(1, []string{"/assets/index-"}, []string{"Tides-", "tides.json"}),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// webpack 5, classic output: a deferred entry script, the webpackChunk
			// JSONP runtime, and a split chunk loaded by <script> injection.
			Name:    "built-webpack-classic-script-injection",
			Host:    builtHost("webpack-classic"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				RenderPresence(0, []string{"Webpack Almanac Shell", "Webpack Split Chunk Loaded"},
					1, []string{"Webpack Almanac Shell", "Webpack Split Chunk Loaded", "Frost dates and moon phases"}),
				NotRecall(1, "almanac pending", "Gazette Chunk Never Requested"),
				NoJSErrors(1),
				Requested(2, []string{"/main.js", "/chunk.almanac."}, []string{"chunk.gazette."}),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// webpack 5, ESM output (output.module): chunks load through import()
			// with literal specifiers.
			Name:    "built-webpack-esm-chunks",
			Host:    builtHost("webpack-esm"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				RenderPresence(0, []string{"Webpack Almanac Shell", "Webpack Split Chunk Loaded"},
					1, []string{"Webpack Almanac Shell", "Webpack Split Chunk Loaded"}),
				NotRecall(1, "almanac pending", "Gazette Chunk Never Requested"),
				NoJSErrors(1),
				Requested(2, []string{"/main.mjs", "/chunk.almanac."}, []string{"chunk.gazette."}),
				NoFailedRequests(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// webpack 5.88 ESM output, whose runtime loads chunks with the
			// non-literal import("./" + file). A bundler that expands that pattern
			// against the filesystem read local files into the page (fixed by
			// loading modules through the registry, ADR 0018); this is that shape,
			// from a real build, resolving against the module's own URL.
			Name:    "built-webpack-esm-nonliteral-import",
			Host:    builtHost("webpack-esm-concat"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				RenderPresence(0, []string{"Webpack Almanac Shell", "Webpack Split Chunk Loaded"},
					1, []string{"Webpack Almanac Shell", "Webpack Split Chunk Loaded"}),
				NotRecall(1, "almanac pending", "Gazette Chunk Never Requested"),
				NoJSErrors(1),
				Requested(2, []string{"/main.mjs", "/chunk.almanac."}, []string{"chunk.gazette."}),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Next.js App Router, static export. The page is prerendered; the
			// client reads the Flight payload from an inline ReadableStream and
			// hydrates. "awaiting hydration" becoming "hydrated" is a useEffect in
			// a client component, so it only changes if hydration really ran.
			Name:    "built-next-app-router-hydration",
			Host:    builtHost("next-app"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "Next App Router Granary", "Suspense boundary resolved with the granary figures", "awaiting hydration"),
				RenderPresence(0, []string{"Next client component hydrated"},
					1, []string{"Next client component hydrated", "Next App Router Granary", "Suspense boundary resolved with the granary figures"}),
				NotRecall(1, "awaiting hydration", "granary pending"),
				NoJSErrors(1),
				RenderedNotDiscarded(1),
				Requested(2, []string{"/_next/static/chunks/"}, nil),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// The same export driven in a live session: a click handled by the
			// hydrated client component, then a <Link> click that the App Router
			// turns into a client-side navigation fed by the ledger page's RSC
			// payload — no document load.
			Name:    "built-next-app-router-interact-and-navigate",
			Host:    builtHost("next-app"),
			Browser: builtJS(),
			Steps:   session("nx", "/", click("#tally"), current, click("main a"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				NoJSErrors(2),
				Recall(3, "Tally count is 1", "Next client component hydrated"),
				InteractChanged(4, true),
				NoJSErrors(4),
				Recall(5, "Next Ledger Second Page", "Back to the granary"),
				NotRecall(5, "Next App Router Granary"),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Astro static build with two Preact islands: client:load hydrates at
			// once, client:visible when an IntersectionObserver reports it.
			Name:    "built-astro-islands-hydration",
			Host:    builtHost("astro"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "Astro Pottery Islands", "island awaiting hydration"),
				RenderPresence(0, []string{"Load island hydrated", "Visible island hydrated"},
					1, []string{"Load island hydrated", "Visible island hydrated", "Astro Pottery Islands"}),
				NotRecall(1, "island awaiting hydration"),
				NoJSErrors(1),
				RenderedNotDiscarded(1),
				Requested(2, []string{"/_astro/Kiln.", "/_astro/client."}, nil),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Each island is its own Preact root with its own state.
			Name:    "built-astro-islands-interact",
			Host:    builtHost("astro"),
			Browser: builtJS(),
			Steps:   session("as", "/", click("#load-island .fire"), click("#load-island .fire"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				InteractChanged(3, true),
				NoJSErrors(3),
				Recall(4, "Load firings: 2", "Visible firings: 0"),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Nuxt static generation. The figures are resolved at build time and
			// reach the client in the Nuxt payload; the template formats one with
			// compact notation, which the client must reproduce ("61.1K", not
			// "61,110") or Vue reports a hydration mismatch.
			Name:    "built-nuxt-payload-hydration",
			Host:    builtHost("nuxt"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "Nuxt Apiary Census", "Colony strength 61.1K bees", "awaiting hydration"),
				RenderPresence(0, []string{"Nuxt page hydrated"},
					1, []string{"Nuxt page hydrated", "Colony strength 61.1K bees", "Payload Keeper Ottoline"}),
				NotRecall(1, "61,110", "awaiting hydration"),
				NoJSErrors(1),
				RenderedNotDiscarded(1),
				Requested(2, []string{"/_nuxt/"}, nil),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name:    "built-nuxt-interact",
			Host:    builtHost("nuxt"),
			Browser: builtJS(),
			Steps:   session("nu", "/", click("#visit"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				NoJSErrors(2),
				Recall(3, "Hive visits: 1", "Nuxt page hydrated"),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// SvelteKit (Svelte 5 runes) with the static adapter: prerendered
			// markup hydrated through Svelte 5's comment anchors. The click updates
			// $state and a $derived value.
			Name:    "built-sveltekit-runes-hydration",
			Host:    builtHost("sveltekit"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "SvelteKit Rope Walk", "awaiting hydration"),
				RenderPresence(0, []string{"Svelte page hydrated"}, 1, []string{"Svelte page hydrated", "SvelteKit Rope Walk"}),
				NotRecall(1, "awaiting hydration"),
				NoJSErrors(1),
				RenderedNotDiscarded(1),
				Requested(2, []string{"/_app/immutable/entry/start.", "/_app/immutable/entry/app."}, []string{"version.json"}),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name:    "built-sveltekit-interact",
			Host:    builtHost("sveltekit"),
			Browser: builtJS(),
			Steps:   session("sk", "/", click("#tie"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				Recall(3, "Knots tied: 1, fathoms paid out: 2", "Svelte page hydrated"),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name:    "built-sveltekit-click-without-script-error",
			Host:    builtHost("sveltekit"),
			Browser: builtJS(),
			Steps:   session("se", "/", click("#tie")),
			Scorers: []Scorer{
				InteractChanged(2, true),
				NoJSErrors(2),
			},
			Floor: 0.99,
		},
		{
			// The same app with SvelteKit's default configuration, which checks
			// /_app/version.json from a self-rearming one-hour setTimeout. A
			// browser makes no such request while the page loads. The budget here
			// is 1s so the case stays quick; the storm scales with the budget.
			Name:    "built-sveltekit-default-version-poll",
			Host:    builtHost("sveltekit-poll"),
			Browser: []browser.Option{browser.WithJS(time.Second), browser.WithJSAllowPrivate(true)},
			Steps:   []Step{read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "Svelte page hydrated"),
				RenderedNotDiscarded(0),
				Requested(1, []string{"/_app/immutable/entry/start."}, []string{"version.json"}),
			},
			Floor: 0.99,
		},
		{
			// Angular with build-time prerendering and client hydration
			// (provideClientHydration). afterNextRender only runs in the browser.
			Name:    "built-angular-prerender-hydration",
			Host:    builtHost("angular"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "Angular Lighthouse Log", "awaiting hydration"),
				RenderPresence(0, []string{"Angular app hydrated"}, 1, []string{"Angular app hydrated", "Angular Lighthouse Log"}),
				NotRecall(1, "awaiting hydration"),
				NoJSErrors(1),
				RenderedNotDiscarded(1),
				Requested(2, []string{"/main-"}, nil),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name:    "built-angular-interact",
			Host:    builtHost("angular"),
			Browser: builtJS(),
			Steps:   session("ng", "/", click("#sweep"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				NoJSErrors(2),
				Recall(3, "Lamp sweeps: 1", "Angular app hydrated"),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Solid rendered to a string at build time and hydrated by
			// solid-js/web, with no meta-framework in between.
			Name:    "built-solid-ssr-hydration",
			Host:    builtHost("solid"),
			Browser: builtJS(),
			Steps:   []Step{read("/", false), read("/", true), requests("/")},
			Scorers: []Scorer{
				Recall(0, "Solid Belfry Register", "awaiting hydration"),
				RenderPresence(0, []string{"Solid page hydrated"}, 1, []string{"Solid page hydrated", "Solid Belfry Register"}),
				NotRecall(1, "awaiting hydration"),
				NoJSErrors(1),
				RenderedNotDiscarded(1),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name:    "built-solid-interact",
			Host:    builtHost("solid"),
			Browser: builtJS(),
			Steps:   session("so", "/", click("#ring"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				NoJSErrors(2),
				Recall(3, "Bells rung: 1", "Solid page hydrated"),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// The hand-written platform page: one script line per browser
			// behaviour that a corpus page was found to depend on. Each line is
			// asserted with the value a browser produces.
			Name:    "platform-script-behaviours",
			Host:    platformHost(),
			Browser: builtJS(),
			Steps:   []Step{read("/platform/", false), read("/platform/", true), requests("/platform/")},
			Scorers: []Scorer{
				RenderPresence(0, []string{"script-order:", "Template row alpha"}, 1, []string{
					// defer and module scripts run after every inline script, in document order.
					"script-order: inline-head > inline-tail > defer > module",
					// a data: module script is decoded locally and runs.
					"data-module: data module ran",
					// <template> contents live in .content only, and clone into the page.
					"template-inert: 2 rows live, 0 template children, 1 in content",
					"Template row alpha", "Template row beta",
					// property writes reach the attributes extraction reads.
					"reflect-hidden: true / display: none;",
					"reflect-props: reflected title | noopener | reflected | email | true | you@example",
					// innerText skips concealed text when read and writes <br> for newlines.
					`innertext-get: "inner shown"`,
					"innertext-set: 1 br, 3 nodes",
					"first written line", "second written line",
					// per-tag interfaces.
					`interfaces: true true true false \[object HTMLTableElement]`,
					// the root element has the viewport's geometry.
					"viewport-root: 1280x720 client 1280x720 inner 1280x720",
					"xpath: 2 cloned, first cell Barley, 2 roast inputs",
					// select and radio state through form named access.
					"form-state: small 0 2 light 2",
				}),
				NoJSErrors(1),
				// the deferred script is a real request; the data: module is not.
				Requested(2, []string{"/platform/deferred.js"}, []string{"data:"}),
				NoFailedRequests(2),
				RequestsStayOnFixture(2),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Number and list formatting a page shows its reader (en-US).
			Name:    "platform-intl-numbers",
			Host:    platformHost(),
			Browser: builtJS(),
			Steps:   []Step{read("/platform/", true)},
			Scorers: []Scorer{
				Recall(0,
					"intl-number: 1,234,567.891",
					"intl-currency: $1,234.50",
					"intl-compact: 61.1K",
					"intl-percent: 25.6%",
					"number-locale: 9,876,543.21",
					"intl-plural: one two few other other two",
					"intl-list: oats, rye, and millet",
				),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// What extraction makes of the page's static structures, and of the
			// two paragraphs the script hid by property and by style (safe output
			// strips hidden content, as in production).
			Name:    "platform-extraction",
			Host:    platformHost(),
			Browser: builtJS(),
			Safe:    true,
			Steps:   []Step{read("/platform/", true)},
			Scorers: []Scorer{
				Recall(0,
					"▸ Collapsed granary ledger", "Ledger line inside the collapsed details element.",
					"| Grain | Bushels |", "| Barley | 412 |", "| Spelt | 97 |",
					"Northfield · Southfield",
					"Kept paragraph beside the removed ones",
				),
				NotRecall(0, "Removed by the hidden property", "Removed by a style display write", "inner concealed"),
				FramedUntrusted(0),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			// Event-handler attributes and properties, and a select changed
			// through interact, read back by page code through form named access.
			Name:    "platform-handlers-and-select",
			Host:    platformHost(),
			Browser: builtJS(),
			Steps: session("pf", "/platform/",
				click("#attr-btn"), click("#prop-btn"), change("#size", "large"), click("#summarize"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				InteractChanged(3, true),
				InteractChanged(4, true),
				InteractChanged(5, true),
				NoJSErrors(5),
				Recall(6,
					"attribute handler ran on attr-btn for click",
					"property handler ran on prop-btn for click",
					"Order summary: large sack, light roast, 5 controls, option 1",
				),
			},
			Floor:    0.99,
			MustPass: true,
		},
		{
			Name:    "platform-interact-checks-radio-and-checkbox",
			Host:    platformHost(),
			Browser: builtJS(),
			Steps:   session("pr", "/platform/", click("#roast-dark"), click("#rush"), click("#summarize"), current),
			Scorers: []Scorer{
				InteractChanged(2, true),
				InteractChanged(3, true),
				Recall(5, "dark roast", "rush true"),
			},
			Floor: 0.99,
		},
		{
			Name:    "platform-document-forms-named-access",
			Host:    platformHost(),
			Browser: builtJS(),
			Steps:   []Step{read("/platform/", true)},
			Scorers: []Scorer{
				Recall(0, "document-forms: 1 form, by name true, by index true, on document true"),
			},
			Floor: 0.99,
		},
		{
			Name:    "platform-intl-date-time",
			Host:    platformHost(),
			Browser: builtJS(),
			Steps:   []Step{read("/platform/", true)},
			Scorers: []Scorer{
				Recall(0,
					// These two come out right today only where the host's zone
					// puts 13:37 UTC on the same calendar day; they belong with
					// the rest until timeZone is honoured.
					"intl-date-long: March 5, 2024",
					"intl-date-parts: Tue, Mar 5, 2024",
					"intl-time: 1:37 PM",
					"locale-time: 1:37 PM",
					"locale-date: March 5, 2024",
					"intl-time-zone: 8:37 AM EST",
				),
			},
			Floor: 0.99,
		},
		{
			Name:    "platform-intl-relative-unit-collator",
			Host:    platformHost(),
			Browser: builtJS(),
			Steps:   []Step{read("/platform/", true)},
			Scorers: []Scorer{
				Recall(0,
					"intl-relative: yesterday / in 3 hours / 2 weeks ago",
					"intl-unit: 88 km/h",
					"intl-collator: aäbC",
				),
			},
			Floor: 0.99,
		},
		{
			Name:  "platform-static-template-contents",
			Host:  platformHost(),
			Steps: []Step{read("/platform/", false)},
			Scorers: []Scorer{
				Recall(0, "Platform Behaviours Workbench", "## Template rows"),
				NotRecall(0, "Template row"),
			},
			Floor: 0.99,
			KnownGap: "a static (render=false) read emits a <template>'s contents as page text (\"Template row\"): template contents are inert and never " +
				"rendered by a browser. The rendered path seals them; the no-JS path does not.",
		},
		{
			// Preact with hooks and htm, all from UMD bundles and no build step —
			// how a small site ships it.
			Name: "preact-hooks-htm-render",
			Host: frameworkHost(preactHooksHtmApp, map[string]string{
				"/fw/preact.umd.js":       "../testdata/frameworks/preact.umd.js",
				"/fw/preact.hooks.umd.js": "../testdata/frameworks/preact.hooks.umd.js",
				"/fw/htm.umd.js":          "../testdata/frameworks/htm.umd.js",
			}),
			Browser: builtJS(),
			Steps: append([]Step{read("/", false), read("/", true)},
				session("ph", "/", click("#weave"), click("#weave"), current)...),
			Scorers: []Scorer{
				RenderPresence(0, []string{"Preact Hooks Loom", "Hooks effect ran"}, 1, []string{"Preact Hooks Loom", "Hooks effect ran", "Picks woven: 0"}),
				// Preact appends beside the placeholder text already in #root, in a
				// browser too, so "loading shell" staying is correct.
				NotRecall(1, "effect pending"),
				NoJSErrors(1),
				InteractChanged(4, true),
				InteractChanged(5, true),
				NoJSErrors(5),
				Recall(6, "Picks woven: 2", "Hooks effect ran"),
			},
			Floor:    0.99,
			MustPass: true,
		},
	}
}
