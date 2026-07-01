# Spec: downlevel classic scripts through esbuild + add a `crypto` global

**Status:** PARTIALLY IMPLEMENTED / downleveling DROPPED (Phase 15). The `crypto`
global shipped as specified. The classic-script downleveling was investigated and
**deliberately not adopted** — see "Outcome" below. The downleveling sections that
follow are retained for historical context; do **not** implement them without
re-reading the Outcome.
**Audience:** a fresh Claude Code session picking this up cold — anchors and
sketches below are meant to be executable with minimal re-discovery.

## Outcome (Phase 15, branch `interact-gesture-accuracy`)

**Shipped:** the `crypto` shim (`getRandomValues` + `randomUUID`) in `preludeJS`
(`internal/js/globals.go`), covered by `internal/js/crypto_test.go`
(`TestCryptoGlobalWorks`, `TestCryptoGlobalInLiveContext`). goja has no `crypto` at
all; the shim is `Math.random`-backed and leaves `crypto.subtle` undefined.

**Dropped — classic-script downleveling.** Two findings on the pinned goja
(`v0.0.0-20260629`) killed the case for it:

1. **goja already parses the modern syntax this spec blamed.** Direct probes show
   private class fields/methods, static blocks, `??=`/`&&=`/`||=`, `?.`, `??`, numeric
   separators, and `**` all run natively with no error. The "SyntaxError: Unexpected
   reserved word (365 more)" the original diagnosis saw on mobalytics predated a goja
   bump that added this support; the parsing gap is effectively closed by the
   dependency upgrade. What goja still rejects is exotic and rare in *classic* bundles:
   async generators, top-level await, decorators, `using`, import attributes.
2. **esbuild's ES2017 downleveling would REGRESS private-field code via a goja
   `WeakMap` bug.** goja's `WeakMap` does not overwrite on a repeated `set`
   (`m.set(o,0); m.set(o,7); m.get(o)` → `0`; plain `Map` is fine). esbuild lowers
   private fields to WeakMap-backed helpers, so downleveling `class C { #n=0;
   bump(){ this.#n+=7; return this.#n } }` makes `bump()` return `0`. A safe
   fallback design exists (compile raw first, esbuild-retry only on parse error, with
   esbuild `Supported`-overrides marking private fields native so they dodge the
   WeakMap path), but given (1) its value is marginal and it was not adopted.

The canary `TestGojaParsesModernSyntaxNatively` (`internal/js/crypto_test.go`) fails if
a future goja bump regresses native parsing — the trigger to revisit this.

**Note for maintainers:** `internal/js/modules.go` `runModules` *unconditionally*
downlevels ES modules to ES2017 via `esbuild.Build`, so it carries the same latent
WeakMap-corruption bug for any private-field-using module. Current framework tests
don't exercise it. If a real module regresses, add the `Supported`-overrides there.

## Context — why

Phase 14 made `interact` emulate a real press/hover/focus gesture, so
press/pointer-based widgets (react-aria/Radix `usePress`) now activate — proven by
`internal/js/gesture_test.go` and the press/hover E2E tests in
`internal/browser/browser_test.go`. But re-testing the original real page
(`https://mobalytics.gg/poe-2/builds/plants-lich-deadrabb1t`, a react-aria tabs
SPA) still returned `interact … changed:false`: the tab never switches because
**the site's React never hydrates in goja**, so no press handlers are ever
attached. No event-layer fix can help a page whose JS didn't run.

`--log-level debug` ("js: render diagnostics") on that page showed
`framework=""` and 6 uncaught errors, of which two are the load-bearing blockers:

```
SyntaxError: script-8.js Unexpected reserved word (and 365 more errors)   # goja can't parse the bundle
Error: crypto.getRandomValues() not supported ... (uuid)                   # missing standard global
```

Root causes, both confirmed in code:

1. **Classic `<script>` bundles skip esbuild.** ES *modules* are already
   downleveled: `runModules` bundles them with `esbuild.Build` at
   `Target: ES2017` (`internal/js/modules.go:71-84`) so goja can run them. But
   classic/inline scripts go straight to goja: `runScripts` fetches the source and
   calls `goja.Compile(...)` with **no transform** (`internal/js/scripts.go:88`).
   goja's parser rejects modern syntax that appears throughout minified bundles
   (private class fields `#x` — ES2022, static blocks, some logical-assignment,
   etc.), so the whole bundle fails to compile and is skipped
   (`scripts.go:88-92`).
2. **No `crypto` global at all.** `grep -rn getRandomValues internal/js` → nothing.
   `uuid`/`nanoid`/react-aria `useId` throw, breaking hydration.

## Goal / scope

- Downlevel **classic external + inline scripts** through esbuild **Transform**
  before goja compiles them — the same downleveling modules already get — so
  modern-syntax bundles parse and run.
- Add a `crypto` global: `getRandomValues(typedArray)` and `randomUUID()`.
- Iterate: re-run the debug diagnosis on real SPAs and add any further missing
  globals surfaced (this fixes the first two blockers, not necessarily 100%
  hydration).

## Non-goals

- Import resolution/bundling for classic scripts — that is modules' job. Use
  esbuild **Transform** (single file), not **Build**.
- Full Web Crypto (`crypto.subtle`), workers, layout, or broad browser-API
  coverage. Leave `crypto.subtle` undefined so libraries feature-detect and fall
  back rather than hit a half-working stub.
- Changing module handling (`modules.go`) beyond optionally extracting a shared
  helper.

## Confirmed anchors

- `internal/js/scripts.go:63` `runScripts` — the wiring seam. Source is in `src`
  (fetched external via `b.transport` at `:75`, or `scriptText(s)` inline), passed
  through `stripSourceMappingURL` (`:87`) then `goja.Compile("script-%d.js", src,
  false)` (`:88`). **Insert the transform between `:87` and `:88`.**
- `internal/js/modules.go:71-99` — the esbuild pattern + `Target: ES2017` +
  `recordError` diagnostics to mirror. `esbuild` is already a dependency
  (`github.com/evanw/esbuild v0.28.1`, imported as `esbuild "…/pkg/api"`).
- `internal/js/globals.go:223` `const preludeJS = ` … — a JS IIFE running after
  the Go globals and before page scripts (injected at `context.go:92` and
  `engine.go:205`). **Add `crypto` here** (it already installs `console`,
  storage, `defEvent`, `ETShim`, etc.).
- `internal/js/customelements.go:340` `recordError` — how uncaught/transform
  errors reach render diagnostics (surfaced by `--log-level debug`).
- goja version: `github.com/dop251/goja v0.0.0-20260629…` — supports most
  ES2017+ but **not** ES2022 private class fields; ES2017 is the safe transform
  target (matches modules).

## Implementation steps

### 1. esbuild Transform helper (new, in `scripts.go` or a shared spot)

```go
// downlevelClassic rewrites a classic script's source to syntax goja can parse
// (mirrors the ES2017 target modules already use). On any transform error it
// returns the original source unchanged, so this can never regress a script that
// goja already handled — goja.Compile still reports a real parse error after.
func downlevelClassic(src string) string {
    res := esbuild.Transform(src, esbuild.TransformOptions{
        Loader:   esbuild.LoaderJS,
        Target:   esbuild.ES2017,
        Format:   esbuild.FormatDefault, // keep top-level in global scope (no IIFE wrap)
        LogLevel: esbuild.LogLevelSilent,
        Sourcemap: esbuild.SourceMapNone,
    })
    if len(res.Errors) > 0 {
        return src // fall back; goja.Compile will surface a diagnostic if still bad
    }
    return string(res.Code)
}
```

Notes:
- `FormatDefault` (not `FormatIIFE`) preserves global-scope semantics: top-level
  `var`/`function` stay global properties, matching today's per-script
  `RunProgram`. (Top-level `let`/`const`/`class` already don't cross `RunString`
  boundaries in goja today — pre-existing, unchanged.)
- Consider a size/skip guard: if `src` is tiny or already ES5-ish the transform is
  cheap anyway; esbuild is Go-native and fast (modules already pay it). Optional:
  cache by content hash if profiling shows cost.

### 2. Wire into `runScripts` (`scripts.go:87-88`)

```go
src = stripSourceMappingURL(src)
src = downlevelClassic(src)                       // <-- new
prog, err := goja.Compile(fmt.Sprintf("script-%d.js", i), src, false)
```

Keep the existing per-script error isolation (`recordError` + `continue`) so one
bad script never blanks the page.

### 3. Add `crypto` to `preludeJS` (`globals.go`, inside the IIFE)

Prelude-only, no Go plumbing (assigning to a typed-array element auto-masks to its
byte width, so one loop covers Uint8/16/32 arrays):

```js
if (!window.crypto) window.crypto = {};
if (!window.crypto.getRandomValues) {
  window.crypto.getRandomValues = function (arr) {
    for (var i = 0; i < arr.length; i++) arr[i] = Math.floor(Math.random() * 4294967296);
    return arr;
  };
}
if (!window.crypto.randomUUID) {
  window.crypto.randomUUID = function () {
    var b = new Uint8Array(16); window.crypto.getRandomValues(b);
    b[6] = (b[6] & 0x0f) | 0x40; b[8] = (b[8] & 0x3f) | 0x80;
    var h = []; for (var i = 0; i < 16; i++) h.push((b[i] + 0x100).toString(16).slice(1));
    return h.slice(0,4).join('') + '-' + h.slice(4,6).join('') + '-' +
           h.slice(6,8).join('') + '-' + h.slice(8,10).join('') + '-' + h.slice(10,16).join('');
  };
}
```

**Decision — randomness source:** `Math.random`-backed is chosen for simplicity
(no Go↔typed-array marshaling). It is *not* cryptographic, but the consumers here
(uuid/nanoid/`useId`) only need collision-resistant IDs, and unblink is a
read-only content extractor, not a security context. If real entropy is ever
wanted, back `getRandomValues` with a Go function over `crypto/rand` and inject it
as a Go global instead. Leave `crypto.subtle` **undefined** (libraries
feature-detect it).

### 4. Iterative re-diagnosis (do this, it's part of the work)

After 1–3, rebuild and re-run the debug diagnosis (see Verification) on the
Mobalytics page **and** 1–2 other react-aria/Next SPAs. Expect further missing
globals to surface (the page also logged `TypeError: Cannot read property
'document' of undefined` and a `render timeout`). For each remaining error, add
the missing global (prelude or Go) or note it. Stop when the target page hydrates
(tab switch works) or the remaining errors are clearly out of scope (workers,
WebGL, etc.) — document whatever is left.

## Design decisions / tradeoffs

- **Transform vs Build:** Transform (no import graph) for classic scripts; Build
  stays for modules. Same `Target: ES2017` for consistency.
- **Fallback-to-original on transform error** guarantees no regression vs today —
  a script goja already ran keeps running even if esbuild dislikes it.
- **Target ES2017** downlevels private class fields (ES2022) and most modern
  syntax while staying well within goja's support. Drop to ES2015 only if
  re-diagnosis shows goja still choking on ES2016/17 output.
- **Global-scope preservation** via `FormatDefault` (not IIFE) so classic-script
  side effects on `window`/globals behave as before.

## Risks

- **Semantics drift:** a *successful but wrong* transform could change behavior
  (rare at ES2017). Keep the target conservative; the fallback only covers
  transform *failures*, not silent miscompiles.
- **Not sufficient alone:** heavy bundles may still hit the per-render timeout
  (Mobalytics logged one) or need more globals — hydration is iterative. Set
  expectations: this unblocks parsing + a common missing global, not guaranteed
  full hydration of every SPA. A larger `--js` timeout may be needed for big apps.
- **Perf:** transforming large bundles on every render adds latency (esbuild is
  fast; modules already incur it). Add hashing/caching only if profiling warrants.
- **Security posture unchanged:** transform runs in-process, no network/FS; the
  `crypto` shim reads no host state. Consistent with Phase 13 hardening.

## Tests

Unit (`internal/js`, `js_test`; reuse `render`/`renderWith` from
`engine_test.go`/`async_test.go` for one-shot, or `openContext`/`dispatch`/
`snapshot` from `interact_test.go`):

- **Modern-syntax classic script runs:** inline `<script>` using a private class
  field + optional chaining + logical assignment that writes a `data-*` attr /
  element text. Fails to run pre-fix (goja parse error, attr absent), works
  post-fix. e.g. `class C { #v = 7; get() { return this.#v; } }` … set
  `#out` textContent.
- **`crypto` works:** a script calling `crypto.randomUUID()` and
  `crypto.getRandomValues(new Uint8Array(4))` writes a sentinel proving both ran
  (e.g. UUID length 36, values present).
- **Regression:** whole suite stays green (`make test`) — especially the
  frameworks tests (`internal/js/frameworks_test.go`) and existing script tests.

Manual end-to-end (the real payoff):

- Rebuild (`make build`), run with `--js --tls-mimic`, drive the Mobalytics page
  (reuse the stdio driver pattern from the Phase 14 investigation): `read`
  (render) → `controls` → `interact {event:"click"}` on the **Skill Gems** group's
  "Uber Endgame (Live Build)" tab → confirm `changed:true` and that the panel now
  renders **Thrashing Vines** with its supports (**Execute III, Rapid Casting II,
  Uul-Netol's Embrace, Zenith II, Blindside** — the answer we previously had to
  pull from `__PRELOADED_STATE__`). If still blocked, capture the new debug
  errors for the next iteration.

## Files (predicted)

- `internal/js/scripts.go` — `downlevelClassic` + wire into `runScripts`.
- `internal/js/globals.go` — `crypto` in `preludeJS` (optionally a Go-backed
  `getRandomValues` if real entropy is chosen).
- `internal/js/modules.go` — optional: extract a shared `esbuild` target constant.
- `internal/js/*_test.go` — new unit tests above.
- `docs/architecture.md` — note classic-script downleveling + `crypto` (a new
  "Phase 15" bullet after the Phase 14 gesture entry).

## Pointers

- Memory: `interact-react-aria-press-gap` (records both the shipped Phase 14 fix
  and these two remaining blockers).
- Phase 14 diff lives on branch `interact-gesture-accuracy` (see
  `newUIEvent`/`dispatchPress` in `internal/js/`).
