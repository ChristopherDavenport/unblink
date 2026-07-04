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

## Amendment — Phase 2: content scripts, `chrome` API, cosmetic filtering

Adds the parts of the surface that let an extension *modify the page*, not just block
requests:

- **Content scripts** (`content_scripts[].js`/`.css`) matching the page are injected
  around the page's own scripts at their `run_at` (document_start/end/idle), in both
  one-shot renders and live sessions.
- **The `chrome`/`browser` API** is installed as Go closures (`internal/js/extapi.go`):
  `runtime` (getURL/id/getManifest; message/port stubs — no responder until the
  background worker exists), `i18n.getMessage` from `_locales`, `storage`
  (local/session/sync/managed — in-memory, shared per process; disk persistence is
  Phase 3), `scripting.insertCSS`, a single-synthetic-tab `tabs`, and accept-and-ignore
  stubs for the UI/eventing surface (`action`, `permissions`→granted, alarms, …) so an
  extension's init runs instead of throwing. Methods support both the MV2 callback and
  MV3 promise forms; resolving synchronously keeps the ADR-0004 settle audit intact.
- **Cosmetic filtering.** There is no CSSOM, so element-hiding CSS (content-script CSS +
  `insertCSS`) is translated into **physical node removal**: the hidden ad markup is
  detached from the frozen tree post-Terminate (one-shot) / on the snapshot clone (live),
  invisible to page JS — mirroring how CSS hiding fires no mutation events. Because
  unblink emits Markdown and never fetches images, this DOM-stripping is the biggest
  output-quality lever.
- **Same-world simplification** (deferred true isolation to Phase 5): content scripts run
  in the page's single JS global with `chrome` added, which the page can see; the shared
  `chrome` binds to one "active" extension (the first with a content script matching the
  page). A script-less page now still gets a render when an extension is loaded (its
  content scripts must run) — a browser does the same.

Still deferred to Phase 3+: the background service worker + `runtime` messaging (settle
via the `pending` bracket), storage persistence + `onChanged`, and MV2 `webRequest` — the
parts uBlock Origin's *dynamic* cosmetic filtering depends on.

## Amendment — Phase 3: background worker + runtime messaging

Adds the persistent background context and the message path uBlock's *dynamic* cosmetic
filtering uses (a content script asks the background which selectors apply to the current
host, then hides them).

- **Background worker** (`internal/js/bgworker.go`): the extension's background scripts run
  on a dedicated, engine-lifetime eventloop — a *separate goja runtime* from every page
  render, started once and kept warm. It reuses the page bridge with a minimal empty
  document (a real service worker has no DOM; giving it one is a pragmatic simplification,
  like same-world content scripts, that reuses the whole prelude + chrome API + fetch
  stack). Its fetch/module loader serves the extension's own files (`bundleTransport`);
  external background network is out of scope for now. Classic background scripts (MV2
  `background.scripts` / a non-module `service_worker`) run directly; a module
  `service_worker` is bundled through the esbuild path (best-effort, not yet validated
  against a real MV3 build).
- **Messaging broker** (`internal/js/broker.go`): `chrome.runtime.sendMessage` from a
  page/content script routes to the background's `onMessage` listeners and back, carrying
  plain Go values across the two runtimes via each loop's `RunOnLoop`. Both the synchronous
  `sendResponse` and the async (`return true` + later `sendResponse`) forms work.
- **The ADR-0004 reconciliation (the central risk, resolved).** The background runs on its
  own eventloop, so its perpetual timers live in its own audit and can never hold a page
  render open. Content-script async triggered by messaging stays visible to the page's
  settle because every round-trip is **bracketed on the page's `pending` counter** (with a
  keepalive), exactly like a network request — incremented synchronously when the message
  is sent, decremented on the page loop when the reply lands. `TestBackgroundMessaging`
  proves an async reply's DOM effect lands *before* settle (not snapshotted away).
- **Storage**: `chrome.storage.local/session/sync/managed` is a shared in-memory store on
  the `ExtensionHost`, so the background and content scripts share state within the process.

Still deferred (to Phase 4, alongside the `chrome-extension://` resource serving a real MV3
build needs): storage **disk persistence** (so uBO's filter-list compile survives restarts)
and **`onChanged`** fan-out, background→page / `tabs.sendMessage`, real `connect`/`Port`,
external background fetch, and MV2 `webRequest`. Content scripts remain same-world
(isolated worlds are Phase 5).

## Amendment — Phase 4: extension resources, storage persistence, dynamic DNR

Fills in the remaining pieces a stock MV3 build (uBlock Origin Lite) leans on:

- **`chrome-extension://` resource serving** (`internal/js/extresource.go`): a content
  script's `fetch(chrome.runtime.getURL(...))` now resolves to the packaged file instead
  of the network. An `extResourceTransport` decorator sits *inside* the counting transport
  but *outside* the blocking one, so extension resources are logged yet never
  DNR-blocked or counted against the byte budget. Access is gated by
  `Bundle.ResourceAccessible` — the extension's own content scripts may read its files,
  and `web_accessible_resources` opens specific paths to matching page origins.
- **Storage disk persistence** (`internal/js/extstore.go`): `chrome.storage.local`/`sync`
  are written to `<UserCacheDir>/unblink/ext/<id>/<area>.json` and lazily reloaded, so an
  extension's one-time setup (uBlock's compiled lists) survives process restarts;
  `session`/`managed` stay memory-only. **`onChanged`** now fans real change records out to
  registered listeners on their own event loops, pruning listeners whose loop has been torn
  down.
- **Dynamic / session declarativeNetRequest rules**: `updateDynamicRules` /
  `updateSessionRules` actually compile and apply into the `RuleMatcher` (RWMutex-guarded,
  read concurrently by `Match` off the loop), so a rule added at runtime blocks a later
  request — verified end-to-end.

Still deferred to **Phase 5**: MV2 `webRequest` (full uBlock Origin's own JS network engine),
background→page / `tabs.sendMessage` and real `connect`/`Port`, external background fetch,
validating a real module service worker, true isolated content-script worlds, scriptlet
injection (`##+js`), and the IndexedDB/cacheStorage shim.

## Amendment — Phase 5: MV2 `webRequest` blocking

Manifest-V2 uBlock Origin does its own network filtering in the background: it registers
`chrome.webRequest.onBeforeRequest` as a *blocking* listener and returns `{cancel:true}`
(or `{redirectUrl}`) for requests its engine matches. unblink does not reimplement that
matcher — it **delivers each subrequest to the background's listeners and honors the
verdict** (`internal/js/extwebrequest.go`).

The wrinkle is *where* a request is decided: the listeners live in the background runtime,
but a subrequest is evaluated on the page's fetch goroutine (off every event loop). So the
verdict is fetched via a **timeout-guarded round-trip onto the background loop** — no event
loop blocks, and a wedged background degrades to "allow" (2 s cap) rather than hanging the
render. `blockingTransport` consults declarativeNetRequest first (cheap, off-loop), then
the webRequest verdict only when the background has registered listeners (an atomic count
read off-loop; the listeners themselves are touched only on the background loop). A related
fix made background `start()` **block until the background's synchronous setup completes**,
so the first page request always sees the registered listeners (previously a startup race).

Still deferred: background→page / `tabs.sendMessage` and real `connect`/`Port`, external
background fetch, a validated module service worker, true isolated content-script worlds,
scriptlet injection (`##+js`), and the IndexedDB/cacheStorage shim.

## Amendment — real-extension bring-up (uBlock Origin, Privacy Badger)

Validated against unmodified real extensions (loaded via `--extension`, never vendored;
env-gated smoke test `internal/js/realext_smoke_test.go`, `UNBLINK_DEBUG_EXT=1` traces the
background). Both are **MV2 with a background *page*** (`background: {"page": "…"}`), which
drove several gaps closed:

- **`background.page`** (an HTML file whose `<script>`s are the background) is now run —
  classic scripts then module scripts, the latter bundled via esbuild with `chrome-extension://`
  recognized as an absolute specifier (`modules.go`).
- An extension's **own background/content-script contexts may read all of its files**
  (`Bundle.ResourceAccessible`), not just `web_accessible_resources`.
- **`chrome.runtime.getManifest()` returns the full manifest** (real extensions read
  `browser_action`, `icons`, …).
- **The `chrome` object is permissive**: any unimplemented namespace/event falls through to a
  self-propagating inert value (callable + event-shaped, coerces to `""`), so an extension's
  init doesn't crash on a missing API (`extapi_permissive.go`). This alone took Privacy Badger
  from an early crash to a **clean full init**.
- Missing globals the backgrounds need (`HTMLDocument`, `Image`) added.
- **Robustness:** a real extension can wedge its background in a deep-init loop; the background
  runtime is now `Interrupt`ed on close so a loaded extension can never hang shutdown, and
  `start()` is time-bounded so it can't hang construction.

Additional fixes from the deeper bring-up (all correct improvements to the MV2 `webRequest`
path, independent of any one extension):

- **Per-tab page context.** Each render gets a unique synthetic tab id; unblink fires a
  `webNavigation.onCommitted` / `tabs.onUpdated` sequence into the background before the page's
  requests (`exttabs.go`), so an extension builds the right per-tab page store. `webRequest`
  details now carry a **unique `requestId`** (a shared id makes an extension inherit the first
  request's verdict) and the page `documentUrl`/`originUrl`.
- **The `chrome-extension` (and `moz-extension`/`data`) URL scheme** is accepted by
  `ParseMatchPattern`, and a `webRequest` listener whose URL filter was specified but parsed to
  nothing now matches **nothing** (not everything). This fixed a real bug: uBO's
  web_accessible_resources *guard* listener filters on `chrome-extension://…/…*`; with that
  scheme rejected, the guard matched every request and cancelled it — the actual reason an
  earlier build appeared to "block trackers" (it was blocking *everything*, including
  first-party).
- The background has no real IndexedDB/Cache API (the in-memory stubs are incomplete for
  transactional use), so they are removed in the background context — code falls back to
  `chrome.storage.local`, which is backed for real. External background fetches return a clean
  404 (not a rejection) so an extension falls back to its bundled copy.

State:

- **uBlock Origin Lite (MV3) — verified working.** Loading `uBOLite_*.chromium.zip`
  compiles its **18,249 `declarativeNetRequest` rules** (the 6 default rulesets: uBlock
  filters, EasyList, EasyPrivacy, …) into the host matcher, and unblink **blocks real
  trackers** (`adscore.com`, `adsmeasurement.com`, `analytics.blue`, …) while correctly
  passing first-party and benign requests — a full render in ~0.1 s. No service worker or
  in-engine filter compilation is involved: the static rulesets are evaluated by unblink's
  own `RuleMatcher` (Phase 1). **This is the recommended ad-block configuration.**
- **Full uBlock Origin (MV2) — not viable in-process.** It loads and runs its background
  page but its static filter engine never becomes ready (`µb.readyToFilter` stays false):
  compiling EasyList + EasyPrivacy + uBO's lists (~3.6 MB) in goja is impractical and its
  init stalls in the list-loading pipeline. The gaps (an IndexedDB/cacheStorage shim, an
  external list transport, filter-compile performance) are recorded but not pursued — uBO
  Lite is the answer.
- **Privacy Badger (MV2)** fully initializes (learning-based, so it does not block on a cold
  profile).

Note the gorhill/uBlock GitHub releases ship *full* uBO for both Firefox (`.xpi`) and Chromium
(`.zip`) — both MV2. uBlock Origin **Lite** is the separate `uBlockOrigin/uBOL-home` project.
