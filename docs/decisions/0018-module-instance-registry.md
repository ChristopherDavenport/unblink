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
- **Scope: the dynamic `import()` path only.** Static `<script type=module>`
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
