# ADR 0010: WebExtensions runtime loading (network filtering first)

Date: 2026-07-04
Status: accepted

## Context

unblink is a *semantic reducer*: it fetches a page, runs its JavaScript, strips
visual-only junk, and returns clean Markdown. The single biggest remaining source
of noise and wasted work is advertising and tracking — ad-network scripts,
analytics beacons, and sponsored DOM blocks. The best-in-class tools for removing
that (uBlock Origin, AdGuard) are mature, actively maintained **browser
extensions**, and uBlock Origin is **GPL-3**. unblink is **MIT**.

We want that capability without contaminating our license or forking a filter
engine we would then have to maintain. A browser solves the same problem by
*loading a user-installed extension at runtime* — the extension is a separate
artifact, not part of the browser's source. Running an extension in an interpreter
is not a derivative work of the extension, the same way a browser executing an
add-on, or a shell running a GPL script, is not.

The engine already has the three seams an extension needs: a single network
choke point every page-JS subrequest passes through (`countingTransport` /
`guardedTransport`), a global-install site for new JS APIs (`bridge.install`), and
a DOM-mutation surface for element removal. What it lacked was a way to *load* an
extension and a policy layer to *act* on one.

The full WebExtensions surface uBlock Origin exercises (background service worker,
`runtime` messaging, persistent storage, MV2 `webRequest`) is large and is being
built as a phased roadmap. This ADR records the decision and covers **Phase 1**:
static `declarativeNetRequest` network blocking, which needs **zero extension JS**.

## Decision

1. **Extensions are runtime-loaded artifacts the operator supplies; unblink ships
   none.** `--extension <path>` / `--extensions-dir <dir>` load an unpacked
   directory or a `.xpi`/`.crx`/`.zip` archive. No filter list or extension code is
   vendored or distributed with unblink, so the source tree stays MIT. The
   recommended target is **uBlock Origin Lite (MV3)**: it is storage + DNR only (no
   IndexedDB, no persistent background), which is exactly the Phase-1/3 sweet spot.

2. **A new pure-Go package `internal/webext`** owns the manifest model (MV2 + MV3),
   match patterns, the `declarativeNetRequest` rule model + matcher, and the
   dir/archive loaders. It imports neither `internal/js` nor `internal/browser`, so
   the dependency direction stays `browser → js → webext`. Every `chrome`/`browser`
   shim, the DNR matcher, and (later) the cosmetic engine are **original MIT code
   implementing the public specs** — not ported from uBlock Origin source.

3. **Network rules are enforced at the JS transport choke point.** An
   engine-lifetime `ExtensionHost` holds the compiled rules, shared read-only across
   every concurrent render and live session (one browser process, many tabs). A
   `blockingTransport` decorator sits *inside* `countingTransport`, so a request a
   rule cancels still appears in the `requests` diagnostics as a failure. A blocked
   request returns a request error — the faithful analog of a browser's
   `net::ERR_BLOCKED_BY_CLIENT`. Because unblink never fetches passive subresources
   (images/CSS/fonts), network blocking targets exactly the JS-initiated ad/tracker
   scripts and beacons; cosmetic DOM removal (a later phase) will handle ad *markup*.

4. **The `urlFilter` matcher is tokenized, not regexp.** The Adblock-Plus anchor
   syntax (`||`, `|`, `^`, `*`) is matched by walking literal segments, so a
   hostile filter cannot cause catastrophic backtracking. `regexFilter` is compiled
   with Go's `regexp` (RE2 — no ReDoS) under a length cap.

5. **CRX/XPI signatures are not verified.** A user-supplied local file is not a
   trust boundary; the extension runs in the same untrusted-JS sandbox as page
   script (heap guard ADR 0003, byte budget ADR 0009, SSRF guard). Archive loading
   *does* guard against path traversal (via `fs.FS` valid-path enforcement) and
   decompression bombs (a declared-size cap).

6. **Extensions require the built-in JS engine.** They are meaningless under
   `--disable-js`; `browser.New` rejects that combination loudly rather than
   silently ignoring the flag.

### Load-bearing invariant for later phases

The background service worker + `runtime` messaging (Phase 3+) must honor the
ADR-0004 provable-idle settle: cross-runtime message round-trips will be bracketed
on the page's `bridge.pending` counter, exactly as network requests are, so a
resident background worker never prevents a page from settling and no
content-script async escapes the settle audit. This ADR reserves that decision; the
same-world content-script simplification (one `vm.GlobalObject()`) and its move to
true isolated worlds are likewise deferred and will be recorded when built.

## Consequences

- unblink gains real ad/tracker **network** blocking from unmodified MV3 ruleset
  extensions with no extension JS executed — robust, self-contained, and immediately
  useful for render speed and tracker suppression.
- The GPL/MIT boundary is clean: no extension bytes in the tree, extensions loaded
  like a browser loads an add-on.
- `internal/webext` is a reusable, independently-testable, fuzzed foundation (manifest
  / match-pattern / DNR / archive parsers all have fuzz targets) for the content-script,
  storage, messaging, and MV2 `webRequest` phases that follow.
- Cosmetic filtering, the `chrome`/`browser` API surface, the background worker, and
  full uBlock Origin compatibility remain future phases; this ADR is amended as each
  lands.
