# Vendored framework bundles (test fixtures)

Pinned, minified production builds of mainstream front-end frameworks, used by the
`internal/js` and `eval` suites to prove that unblink's JS engine actually renders
real React / Vue / Preact / Lit apps (not hand-written stand-ins). They are served
to the engine through the in-memory test transport / httptest fixtures — never
fetched at test time.

Upgrades must be deliberate: bump the version here and re-pin.

| File                          | Project   | Version  | License    | Source (unpkg/jsDelivr)                                          |
| ----------------------------- | --------- | -------- | ---------- | ---------------------------------------------------------------- |
| `react.production.min.js`     | React     | 18.3.1   | MIT        | unpkg.com/react@18.3.1/umd/react.production.min.js               |
| `react-dom.production.min.js` | ReactDOM  | 18.3.1   | MIT        | unpkg.com/react-dom@18.3.1/umd/react-dom.production.min.js       |
| `vue.global.prod.js`          | Vue       | 3.4.38   | MIT        | unpkg.com/vue@3.4.38/dist/vue.global.prod.js                     |
| `preact.umd.js`               | Preact    | 10.23.1  | MIT        | unpkg.com/preact@10.23.1/dist/preact.umd.js                      |
| `preact.hooks.umd.js`         | Preact Hooks | 10.23.1 | MIT       | unpkg.com/preact@10.23.1/hooks/dist/hooks.umd.js                 |
| `htm.umd.js`                  | htm       | 3.1.1    | Apache-2.0 | unpkg.com/htm@3.1.1/dist/htm.umd.js                              |
| `lit-all.min.js`              | Lit       | 3.2.0    | BSD-3-Clause | cdn.jsdelivr.net/gh/lit/dist@3.2.0/all/lit-all.min.js          |
| `svelte-app.iife.js`          | Svelte    | 4.2.20   | MIT        | compiled locally (see below)                                     |

React / Vue / Preact load as classic UMD `<script src>` (the goja path, no esbuild).
Lit is an ES module bundle, so it exercises the esbuild → ES2017 → goja path.

`svelte-app.iife.js` is the only *compiled* fixture (Svelte has no CDN runtime
bundle — a component compiles to JS). It is a minimal `App.svelte` mounting an
`<article>` with a stable "Svelte Rendered Title" marker, bundled to a
self-contained IIFE. To rebuild:

```sh
npm install svelte@4 esbuild esbuild-svelte
# App.svelte: <script>let title='Svelte Rendered Title'; let body='…';</script>
#             <article><h1>{title}</h1><p>{body}</p></article>
# entry.js:   import App from './App.svelte'; new App({ target: document.getElementById('app') });
esbuild entry.js --bundle --format=iife --minify --outfile=svelte-app.iife.js \
  --plugin:esbuild-svelte   # compilerOptions: { css: 'injected' }
```

Svelte 4 (not 5): the v4 compiled runtime uses conventional
createElement/appendChild/setData DOM manipulation, which the flat-DOM engine
renders faithfully; v5's comment-anchor hydration model is out of scope.

Each project's full license text is available from its repository; these are
unmodified redistributions of the published artifacts for testing only.
