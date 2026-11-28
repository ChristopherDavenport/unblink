# ADR 0018: a per-render module registry — share one instance per URL across import()s

Date: 2026-07-06
Status: accepted

## Context

goja has no ES-module support (no module records, no `import()`), so unblink drives
page modules through esbuild. The original dynamic-import path (`bundleDynamicChunk`)
handled each `import(spec)` by running esbuild with `Bundle: true, Format: IIFE`,
inlining the specifier's **entire static graph** into one self-contained bundle, then
running it and reading back its namespace.

That is correct for a single `import()` in isolation, but it has no cross-import
identity: two separate `import()` calls that share a dependency each inline their own
copy of it. A browser doesn't — its module map instantiates each URL once and hands
every importer the same live namespace.

The duplication is not cosmetic when a shared module holds process-global mutable
state. An Astro `client:only` island imports its **component** chunk and its
**renderer** chunk as two separate `import()`s, and both statically import React
(`a.ClefJfM7.mjs`). Under per-import bundling, React was instantiated twice. React's
hooks dispatcher is a singleton on module-level state: ReactDOM (copy A) sets its
dispatcher during render while the component's hooks read copy B's (still null),
throwing `TypeError: Cannot read property 'useMemoCache' of null`. The component
crashed mid-render, the island stayed empty, and the page fell back to its static SSR
shell. The same hazard applies to any cross-chunk singleton (stores, event buses) and
to `instanceof` identity across chunk boundaries.

## Decision

Give dynamic `import()` browser module-map semantics with a **per-render module
registry keyed by resolved URL** (`internal/js/moduleregistry.go`), replacing
`bundleDynamicChunk`/`runDynamicChunk`. Each module URL is fetched, esbuild-
transformed to a self-contained CommonJS unit, and **evaluated exactly once**; its
live namespace (`module.exports`) is shared with every importer.

Loading is two-phase, which keeps it cycle-safe and keeps goja's evaluation
single-threaded on the loop goroutine:

- **Instantiate (off-loop).** `instantiateGraph` traverses the graph reachable from
  the entry, calling `instantiateSelf` per URL: fetch the source (reusing the asset
  cache and any in-flight prefetch warm — `fetchModuleSource`), esbuild-transform it
  (`Format: CommonJS`, dynamic-import lowered), and record its static `require()`
  deps. Dedup is by registry membership; a per-URL `selfDone` channel lets concurrent
  callers wait. Crucially, transforming a module never waits on its dependencies, so
  an import cycle cannot deadlock this phase.
- **Evaluate (on-loop).** `evaluate` runs each module body once, in post-order, by
  wrapping the CJS unit as `(function(require, module, exports){…})` and calling it
  with a `require` bound to the registry (resolved against the module's URL). A
  static `require()` returns an already-evaluated instance; a module currently being
  evaluated (a cycle) is handed its in-progress exports, matching CommonJS. A nested
  dynamic `import()` re-enters the async loader (`__unblinkImport`), so it too
  shares instances.

Load-bearing details:

- **CommonJS, not IIFE.** Per-file CJS is what exposes module boundaries so instances
  can be shared; IIFE bundling fundamentally hides them. esbuild emits static imports
  as `require("spec")` (intercepted by the registry) and lowers dynamic imports to a
  distinct `Promise.resolve().then(() => __toESM(require(…)))` form, which is
  rewritten to the async `__unblinkImport` loader — kept separate from static
  requires so a dynamic import stays async while a static import is a synchronous
  registry lookup.
- **The ADR 0004 settle bracket is preserved.** `importPromise` still brackets the
  off-loop instantiate with `pending`/keepalive, and resolves/rejects inside the
  on-loop evaluate, so the provable-idle settle waits for the whole graph.
- **Concurrency.** `evaluate` runs only on the loop goroutine, but a *different*
  concurrent `import()` may still be populating the registry off-loop, so the map
  read is mutex-guarded (`lookup`). Instance fields written off-loop are published to
  the loop via `selfDone`/`WaitGroup` happens-before. Verified under `-race`.

## Consequences

- A dependency shared across `import()` calls is instantiated once, so cross-chunk
  singletons and `instanceof` identity work. poe.ninja (Astro + React 19.2) hydrates
  its real content instead of the empty shell.
- **Scope: the dynamic `import()` path only** (superseded 2026-11-27 — see the
  amendment below). Static `<script type=module>`
  (`runModules`) still bundles per entry (esbuild dedups *within* one entry's graph,
  which is sufficient there). A module loaded by **both** a static module-script and a
  dynamic `import()` could therefore still be duplicated across those two paths. This
  is not hit in practice — island frameworks load via `import()` — and unifying the
  static path onto the registry is deferred.
- **CommonJS interop, not full ESM.** Live bindings degrade to CJS copy/getter
  semantics (esbuild's `__toESM`/`__export` shims), and cyclic imports observe
  partial exports. This matches how Vite/Rollup-built chunks (already CJS-interop
  shaped) behave; exotic ESM-only live-binding cycles are a known limitation.
- Per-module fetch+transform replaces one whole-graph bundle. Shared chunks are now
  fetched/transformed once instead of once per importing bundle (poe.ninja re-fetched
  React ~4× before), so heavy code-split graphs do *less* redundant work; the asset
  and compiled-program caches still amortize across renders.

## Amendment (2026-11-27): static module scripts load through the registry too

**The scope limit above is withdrawn.** `runModules` no longer calls `esbuild.Build`;
every `<script type=module>` entry, external or inline, and its static graph load
through this registry. `esbuild.Build` is not called anywhere on page-supplied code.

### Why: the bundler read the local filesystem

esbuild's bundler treats a dynamic import (or `require`) whose argument is a
non-literal relative path as a **glob import**: ``import(`./${n}.json`)`` becomes the
pattern `./**/*.json`, `import("./" + x)` becomes `./**/*`, and `../` patterns climb.
It expands the pattern against the real filesystem and inlines every match. That
expansion never reaches a plugin's `OnResolve`/`OnLoad`, so the network-only resolver
plugin did not contain it. The root was the process working directory for a module
fetched from the network and `/` for an inline module (`ResolveDir: "/"`).

Untrusted page JS could therefore read any file esbuild has a default loader for
(`.js`, `.mjs`, `.ts`, `.json`, `.css`, `.txt`, …) under the server's working
directory or a parent, and send its contents to any origin with `fetch`. The inline
form walked the whole filesystem, which is also a denial of service (minutes of CPU,
gigabytes of memory). Real pages tripped it by accident: webpack's ESM chunk loader is
`import("./" + chunkName)`, which made esbuild try to bundle whatever sat in the
working directory and drop the page's entry when it met a file type it could not load.

Moving `ResolveDir` to an empty directory does not fix it (`../` escapes), and there is
no option to turn glob imports off. `esbuild.Transform` compiles one source string and
has no resolver and no filesystem access, so the registry — which already used it —
is the fix. `TestModuleGlobImportCannotReadLocalFS` is the regression test;
`TestNoEsbuildBundlerOnPageCode` is a tripwire against reintroducing `Build`.

### What changed in the registry

- **Entries.** An external module script is `instantiateGraph(url)` + `evaluate(url)`.
  An inline module has no URL: it gets an unregistered instance (`instantiateInline`)
  whose referrer is the document base, so it can be neither imported nor deduped.
- **Still synchronous.** `runModules` blocks the loop goroutine for the instantiate
  phase (the fetches run concurrently off-loop), as the bundle build did. Module
  scripts still run in document order, after classic scripts and before
  `DOMContentLoaded`, and no async primitive is added (ADR 0004).
- **One instance across static and dynamic.** The "one React" property now holds for a
  module reached by a static entry and by `import()`, and for a module shared by two
  module scripts (each entry used to bundle its own copy).
- **Lazy means lazy.** A literal `import("./chunk.js")` is a call to the loader, not a
  bundle edge, so the chunk and its static graph are fetched when the page calls it.
  The bundler fetched all of them at load.
- **Failure is local.** A lazy chunk that fails to load rejects that `import()`
  promise. A static dependency that fails throws at the importer's `require`, failing
  that entry with a recorded error; other entries run. Under the bundler any failed
  load, lazy or not, dropped the whole entry.
- **Cycles see the live namespace.** A cyclic importer now receives the module's live
  `module.exports` (which esbuild's output assigns before the module's own requires
  run) instead of a placeholder object that was never filled.
- **A dependency's evaluation error is rethrown as itself** to each importer, instead
  of being wrapped in a new `TypeError`.
- **`import.meta`.** `import.meta.url` is the module URL (the document base for an
  inline module) and `import.meta.resolve()` applies the import map. Both were
  `undefined` on either path before. esbuild's `const import_meta = {};` is rebound to
  a wrapper parameter by `importMetaDeclRE`.
- **Non-literal `import()`.** The lowered call is redirected by rewriting only its
  prefix (`dynImportPrefixRE`), so any argument expression survives, and it resolves
  against the importing module's URL. In a bundled static module it used the document
  base.
- **JSON modules.** A URL ending in `.json` loads as a JSON module on every path (the
  bundle loader did this; the dynamic path did not). The body goes through
  `JSON.parse` from a string literal and is never spliced into the wrapper as code.
- **SRI.** An integrity-checked module entry is registered from the verified bytes
  (`instantiateFrom`), so the loader cannot fetch the URL a second time and run
  different bytes when the asset cache is off.
- **Caching.** The per-entry bundle cache is gone. Each module's transform is cached
  in the asset cache under a hash of its source, independent of the page URL and the
  import map, so repeat renders still skip esbuild.

### Differences from the bundle path that a page could observe

- An external module named by two script tags evaluates once (module-map behaviour);
  it used to run twice.
- Modules are no longer scope-hoisted into one function. Each is a CommonJS unit with
  esbuild's interop shims, as dynamic chunks already were: the "CommonJS interop, not
  full ESM" consequence above now applies to static graphs too. As before, esbuild
  does not add a strict-mode directive, so module code is not forced into strict mode.
- A failed static dependency no longer prevents the modules that precede it in
  evaluation order from running: evaluation is post-order and stops at the failing
  `require`. A browser evaluates nothing in that graph.
- Top-level `await` is unchanged: a transform error for that module (it was a bundle
  error for the whole entry).
- Syntax lowering is unchanged: the same esbuild ES2017 target, now applied per module.
- CSP and SRI are unchanged in scope: they gate the module entry, and its static
  dependencies inherit the decision.

### Cost

One transform and one function wrapper per module replace one bundle. Measured on the
new `BenchmarkRenderModuleGraph*` (an entry importing 100 chunks, 201 modules): about
9 ms per render with the asset cache warm against about 3 ms for the bundle, and about
37 ms cold against about 10 ms. A single large module (`BenchmarkRenderLitModule*`)
shows no measurable difference. In exchange a code-split page no longer downloads and compiles the lazy
chunks it never runs.
