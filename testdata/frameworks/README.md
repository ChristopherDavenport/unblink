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
| `astro-island-runtime.js`     | Astro     | 6.3 – 6.4 | MIT       | `astro/dist/runtime/server/astro-island.prebuilt.js` (the string it exports) |

`astro-island-runtime.js` is the `<astro-island>` custom-element runtime Astro
inlines into every page with an island. The file is byte-identical to the
prebuilt runtime published in astro 6.3.8 and 6.4.8 (checked against unpkg on
2026-11-28; earlier and later minors differ, and Astro 7 no longer ships a
prebuilt file). `internal/js/astro_island_test.go` loads it with hand-written
renderer and component stubs; the built `astro` fixture below is the end-to-end
version, with Astro 7's own runtime.

`preact.hooks.umd.js` and `htm.umd.js` are used with `preact.umd.js` by the
eval case `preact-hooks-htm-render` (a no-build Preact page).

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

## Fixtures built from source (`src/` → `built/`)

The single files above prove a framework can render one article. They cannot
catch what real sites do: module graphs with lazy chunks, a bundler's
chunk-loading runtime, history-mode routing, data fetched after mount, and
server-rendered markup hydrated in place. `built/` holds small applications
built with the real toolchains — unmodified production output, hashed chunk
names included — and the eval gate (`eval/built_cases.go`) serves each one as a
static site.

- `src/<name>/` is the hand-written application: a few source files, a
  `package.json` with exact versions, and its `package-lock.json`. No
  `node_modules` or intermediate output is tracked (`src/.gitignore`).
- `built/<name>/` is the committed build output, plus
  `THIRD-PARTY-LICENSES.txt`: the name, version, licence and full licence text
  of every npm package whose code is in that build.
- `make eval-fixtures` (`scripts/eval-fixtures.sh`) rebuilds them: `npm ci` and
  `npm run build` per fixture, then `src/_shared/licenses.mjs`. It needs network
  and Node, and is local-only like `make wpt-sync` — CI and `make eval` only
  read `built/`. `FIXTURES="nuxt astro"` narrows it.

Built on 2026-11-28 with Node 25.0.0 and npm 9.2.0 (Angular under Node 24.22.3,
see below). Two consecutive clean rebuilds were byte-identical.

| `built/` directory   | Toolchain and versions                                             | What it is |
| -------------------- | ------------------------------------------------------------------ | ---------- |
| `vite-react`         | Vite 8.4.0, @vitejs/plugin-react 6.3.0, React 19.3.1                | Client-only SPA; one lazily imported chunk that renders, one that never does |
| `vite-vue`           | Vite 8.4.0, @vitejs/plugin-vue 6.0.11, Vue 3.5.46, vue-router 5.5.3 | History-mode router; a code-split route that fetches JSON after mount |
| `webpack-classic`    | webpack 5.113.0                                                    | `<script defer>` entry, `webpackChunk` JSONP runtime, chunk loaded by script injection |
| `webpack-esm`        | webpack 5.113.0 (`output.module`)                                  | ESM chunks loaded with literal `import()` |
| `webpack-esm-concat` | webpack 5.88.2 (`output.module`), pinned on purpose                | ESM runtime with the non-literal `import("./" + file)` large sites still ship |
| `next-app`           | Next.js 16.4.6 (Turbopack), React 19.3.1                           | App Router static export: Flight payload, Suspense, a client component, `<Link>` |
| `astro`              | Astro 7.4.13, @astrojs/preact 6.1.6, Preact 11.2.0                 | Static page with a `client:load` and a `client:visible` island |
| `nuxt`               | Nuxt 4.7.0, Vue 3.5.46                                             | `nuxt generate`: payload, hydration, compact-notation `Intl.NumberFormat` |
| `sveltekit`          | SvelteKit 3.4.1, Svelte 5.59.2, adapter-static 4.0.1, Vite 8.4.0    | Prerendered page with runes, hydrated; version poll off |
| `sveltekit-poll`     | same sources, SvelteKit's default `version.pollInterval`           | The hourly self-rearming version check (a known-gap case) |
| `angular`            | Angular 22.3.0 (`@angular/build`, `@angular/ssr`)                   | One prerendered route hydrated with `provideClientHydration` |
| `solid`              | solid-js 1.9.20, vite-plugin-solid 2.11.17, Vite 8.4.0              | `renderToString` at build time, `hydrate` in the browser |

All bundled packages are MIT, ISC, BSD, 0BSD or Apache-2.0; `licenses.mjs` fails
the build on anything else (ADR 0010: no copyleft bytes in the tree).

Notes on individual builds:

- **Hashed names are part of the fixture.** Cases match chunks by prefix
  (`/assets/Orchard-`), never by hash, so a version bump does not edit Go code.
- **`webpack-esm-concat` pins webpack 5.88.2** because current webpack emits
  literal chunk specifiers. The non-literal form is the one a
  filesystem-globbing bundler expands (the v0.16–v0.25 local-file read), and
  sites built with older webpack still serve it.
- **`nuxt`** sets a fixed `buildId`, and `normalize.mjs` replaces the prerender
  timestamp, so a rebuild does not churn four files.
- **`next-app`** sets a fixed build id. Its output includes the `nomodule`
  polyfill chunk and the 404 pages a real export has.
- **`angular`** builds under Node 24 (`npx -p node@24.22.3`): Angular 22's
  prerender worker fails on Node 25 with "module is not linked".
- **Not built:** a Next.js Pages Router export (another ~300 KB of React for
  one more hydration path), SolidStart and Qwik (meta-framework toolchains much
  larger than the plain Solid SSR build, which covers Solid's hydration).

`eval/corpus/platform/` is the companion hand-written page (no framework, not
vendored): one script line per browser behaviour a real page was found to
depend on, asserted with the values Chromium produces.
