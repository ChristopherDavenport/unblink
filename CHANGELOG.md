# Changelog

All notable changes are recorded here. Earlier history lives in the phase log of
[docs/architecture.md](docs/architecture.md).

## Unreleased

### Security

- **Untrusted page JavaScript could read local files (esbuild glob imports).**
  Affected versions: v0.16.0 (the first public release) through v0.25.0.
  `<script type=module>` was bundled with esbuild, whose bundler expands a dynamic
  import or `require` with a non-literal relative argument (``import(`./${x}.json`)``,
  `import("./" + x)`, `../` forms too) as a glob over the real filesystem, without
  consulting unblink's network-only resolver. A page could read files under the
  server's working directory or its parents — or anywhere under `/` from an inline
  module — for every file type esbuild loads by default (JavaScript, TypeScript, JSON,
  CSS, text), and send the contents to any origin. The inline form also walked the
  whole filesystem, stalling the render and consuming gigabytes of memory. It needed
  JavaScript rendering (the default) and one visit to a hostile page; `--disable-js`
  was not affected. Page modules are no longer bundled: they load through the
  per-render module registry, which only uses esbuild's `Transform` (no filesystem
  access). See the 2026-11-27 amendment to
  [ADR 0018](docs/decisions/0018-module-instance-registry.md).

- **Untrusted page JavaScript could hang a render forever.** Affected versions:
  v0.16.0 (the first public release) through v0.25.0 — reproduced on every tagged
  release. The render watchdog stops a script that never
  yields by interrupting the engine, and an interrupt unwinds exactly one call. If
  another callback that never returns was already queued — the next tick of
  `setInterval(function () { for (;;) {} }, 10)` is enough — the event loop started
  it with no interrupt pending. The render never returned, its worker slot was never
  released and one CPU core spun until the process exited; a few such pages exhaust
  the render concurrency limit. A live session's operations and its close could hang
  the same way. The engine now keeps interrupting until the render's settle has
  closed and until its event loop has stopped. `--disable-js` was not affected.

- **A session page could spin a CPU core between tool calls.** Present on `main`
  after v0.25.0; tagged releases were not individually tested. A session's live JavaScript
  runtime keeps serving the page's timers between tool calls, and nothing watched
  it then: a timer callback that never returns spun one core until the next call or
  the session's close. An idle watchdog now interrupts any page task that runs
  longer than 2 s while no tool call is in flight, and shuts the runtime down after
  three in a row. `interact` then reports `live_js_stopped` (retrying reloads the
  page's scripts; in-page state is lost) and `read` returns the last captured page
  with a `js_errors` line saying its JavaScript is no longer running. A tool call
  queued behind a runtime that closes now returns instead of waiting forever.

### Changed

- **Module scripts load through the module registry.** One instance per module URL is
  now shared by every importer in a render, static or dynamic. A literal `import()`
  the page never calls is no longer fetched at load, and a module that fails to load
  fails only the importers that need it instead of the whole entry. `import.meta.url`
  and `import.meta.resolve()` work, `import()` of a `.json` URL works on every path,
  and a module's own non-literal `import()` resolves against that module's URL. An
  external module named by two script tags now runs once. A wide module graph costs
  more CPU per render than the single bundle did.

- **Engine-built objects are instances of their interfaces.** `window` is a `Window`
  (Ember's "is this a browser?" test, `self.constructor === Window`, now passes — an
  Ember app such as Discourse no longer boots non-interactive and blank), and
  `location`, `history`, `navigator`, `screen`, `performance`, the storages, `crypto`
  and `customElements` are instances of their interfaces. Event classes form the
  standard hierarchy, so `e instanceof Event` holds for every event and a dispatched
  `click` is a `PointerEvent` whose `button` is 0. `querySelectorAll` returns a
  `NodeList` and `children`/`getElementsBy*` an `HTMLCollection` with
  `item()`/`namedItem()`. New: `UIEvent`, `FocusEvent`, `WheelEvent`, `DragEvent`,
  `CompositionEvent`, `SubmitEvent`, `PopStateEvent`, `HashChangeEvent`,
  `PageTransitionEvent`, `BeforeUnloadEvent`, `ProgressEvent`, `AnimationEvent`,
  `TransitionEvent`, `ClipboardEvent`, `Node.baseURI`, `window.origin`,
  `window.length`, and `String(location)` is its URL.

### Added

- **`mode=article` no longer returns a fragment of a hub page.** On a section
  front, a link directory or an index, readability's best block is one card or
  the footer blurb, and `read` (whose default mode is `article`) returned it as
  the page. The extraction is now checked against the page's content — the main
  landmark or body, minus navigation, sidebar, header and footer chrome — and
  when it holds under half of it (or under two thirds, with the rest a list of
  links) `read` returns the main landmark instead, or the whole page when there
  is none. The result reports it: `mode` is `main` or `full`, `article_fallback`
  is true and the new `article_fallback_reason` is `partial_article`
  (`no_article` for the existing nothing-found fallback). Real article pages are
  unchanged.
- **Render-collapse guard.** When a server-rendered page's JavaScript leaves
  under a tenth of its content — a hydration crash that puts the framework's
  error boundary where the document was, an error or consent shell — the
  rendered DOM is discarded and the page is the server's HTML, as with
  `render=false`. `read` and `browse` report `render_discarded`; in a session,
  `interact` still acts on the live JavaScript DOM and says so
  (`render_was_discarded`). A client-rendered app (nothing to fall back to), a
  render that merely shortens or hides content, and a render whose
  `wait_for`/`wait_text` gate was met are never discarded. See
  [ADR 0023](docs/decisions/0023-render-collapse-guard.md).
- **Markdown content negotiation (`read`'s `prefer_markdown`,
  `--prefer-markdown`).** A `text/markdown` response is now returned as-is
  (`kind: "markdown"`), no longer wrapped in a code fence as plain text. `read`
  gained a tri-state `prefer_markdown`: `true` asks the site for its Markdown
  version (`Accept: text/markdown, text/html;q=0.9, …`), `false` fetches the
  HTML page (also after a Markdown read of the same URL), and absent follows the
  server default, which `--prefer-markdown` / `--prefer-markdown=false` set and
  which is off for now. A site without a Markdown version answers with HTML and
  the read is unchanged. No other tool, subresource, page-JS request, form
  submit or discovery probe sends the header, and the URL cache keeps the two
  representations apart. See
  [ADR 0021](docs/decisions/0021-markdown-content-negotiation.md).
- **Opt-in robots.txt enforcement (`--respect-robots`).** Off by default, so
  nothing changes unless an operator sets it. With it on, every agent-directed
  navigation is checked against the target origin's robots.txt (`*` group, path
  plus query) before the request is sent: the page fetch of every page tool
  (cached or not, with or without a session), `click`, `submit_form`, each page
  and declared sitemap file `map` fetches, and every redirect hop of those. A
  disallowed URL is never requested; the tool returns a non-retryable
  `robots_disallowed` error naming the URL and the matching rule, and `map`
  skips it and reports `skipped_by_robots`. A robots.txt answering 4xx means no
  restrictions; 5xx or a failed fetch means the origin is disallowed, re-checked
  after 30 seconds (RFC 9309 section 2.3.1). `site` and the `browse` robots hint
  gain `enforced`. Not gated: a page's own subresources and page-JS requests,
  the robots.txt/llms.txt discovery probes, and `search` provider
  calls. `Crawl-delay` is still only reported; pace with `--rate-limit`. See
  [ADR 0020](docs/decisions/0020-opt-in-robots-enforcement.md).

### Fixed

- **A render no longer closes in the middle of time-sliced work.** React hydrates in
  ~5 ms slices, yielding through `setImmediate`, and changes nothing in the page
  until it commits; with no request in flight that looked like a quiet page, and the
  render closed 60 ms in. `setImmediate`, `MessageChannel` and `window.postMessage`
  — the ways a scheduler yields and continues — now hold the settle open while one is
  pending, for at most 2 s per render. `setTimeout(fn, 0)` and
  `requestAnimationFrame` loops do not. See the amendment to
  [ADR 0004](docs/decisions/0004-provable-idle-settle.md).
- **A `MessagePort` delivers a message posted from inside a handler in a later
  task,** as a browser does, instead of in the same synchronous drain.

- **A self-rearming timer no longer becomes a busy loop at the end of a render.** A
  one-shot timer longer than the render budget is pulled in so the content behind it
  still appears. Once that deadline had passed, *every* new timer was pulled in to
  zero delay, so a timer that re-arms itself — a 30-second poll, a retry backoff —
  ran back to back for the last 600 ms of the budget: about 20,000 times in a test,
  and on a Discourse forum 1,731 `POST …/message-bus/…/poll` requests in one read.
  After the deadline a timer is now pulled in only if it would outlast the budget,
  and at most 32 times per render.
- **`CSS.supports()` no longer claims other engines' vendor properties.** It answered
  `true` to everything, including `-webkit-backdrop-filter`, `-webkit-touch-callout`
  and `-moz-appearance` — the probes pages use to detect Safari, iOS and Firefox. A
  component library took its WebKit branch, rendered different markup on the client,
  and React hydration failed (error #418) on Next.js App Router pages. Standard
  properties still answer `true`; vendor-prefixed ones answer as desktop Chrome does.
- **A closed `<dialog>` is hidden content, and `dialog.show()`/`showModal()`/`close()`
  exist.** A `<dialog>` without `open` is not rendered by any browser, but its text
  was extracted like visible text; a page that builds hundreds of closed dialogs (a
  specification's per-term reference panels) grew its Markdown by half. It now counts
  as hidden, like `[hidden]`, and the three methods set and remove `open`, so a dialog
  a script opens is visible. Calling them used to throw.

- **Property writes reach the page tree (reflected attributes).** `input.name = …`,
  `a.title = …`, `img.alt = …`, `el.hidden = true`, `link.rel = …`,
  `option.selected = true`, `el.ariaLabel = …` set an expando on the JavaScript
  wrapper and nothing else, so `read`, `forms` and `links` never saw state a page set
  through properties; only `id`, `className`, `href`, `src`, `value`, `checked` and
  `slot` reflected. The common reflected attributes of every HTML interface now read
  and write the content attribute, with the standard's defaults and keyword rules,
  and exist only on the interfaces that have them (`'disabled' in input` is true,
  `'disabled' in div` is false — the test Preact and Vue use to choose between a
  property and an attribute). `meta.content` no longer reads `undefined`.
- **`el.style` writes reach the `style` attribute.** `el.style.display = 'none'`,
  `style.setProperty(…)` and `style.cssText = …` changed a private object; the
  element's `style` attribute, which `innerText` and hidden-content stripping read,
  never moved. `el.style` is now a declaration block over the attribute. There is
  still no CSSOM: values are not validated and nothing is computed. Content a page
  hides with an inline style is now treated as hidden, and content it reveals as
  visible.
- **`innerHTML`/`outerHTML` write void elements as `<br>`, not `<br/>`.**

- **Event-handler properties and attributes run.** `window.onload = fn`,
  `el.onclick = fn`, `document.onreadystatechange = fn` and `onclick="…"` in markup
  were stored and never called; only `addEventListener` listeners ran. They are now
  real event handlers: one entry in the target's listener list at the position the
  handler was first set, `this` and `event` bound, `return false` cancelling the
  event, `<body onload>` and friends forwarded to the window. An attribute's text is
  compiled on first use, with the element, its form owner and the document in scope,
  and is gated by the page's Content-Security-Policy as inline script
  (`script-src-attr`, `'unsafe-inline'`, `'unsafe-hashes'`; a nonce never covers an
  attribute). `interact` clicks and submits run them.
- **Uncaught exceptions reach `window.onerror` and `error` listeners.** An exception
  that escapes a script, an event listener or a timer callback fires an `ErrorEvent`
  at the window (`onerror` gets the five-argument form, and returning `true` handles
  it). Listener and timer exceptions used to vanish without a trace; they are now in
  `js_errors` unless a handler took them.
- **`js_errors` covers the whole render.** It was filled once, after the initial
  script pass, so an error raised later — from a timer, a lazily loaded chunk, a
  `console.error` in an async callback, a promise rejected after load — was never
  reported by a one-shot `read`. It is now read when the render settles.

- **JavaScript engine: DOM and Web API gaps found by the real-page corpus.** Each
  of these stopped a mainstream framework from booting or hydrating, or made a page
  update the engine could not see:
  - `ReadableStream.read()` on an open, empty stream now stays pending until the
    producer enqueues (it resolved `{done: true}`, which React's Flight client reads
    as "Connection closed" — no Next.js App Router page hydrated). `tee()`,
    `pipeTo()`/`pipeThrough()` and `TransformStream` go through the controller;
    `Response.body`, `Blob.stream()`, `new Response(stream)` and
    `ReadableStream.from()` are new.
  - The ParentNode and ChildNode mixins exist on every interface that has them:
    `append`/`prepend`/`replaceChildren` and `querySelector(All)`/`children`/
    `getElementById` on `DocumentFragment` (`template.content`, range and created
    fragments), `ShadowRoot` and parsed documents; `before`/`after`/`replaceWith`/
    `remove` on elements, text and comment nodes. `new DocumentFragment()`,
    `new Text()` and `new Comment()` construct real nodes. `ShadowRoot` is a global
    and a `DocumentFragment`.
  - Every element is an instance of its tag's interface (`HTMLScriptElement`, …),
    `Object.prototype.toString.call(el)` names it, and `constructor.name` no longer
    exposes a Go symbol.
  - `innerText`/`outerText` exist (a layout-free approximation on read, exact on
    write). `textContent` on a text or comment node reads and writes its data.
  - `document.readyState` goes `loading` → `interactive` → `complete` with
    `readystatechange`, where it was a constant `complete`.
  - `document.evaluate`, `XPathEvaluator`, `XPathExpression` and `XPathResult`: an
    XPath 1.0 evaluator over the node tree, bounded in expression size, nesting and
    evaluation steps.
  - `el.attributes` is live and `removeAttributeNode`/`setAttributeNode` exist;
    `classList` is a full, live `DOMTokenList` and `link.relList.supports()` answers
    from what the engine acts on; `document.createNodeIterator`, `document.scripts`/
    `images`/`links`, `document.domain`, `history.state`, `StorageEvent`,
    `performance.timing`/`navigation` and the navigation timing entry are new.
  - `<html>`/`<body>` report the constant 1280×720 viewport rect; element scroll
    methods exist and `scrollTop` is writable.
  - `n.toLocaleString('en-US')` formats (it threw a `RangeError`).
  - `unhandledrejection` is dispatched to window listeners; an element's `load`
    event no longer reaches window listeners.
  - A page-global `require` (an AMD loader) is no longer shadowed inside module
    scripts.
- **`data:` script URLs are decoded locally** on every script path (classic,
  script-inserted, module entry and dependency, `import()`, preload hints) instead
  of being sent to the network. They are subject to `script-src` — only an explicit
  `data:` source or `'strict-dynamic'` admits one — to `integrity`, and to
  `--js-max-bytes`.

## v0.25.0 — 2026-11-27

### Added

- **Import map `scopes`.** The import map is parsed and resolved per the HTML Standard
  (`internal/js/importmap.go`): `"imports"` and `"scopes"`, exact and trailing-slash
  prefix keys, the most specific matching scope first. One resolver serves static
  `<script type=module>` bundling, the module registry's static dependencies, and
  dynamic `import()` — which previously did not consult the import map at all. A
  registry module's nested `import()` now resolves against that module's own URL.
  Limits: a classic script's `import()` and a non-literal `import()` in a bundled
  module use the document base as referrer; only the first import map is read; the
  import map `"integrity"` key is not handled.
- **Form-associated custom elements.** `ElementInternals` is real for elements defined
  with `static formAssociated = true`: `setFormValue`, `form`, `labels`, the validity
  API (`setValidity`, `checkValidity`, `reportValidity`), `states`, and the
  `formAssociatedCallback`/`formDisabledCallback`/`formResetCallback` reactions. Such an
  element takes part in `form.elements`, `new FormData(form)`, form submission and
  form validation, and its value appears in the `forms` tool and is sent by
  `submit_form` ([ADR 0019](docs/decisions/0019-form-associated-element-reflection.md)).
  `attachInternals()` now throws `NotSupportedError` on a non-custom element or a
  second call, as browsers do. `form.reset()` is new, and `requestSubmit()` validates
  first unless `novalidate`. Not implemented: `formStateRestoreCallback`, `:state()`,
  `form="id"` association, reflection of `File` values to the `forms` tool.
- **`form="id"`, `:state()` and default ARIA semantics for custom elements**
  ([ADR 0019](docs/decisions/0019-form-associated-element-reflection.md), Amendment 1).
  A control's `form` attribute now decides its form owner, for native and custom
  controls, in page JS and in the `forms` tool alike; a descendant control that names
  another form no longer belongs to its ancestor form. `:state(x)` matches
  `ElementInternals.states` in `matches`/`querySelector`/`closest`. A custom
  element's `internals.role`, `ariaLabel`, `ariaLabelledByElements` and checked/
  expanded/pressed/selected/required/invalid/disabled defaults reach the semantic
  output; an explicit `role`/`aria-*` attribute wins. `adoptedCallback` is still
  not fired.
- **`Element.insertAdjacentText`.** It was missing and threw a `TypeError`.
- **Passive-by-default touch/wheel listeners.** `touchstart`/`touchmove`/`wheel`/
  `mousewheel` listeners on window, document, `<html>` and `<body>` are passive unless
  registered with an explicit `passive: false`, as the DOM Standard specifies, so a
  `preventDefault()` there no longer cancels the event. Re-adding the same
  (type, callback, capture) listener is now a no-op.
- **DOM `Range` and `Selection`.** `Range` implements the DOM Standard's boundary
  points, comparison methods, `toString`, and the clone/extract/delete/insert/surround
  algorithms over the node tree (it was a shape-only stub returning empty results);
  `Selection` holds the range it was given, and `getRangeAt` on an empty selection
  throws `IndexSizeError`. Ranges are not live, no `selectionchange` event fires, and
  the geometry methods remain zero stubs.

### Changed

- **Go 1.27 is now the minimum.** The refreshed `golang.org/x/*` dependencies need
  1.26 and `whatwg-url` 0.9 needs 1.27; the `go` directive moved 1.25 → 1.27.
  `GOTOOLCHAIN=auto` (set by the Makefile) still downloads the toolchain automatically.
- **`--tls-mimic` uses `net/http`'s HTTP/2 client** instead of the deprecated
  `x/net/http2` client API. The opening HTTP/2 frames are unchanged and are now pinned
  by a wire-level test.
- **`whatwg-url` 0.9**, under its new module path
  `github.com/nationallibraryofnorway/whatwg-url`.
- **WPT conformance re-measured**; see `docs/wpt-compatibility.md`. Earlier figures
  undercounted because the missing `insertAdjacentText` crashed the test harness's
  result rendering.
- **Dependencies refreshed**, including releases that clear govulncheck findings, and a
  reviewed goja/goja_nodejs bump ([ADR 0002](docs/decisions/0002-dependency-pinning-policy.md))
  that picks up upstream fixes for out-of-bounds access through detached TypedArray
  buffers and several parser panics. Upstream goja now accepts the `[\s-_]` regex idiom
  itself; the [ADR 0017](docs/decisions/0017-regex-class-hyphen-rewrite.md) rewrite is
  retained as a no-op.
- **`statusText` is the server's reason phrase.** `XMLHttpRequest.statusText` and
  `Response.statusText` report the phrase from the HTTP/1.x status line instead of a
  constant `OK`. It is empty on HTTP/2 and later, which have no reason phrase.

### Fixed

- **`XMLHttpRequest` follows the XHR Standard where it previously diverged.** `abort()`
  cancels the request and fires `abort`/`loadend` instead of delivering the response;
  `timeout`/`ontimeout` cut the request at the deadline; a response walks
  `HEADERS_RECEIVED` → `LOADING` → `DONE` with `readystatechange` at each step (also
  delivered to `addEventListener` listeners); `overrideMimeType` and `responseXML`
  work; `send()` throws `InvalidStateError` outside the `OPENED` state.
- **Next.js pages that rendered as "Application error" now render.** Three engine gaps,
  each fatal to a page such as react.dev/learn: `Node.isEqualNode`/`isSameNode` were
  missing (Next.js's head manager calls `isEqualNode`); `document.hasFocus` was
  missing; and `MutationObserver` handed an observer the mutations made while it was
  disconnected, which crashed CodeMirror 6, and never called back an observer that
  re-observed after `disconnect()`. Records are now queued per observer at mutation
  time, as the DOM Standard specifies.
- **A throwing XHR event handler no longer strands the request.** Each handler and
  listener is isolated; the exception is reported to the console and delivery
  continues.
- **`submit_form` sends what `forms` shows.** Repeated field names are all sent, in
  order (only the last was sent before); an unchecked checkbox or radio contributes
  nothing (a radio group used to post its last radio whether or not it was checked),
  and a checked one without a `value` posts `on`. A caller-supplied value for a name
  still replaces every entry of that name.
- **Custom elements created through `innerHTML` are upgraded.** The setter never
  upgraded them, in the light tree or a shadow root.
- **`--tls-mimic` connection pool.** A burst of concurrent requests to a host with no
  pooled connection each dialed and closed the previous connection under its
  in-flight request, so all but one failed; they now share one dial. A connection at
  the server's stream limit is treated as busy, not dead. A request that fails on its
  own deadline returns that error and leaves the healthy connection pooled.
- **Import maps:** a map inserted by a classic script before the first module loads is
  honoured again; URLs are parsed and serialised with the WHATWG URL parser; duplicate
  keys resolve last-wins; a map whose `imports` or `scopes` is not an object is
  rejected. Lookups no longer scan every key, and a map is rejected above 4 MiB of
  source, 50,000 entries or 2,000 scopes.
- **PDF extraction stops when its request ends**
  ([ADR 0001](docs/decisions/0001-pdf-extraction-library.md), Amendment 3). The
  extraction goroutine used to keep running after the context ended or the wall-clock
  limit passed. The vendored reader is now bound to the context and fails at its
  checkpoints once it is done. Cancellation is cooperative: a loop that reaches no
  checkpoint would not be stopped; none is known.
- **Feed (RSS/Atom/JSON Feed) Markdown.** A literal `<` in plain-text content is no
  longer treated as a tag opener (which dropped the text after it), adjacent block
  elements are separated instead of glued together, titles are collapsed to one line,
  and link labels and destinations are escaped. Relative item links are resolved
  against the feed URL.

### Security

- **`Set-Cookie` never reaches page JS.** Response headers handed to page script kept
  `Set-Cookie`, so an HttpOnly cookie's value could be read through
  `fetch()`'s `response.headers`, through `XMLHttpRequest.getResponseHeader`/
  `getAllResponseHeaders`, and through an internal property of the XHR object.
  `Set-Cookie` and `Set-Cookie2` are now dropped where the engine builds the response
  object for page JS, which covers fetch and XHR alike. The cookie jar is fed below
  that point and still stores the cookies.
- **Import maps are gated by CSP.** An inline `<script type="importmap">` now needs
  the same nonce/hash allowance as an inline script (`internal/js/csp.go`,
  `cspAllowInlineElement`). Previously a non-nonced import map under
  `script-src 'nonce-…'` was honoured and could redirect a trusted module's imports.
  A blocked map is ignored and recorded as a CSP violation; report-only reports
  without blocking; `--no-csp` disables the gate.

## v0.24.0 — 2026-07-06

### Fixed

- **Astro/React islands now hydrate** ([ADR 0017](docs/decisions/0017-regex-class-hyphen-rewrite.md),
  [ADR 0018](docs/decisions/0018-module-instance-registry.md)). Two goja-level gaps kept
  `client:only` islands (e.g. poe.ninja) silently inert:
  - goja rejects a valid-ECMAScript `-` immediately after a `\s`/`\d`/`\w` shorthand in a
    character class (the `/[\s-_]/` camelCase/slugify idiom) with "invalid character class
    range", aborting the whole script/module. `fixClassRangeHyphen`
    (`internal/js/regexfix.go`) source-rewrites the hyphen to `\-` before compile —
    value-preserving in every JS context, with an odd-backslash guard for a real
    `[\\s-z]` range.
  - Dynamic `import()` duplicated a module shared by two entry chunks (React's dispatcher
    across an island's component + renderer chunks → "useMemoCache of null"). A per-render
    module-instance registry (`internal/js/moduleregistry.go`) gives browser module-map
    semantics — each resolved URL is fetched, transformed, and evaluated once, its live
    namespace shared across importers.

### Changed

- **`requests` keeps the tail and classifies by resource type.** The network log is now a
  ring buffer that evicts the oldest record (raised to 4096, sized for a heavy SPA's whole
  subrequest set) instead of a 256-record head cap that dropped exactly the late-firing
  data calls the tool exists to surface. Every request carries a devtools-style resource
  type (`html`/`js`/`xhr`/`css`/`fonts`/`images`/`media`/`websocket`/`other`) from its
  initiator refined by `Content-Type`; the tool takes `types=[...]` to filter (e.g.
  `types=["xhr"]` for just the data calls) and reports the pre-filter `total` alongside
  `count`.

## v0.23.1 — 2026-07-05

### Fixed

- **Version reporting is now correct by construction.** The advertised server
  version (`--version` and the MCP handshake) came from a hardcoded constant
  overridden only by the release `-ldflags` stamp, so a `go install …@vX.Y.Z`
  build — which runs no ldflags — reported a stale version. `Version()` now falls
  back to the module version the Go toolchain embeds (`debug.ReadBuildInfo`), so
  every build path reports its real version with no constant to hand-maintain.
  The release workflow also auto-stamps the README Status headline from the tag,
  matching how `.claude-plugin/plugin.json` is already synced.

## v0.23.0 — 2026-07-05

### Added

- **web-platform-tests conformance harness ([ADR 0016](docs/decisions/0016-wpt-conformance.md)).**
  `make wpt` runs the in-scope WPT `testharness.js` corpus directly against the JS
  engine and emits an honest, bucketed scorecard — every subtest is pass, an in-scope
  gap, by-design, a goja ceiling, or a runner limitation; conformance = pass/(pass+gap)
  with all exclusions itemized in `wpt/last-run.json`. The corpus is a pinned, vendored
  subset refreshed by `make wpt-sync` and kept out of the repo (only the upstream SHA
  is tracked). On-demand analysis, not a CI gate. The ranked roadmap it produces lives
  in [docs/wpt-compatibility.md](docs/wpt-compatibility.md).

### Changed

- **URL is now a native WHATWG parser.** `window.URL`/`URLSearchParams` are backed by
  `github.com/nlnwa/whatwg-url` (Go binding in `internal/js/urlnative.go`) with
  reparsing setters, `canParse`/`parse`, IDNA/punycode, special-scheme rules, and a
  live `searchParams` — replacing the previous regex shim. WPT `url/` conformance
  0.11 → 0.98. The Go-side link/fetch pipeline keeps `net/url` and is unaffected.
- **TextDecoder/TextEncoder correctness.** `fatal` mode throws on malformed input,
  `{fatal, ignoreBOM}` are reflected + honored (BOM stripping), encoding labels are
  validated/canonicalized (invalid labels throw `RangeError`; valid legacy labels are
  accepted), and `TextEncoder` emits U+FFFD for lone surrogates instead of WTF-8. WPT
  `encoding/` 0.50 → 0.87.
- **DOM conformance.** A real `DOMException` (legacy `.code`, Go-side throwing),
  UTF-16-correct `CharacterData` mutation methods (`substringData`/`insertData`/
  `deleteData`/`replaceData`/`appendData` + `IndexSizeError`), DOMTokenList branding,
  `createHTMLDocument` Document identity, and `dispatchEvent` `InvalidStateError` for
  events dispatched before `initEvent`. WPT `dom/` 0.49 → 0.65.

### Security

- **Same-Origin-Policy audit + hardening** (confirmed against unblink's own targeted
  tests, which WPT structurally cannot reach). CSP **nonce hiding** — page JS can no
  longer scrape and reuse a nonce past `script-src`. A same-origin fetch **redirected
  cross-origin** is re-validated against CORS, so a redirect can't launder a
  cross-origin read past the Same-Origin Policy. Plus regression locks for
  SharedArrayBuffer-absence-under-isolation, cross-origin SRI, the public-suffix
  cookie jar, and the moot-by-architecture SOP DOM-isolation stubs.

## v0.22.0 — 2026-07-04

### Added

- **WebExtensions runtime loading — ad/tracker blocking with real, unmodified
  extensions ([ADR 0010](docs/decisions/0010-webextensions-runtime-loading.md)).**
  `--extension <dir|.xpi|.crx|.zip>` and `--extensions-dir` load a user-supplied
  browser extension at runtime — unblink ships **no** extension bytes, keeping GPL
  tooling (uBlock Origin is GPL-3) out of the MIT tree the way a browser loads a
  user add-on. A new pure-Go `internal/webext` package models MV2/MV3 manifests,
  match patterns, and a tokenized no-ReDoS Adblock `urlFilter` matcher; the engine
  surface (`internal/js/ext*.go`) provides the `chrome`/`browser` API, content
  scripts, cosmetic filtering (element-hiding CSS → physical node removal, since
  there is no CSSOM), a background worker + runtime messaging, static/dynamic
  `declarativeNetRequest`, and MV2 `webRequest` blocking. **uBlock Origin Lite
  (MV3) is verified working**: its 18,249 `declarativeNetRequest` rules compile
  into unblink's host matcher and block real trackers (~0.1 s render, no service
  worker); Privacy Badger fully initializes. Extensions require JavaScript
  (rejected under `--disable-js`) and run in the same heap/byte/SSRF sandbox as
  page JS. *Full* uBlock Origin (MV2) is not viable in-process — compiling its
  filter lists in goja is too slow; Lite is the answer.
- **Resource-based JavaScript budgets + concurrent module-graph warming
  ([ADR 0009](docs/decisions/0009-resource-based-js-budgets.md)).** A per-render /
  per-dispatch **byte** budget (`--js-max-bytes`, default 64 MiB) replaces the
  fixed request-count cap (now an off-by-default backstop, `--js-max-requests`)
  and the removed live-session rate window. Concurrent `<link rel=modulepreload>`
  warming loads a code-split SPA's serial `import()` graph as cache hits — adding
  no async primitive, so the provable-idle settle invariant (ADR 0004) still holds.

## v0.21.0 — 2026-07-04

### Added

- **`extract` tool — caller-directed CSS-schema extraction.** Pass a `fields` schema mapping
  each output name to a CSS selector (a string takes the first matching element's collapsed text;
  an object `{selector, attr}` takes an attribute value instead) and an optional `root` selector to
  emit one record per matching container (e.g. `root="li.product"`); omit `root` to treat the whole
  document as a single record. Returns an array of records — `limit` defaults to 50 (hard cap 200)
  and `truncated` reports whether more matched. Read-only, HTML only, no JavaScript injection: pure
  selection over the parsed DOM via the new `dom.Records` primitive (`internal/dom/schema.go`).
  Complements `data` (auto-discovered JSON-LD/tables/microdata) with agent-directed extraction, and
  closes the one Obscura/Lightpanda differentiator ("CSS-schema extraction") that fit unblink's
  reduce-to-meaning identity. Included in the `read-only` and `full` tool presets; not capability-gated.
- **`browse` now proposes extraction schemas (`collections`).** Every `browse` auto-detects the
  page's dominant repeating record-sets (product lists, search results, table-like rows) and returns
  each as a ready-to-use `extract` schema: a validated `root` selector, the enclosing landmark region,
  and record-relative field selectors (`{name, selector, attr}`). Detection runs inside the always-on
  structural pass (`internal/dom/collections.go`), reuses `browse`'s region classification to rank
  candidates (content lists in `main`/`article` win; nav/footer chrome is demoted or dropped), and
  validates every emitted schema by round-tripping it through the `extract` executor — so a surfaced
  collection always works. This closes the discovery gap: an agent orienting with `browse` gets the
  selectors for free instead of reading raw HTML to find them. Terse and capped to keep `browse` cheap.

## v0.20.0 — 2026-07-04

### Added

- **A semantic structured representation of the page, computed from the parsed node
  tree with no browser ([ADR 0007](docs/decisions/0007-semantic-structured-representation.md),
  [ADR 0008](docs/decisions/0008-stable-element-ids.md)).** `browse` now returns a
  landmark/region map (banner/navigation/main/… with per-region link/form/control/
  heading counts), a structured heading outline (level/text/id, deep-linkable), and a
  unified `metadata` object (canonical — finally surfaced — plus og:image, author,
  published/modified time, favicon, twitter). `controls`/`forms`/`interact` controls
  gain rich ARIA state (checked/expanded/pressed/selected/required/invalid, plus
  value/placeholder/href), and every control/region/heading carries a stable
  content-hash `id` — a mutation-resilient reference key alongside the CSS selector.
  Ports charlotte's accessibility-tree decomposition to unblink's no-layout model;
  geometry/bounds/CSSOM remain permanent non-goals.
- **`requests`, `console`, and `cookies` tools.** `requests` lists the network
  requests a page's JavaScript made while rendering (method/url/status) — render once,
  see the JSON endpoint the page fetched, then `read` it directly instead of scraping
  the hydrated DOM. `console` returns the page's captured console output
  (log/info/warn/error/debug), filterable by level, for debugging a render. `cookies`
  inspects, sets, or clears a session's cookies, scoped to an origin.
  `requests`/`console` require JavaScript.

### Changed

- **Configurable tool exposure.** The server now advertises only tools that can do
  something: `search` is hidden without `--search-provider`, and `interact`/
  `requests`/`console` are hidden under `--disable-js` — a tool that could only return
  an error is context cost with no value. Operators can narrow the surface further with
  `--tools` (comma-separated tool names and/or the presets `core`/`read-only`/`full`)
  and `--disable-tools`.

## v0.19.0 — 2026-07-03

### Added

- **A broad Web API surface so JS pages render instead of crashing at boot
  (Phases 24–26, [docs/web-api-priorities.md](docs/web-api-priorities.md)).** The
  flat-DOM engine now provides the browser APIs mainstream apps touch during
  hydration, each by the cheapest treatment that lets content materialize — a real
  implementation, an inert crash-avoidance stub, or a deliberate leave-undefined:
  - **Tier 1** — `CSS.escape`/`CSS.supports`, `FileReader`, `ReadableStream`/
    `WritableStream`/`TransformStream`, `EventSource`, an in-memory `indexedDB`, an
    inert canvas-2D context, the `navigator` serviceWorker/clipboard/permissions/
    geolocation/mediaDevices surfaces, and `Element` `lastElementChild`/
    `getAttributeNode`/`namespaceURI`.
  - **Tier 2** — `DOMMatrix`/`DOMPoint`/`DOMRect`/`DOMQuad`/`Path2D`, `document.fonts`/
    `FontFace`, `Element.animate` (Web Animations), custom-element `disconnectedCallback`
    + `attachInternals`/`ElementInternals`, the Navigation API, `TextEncoderStream`/
    `TextDecoderStream`, `cookieStore`, `reportError`.
  - **`crypto.subtle`** ([ADR 0006](docs/decisions/0006-crypto-subtle.md)) — a real,
    Go-backed WebCrypto: `digest` (SHA-1/256/384/512), HMAC sign/verify, AES-GCM/CBC/CTR
    encrypt/decrypt, PBKDF2/HKDF derive, and symmetric key generate/import/export, with
    `getRandomValues` re-pointed at `crypto/rand`. RSA/ECDSA and wrap/unwrap reject
    catchably rather than throwing. Reverses the earlier "leave `crypto.subtle`
    undefined" decision.
  - **Tier 3** — inert crash-avoidance stubs for media playback + the full Web Audio
    node graph, WebRTC, Payment Request, Web Speech, the sensor family, `Notification`
    (`permission:'denied'`), Popover, Fullscreen, Picture-in-Picture, View Transitions,
    and the WebHID/USB/Serial/MIDI/Bluetooth/WebXR/WebGPU device family.

  Async completions route through the provable-idle settle audit (ADR 0004). Still
  permanent non-goals: pixels/layout/geometry, real media/GPU rendering, real Workers/
  WebSocket/persistent storage, and Shadow-DOM style scoping.

### Changed

- **[docs/comparison.md](docs/comparison.md)** reframes the JavaScript "what it
  forfeits" ceiling: with the broad Web API surface, pages rarely crash at boot, so
  the forfeit is now specifically pixels/layout and second-thread features — not the
  API surface itself.

## v0.18.0 — 2026-07-03

### Changed

- **JavaScript rendering is on by default.** The opt-in `--js` flag is replaced by
  an opt-out **`--disable-js`**, and the `read`/`click`/`submit_form` `render` arg now
  **defaults to on** — a read runs the page's JavaScript unless the caller passes
  `render=false` (or the server was started with `--disable-js`). For a robustness-first
  tool, seeing SPA content by default beats making every agent opt in per call; renders
  are ~2–10 ms (ADR 0004) and pages with no scripts still pay ~nothing. The `render`
  MCP arg became tri-state (omitted = render, explicit `false` = skip). Migration:
  drop `--js` from your launch args (JS is already on); add `--disable-js` for the
  static-only path.

### Added

- **Composed, encapsulating Shadow DOM (Phase 23, ADR 0005).** Replaces the flat,
  non-encapsulating model. Each shadow root is a detached subtree (page-JS
  `document.querySelector` respects the boundary); a compose pass flattens it —
  resolving `<slot>` distribution — into the light tree for extraction, fixing content
  loss where a component's `shadowRoot.innerHTML` used to destroy slotted children.
  Events cross the boundary correctly (composed path + `target` retargeting +
  `composedPath()`, `bubbles`/`composed` kept orthogonal); interact and `wait_for`
  pierce the boundary to reach shadow-rendered controls; declarative Shadow DOM
  (`<template shadowrootmode>`) renders on the static no-JS path; closed roots are
  hidden from page JS but still composed into output.

### Performance

- **Provable-idle settle (ADR 0004).** Every JS work source was already
  instrumented (network via the in-flight bracket, macrotasks via the wrapped
  timer table) except goja's native `setImmediate` — now wrapped. With that
  leak closed, zero in-flight requests plus zero live timers *proves* the page
  cannot change again, so the settle closes after a ~1 ms confirmation tick
  instead of waiting out the 60 ms quiet window; pages with anything armed
  keep the unchanged 60 ms-quiet-after-last-activity heuristic (now polled at
  5 ms granularity). Render benchmarks: minimal 63.3 ms → 1.8 ms, React
  81.6 ms → 9.4 ms, Vue 65.7 ms → 3.5 ms; synchronous-only `interact`
  dispatches stop paying the window per click. `SettledIdle` is recorded in
  render diagnostics.
- **Concurrent script-body prefetch.** The initial external `<script src>`
  bodies fetch in parallel (4 bounded workers, same counting/budgeted/
  SSRF-guarded transport) while execution stays strictly document-ordered:
  N scripts cost max(RTT) instead of sum(RTT), with identical request
  accounting (nothing speculative is fetched).
- **Content-only compile caches.** Identical script bytes share one compiled
  `goja.Program` regardless of URL or page position (external scripts now
  compile under their absolute URL — better stack traces than the positional
  `script-N.js`), and the esbuild bundle key ignores the page URL's
  query/fragment, so `?utm=`-style variants stop re-running the whole
  module-graph build.
- **Markdown memo on the page cache.** Repeat reads and pagination cursor
  pages within the 60 s cache TTL skip reduce+emit+defang entirely (warm read
  657 µs → 11 µs, allocations −99%). The memo lives and dies with its cache
  entry; sessions, waited renders, and credentialed one-shots are unaffected.
- **`--js-concurrency`** (new flag, default auto = CPU count clamped to
  4..16): the one-shot render semaphore was silently pinned at 4; it now
  scales with cores. `--js-prewarm` stays at 4; `MaxIdleConnsPerHost` rises
  8 → 16 to match.
- **Crossbench was measuring unblink's politeness limiter, not its engine**:
  with the limiter at its old 5 req/s default, the single-host cache-busted
  corpus paced every render at ~200 ms and pinned sequential throughput at
  exactly 5 pages/s. `docs/comparison.md` now also publishes the
  concurrent-throughput row. Re-measured medians: SPA renders ~2–10 ms (was
  ~200 ms), ~249 pages/s sequential / ~827 pages/s at 8-way concurrent (was
  ~5 pages/s).

### Changed

- **The per-host politeness rate limiter is now opt-in** (`--rate-limit`
  defaults to `0`/off, was 5 req/s): out of the box unblink runs
  like-for-like with the other benchmarked tools, none of which ships a
  crawl limiter, and throughput is bounded by the site rather than by
  unblink. Set `--rate-limit 5` (with `--rate-burst`) to restore the old
  polite-crawl behavior. Live-session JS subrequests stay bounded either
  way: the rolling window (300/min) enforces the same 5/s average the old
  default did.

### Added

- **Cross-tool benchmark harness** (`make crossbench`): `scripts/membench`
  generalized from a chromedp-only baseline into a generic stdio MCP client +
  per-tool adapters that measure every tool in `docs/comparison.md` — unblink,
  raw headless Chromium, Playwright MCP, Charlotte (npx, pinned versions), and
  Obscura/Lightpanda (external binaries, skipped with a note when absent) — on
  identical local fixtures. New metric family: **token cost per task**
  (`read-article` / `read-full` / `orient`), counted with an offline
  o200k_base BPE tokenizer, bytes alongside, every output sentinel-validated
  so an error can never score as token efficiency — and whole-document reads
  tail-verified, so a silent truncation can't either (this caught Obscura's
  ~4 KB snapshot cap). Plus a **content-boundary probe** (`-safety`) that
  measures what an untrusted page smuggles into the model — hidden-instruction
  text, image-beacon exfiltration, PDF handling, untrusted-content fencing —
  across each tool's read surface. And `-probe` (dump a
  server's tool list), `-selfcheck` (directional sanity assertions),
  `-dump-dir`, `-debug-mem`, and a generated provenance header.
  `docs/comparison.md`'s measured sections are now produced by this harness.
- **golangci-lint** as the linter (`.golangci.yml`, `make lint`, CI step):
  default linter set, errcheck relaxed for idiomatic `Close` and test HTTP
  handlers, `third_party/` exempt. Its first run removed one piece of dead
  code (`js.Context.settleThenClose`).

### Fixed

- **Off-screen hidden text leaked through the default article read.** The
  safe-output hidden-strip ran *after* readability extraction, but readability
  drops inline `style` attributes while cleaning — erasing the
  off-screen-positioning signal (`left:-9999px`, `clip:rect(0…)`) the strip
  keys on, so injected "screen-reader" text survived into `read`'s default
  article Markdown (`display:none`/`aria-hidden`/`hidden` were already stripped,
  and `mode:"full"` stripped all channels). The strip now also runs *before*
  readability, on a clone, guarded by a cheap `dom.HasHidden` pre-scan so pages
  with no hidden nodes skip the copy (article hot path unchanged: +0.0% B/op
  on hidden-free pages). Surfaced by the cross-tool content-boundary probe.

### Changed

- Removed the Go Report Card badge — the service is retired (it recommends
  golangci-lint, adopted above). GitHub Actions bumped to current majors
  (checkout v7, goreleaser-action v7, docker actions v4; all Node 24).

## v0.17.1 — go install fix

### Fixed

- `go install github.com/christopherdavenport/unblink/cmd/unblink@latest`
  works: the vendored PDF fork is now imported by its in-repo path
  (`…/unblink/third_party/pdf`) instead of a `replace` directive, which
  `go install <module>@version` refuses
  ([ADR-0001](docs/decisions/0001-pdf-extraction-library.md), Amendment 2).

## v0.17.0 — Publication readiness (Phase 21)

The pre-first-public-release pass: close the highest-frequency browser-API gaps,
bound the last unbounded resource, make the failure modes visible and gated, and
back the footprint pitch with measured numbers.

### Added

- **Browser API tier-1** (a new prelude layer): `TextEncoder`/`TextDecoder`,
  `atob`/`btoa`, `performance.now`/`mark`/`measure`; a truthful constant viewport
  (1280×720@1x: `innerWidth`/`screen`/`devicePixelRatio`/`visualViewport`) and a
  real `matchMedia` evaluator (replacing the always-false stub); constructible
  `Headers`/`Request`/`Response`/`Blob`/`File`/`FormData` with a real `fetch`
  `Response` (arrayBuffer/blob/clone/redirected) and FormData multipart bodies;
  `postMessage` + functional `MessageChannel`; `DOMParser`/`XMLSerializer`/`Range`;
  constructable `CSSStyleSheet` + `adoptedStyleSheets` (Lit's feature-detect);
  `WeakRef`/`FinalizationRegistry` and an `Intl` crash-avoidance shim;
  `document.title` setter, `document.URL`/`referrer`/`currentScript`/
  `getElementsByName`; a fuller `navigator` (`webdriver`, `sendBeacon`,
  `userAgentData`, …).
- **`interact` keyboard support**: `event=keydown`/`keyup`/`keypress` now dispatch
  a real `KeyboardEvent` (via a new optional `key` param), fire the full
  keydown→input→keypress→keyup sequence for printable keys, and submit a form on
  `Enter` inside it — so `{event:"keydown", value:"query", key:"Enter"}` drives a
  search box end to end.
- **JS memory guard** (`--js-memory-limit`, default 1024 MiB): a process-heap
  watchdog interrupts runaway page JS before it can OOM the server
  ([ADR-0003](docs/decisions/0003-js-memory-guard.md)).
- **Timer clamp + `timers_pending`**: a one-shot `setTimeout` past the render
  budget is clamped to fire in-budget so its deferred content still materializes;
  any timer still pending at snapshot is reported.
- **Measured footprint**: `make membench` (a separate module) benchmarks unblink
  vs. headless Chromium; `docs/comparison.md` gains real numbers (≈15× lighter and
  faster to start on identical pages).
- `SECURITY.md`, `CONTRIBUTING.md`, this changelog, a README MCP-client-config
  section, and a consolidated flags table.
- **Docker images**: multi-arch (`linux/amd64` + `linux/arm64`, distroless
  static, nonroot) images published to `ghcr.io/christopherdavenport/unblink`
  (`:X.Y.Z` and `:latest`) by goreleaser on every tag.
- **MCP registry listing**: `io.github.ChristopherDavenport/unblink` —
  `server.json` plus a publish workflow that stamps the version from the tag,
  waits for the ghcr image to be pullable, and publishes via `mcp-publisher`
  with GitHub OIDC.
- **Claude Code plugin manifest** (`.claude-plugin/plugin.json`): a docker-run
  MCP server entry whose image tag is auto-synced to each release by a bot
  commit on `main`.
- **Release pipeline hardening**: the test suite + eval gate now block
  goreleaser on tag pushes; GitHub Releases get auto-generated notes; README
  badges; issue/PR templates; Dependabot for Go modules and Actions (goja pins
  excluded per [ADR-0002](docs/decisions/0002-dependency-pinning-policy.md)).

### Changed

- `navigator.webdriver` is `true` by default (unblink is automation and
  self-identifies); `--tls-mimic` flips it to `false` for fingerprint parity.
- The eval gate grew from 40 to 47 must-pass cases, now covering the
  starved-render diagnostics, `net_denied`, keydown, Svelte, Lit, and a tier-1 API
  smoke page.

See the [Phase 21 section of docs/architecture.md](docs/architecture.md) for the
full design notes.
