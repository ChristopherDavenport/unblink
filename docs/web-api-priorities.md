# unblink Web API priorities

A standing, prioritized catalog of the Web platform surface page JS sees, and what to build
next. It exists because that surface has grown reactively — each API was added the day a
real page (mobalytics, Reddit, an Angular/HN clone) crashed during hydration without it —
and there was no single place that ranked the remaining work, recorded what is deliberately
out, or gave a shared rubric for deciding. This is that place. It complements the completed
`## Roadmap` phases in [architecture.md](architecture.md) and the [ADRs](decisions/); it does
not restate them.

The comprehensiveness anchor is MDN's [Web API index](https://developer.mozilla.org/en-US/docs/Web/API).
Every category there resolves to a tier, a non-goal, or the engine ceiling below — see the
[coverage map](#coverage-map-every-mdn-category) at the end.

## Why this document — the one metric

unblink does **semantic reduction, not rendering** (see [comparison.md](comparison.md)). An
AI consuming the output needs structured *meaning*, not pixels. So there is exactly one
question that ranks a Web API:

> **Does providing it cause more real page *content* to materialize in the extracted
> Markdown?**

Not fidelity. Not spec-compliance. Not visual correctness. An API earns priority only insofar
as its *absence* crashes hydration, aborts a framework's boot, or leaves content that would
otherwise render blank. An API that a page uses purely for layout, animation, telemetry, or
pixels earns nothing — implementing it moves no text into the output. This metric is what
makes the rest of the document mechanical.

## Three treatments: FULL / STUB / LEAVE-UNDEFINED

Every API resolves to one of three treatments. The distinction is load-bearing: it separates
"expensive and necessary" from "cheap and necessary" from "cheaper to omit," and it dissolves
the false choice between "implement the whole feature" and "let the page crash."

- **FULL** — content depends on the API *working*. Build it for real. Examples already
  shipped: `URL`/`URLSearchParams` (`internal/js/globals.go`), `fetch`/`XMLHttpRequest`
  (`internal/js/prelude_api.go`), `MutationObserver` (`internal/js/mutationobserver.go`),
  `document.forms`/`requestSubmit` (`internal/js/forms.go`).

- **STUB** — the API's *absence* throws and aborts boot, but content does **not** depend on
  it doing anything. A cheap inert or synthetic value keeps hydration alive so the DOM that
  *does* hold content still renders. This is an established pattern in the engine, not a new
  idea: `WebSocket` is a connection-less stub whose own comment reads *"real sockets are a
  permanent non-goal … Constructing one no longer throws … so … logic degrades gracefully
  instead of crashing hydration"* (`internal/js/globals.go:440`); `Worker`/`SharedWorker` are
  inert (`globals.go:472`); `IntersectionObserver`/`ResizeObserver` emit a single synthetic
  "visible / zero-size" entry (`globals.go:520`); `getComputedStyle` returns empty strings
  (`globals.go:509`); element geometry returns a frozen zero-rect (`internal/js/proto.go`).

- **LEAVE-UNDEFINED** — the API is *feature-detected* by real code, and defining it does more
  harm than good (it makes an app take a broken or heavier path it would otherwise skip). The
  poster child is `crypto.subtle`, left undefined on purpose *"so libs feature-detect"*
  (`globals.go:597`).

**The reframe this enables:** several APIs that read as blanket "permanent non-goals" in
[architecture.md](architecture.md) only need a **STUB**, never a FULL implementation. *No
persistent IndexedDB* does not require that `indexedDB` be a `ReferenceError`. *No canvas
pixels* does not require that `canvas.getContext` be `undefined`. The permanent non-goal is
the **full feature** (real cross-thread storage, real rasterization); a boot-survival stub
that lets feature-detection and constructor calls proceed is cheap and squarely in scope.
Wherever a tier below reclassifies a "non-goal," it is proposing the stub, never the feature —
the two are called out side by side in [Non-goals](#non-goals-with-revisit-triggers) so this
document reads as consistent with the architecture, not a reversal of it. The one genuine
reversal (`crypto.subtle`) is marked as such.

## How to read the tiers

| Column | Meaning |
|---|---|
| **API** | The interface/global, as page JS names it. |
| **Treatment** | FULL, STUB, or LEAVE-UNDEFINED (above). |
| **Failure mode** | What breaks *today* without it — the content that goes missing. |
| **Effort** | Rough implementation cost: low (a prelude shim / a few methods), med (a new file or a broad API), high (not attempted at the shim layer). |
| **settle-audit** | Present on every **async** primitive. Per [ADR 0004](decisions/0004-provable-idle-settle.md), any new source of future JS work (network callbacks, event emitters, schedulers) **must** route through the pending bracket / timer audit (`internal/js/async.go`, `internal/js/context.go`) or provable-idle settle closes early and silently truncates content. This is a hard merge gate, not a nicety. |

Tiers are ordered by `(content yield × site frequency) ÷ effort`.

## Tier 0 — shipped (the render baseline)

Recorded so it is not silently regressed and so the "why it mattered" is not lost. Each of
these was a real hydration blocker before it landed; the phase that shipped it is in
parentheses. Full detail lives in the [architecture roadmap](architecture.md).

| API | Treatment | Shipped |
|---|---|---|
| `URL` / `URLSearchParams` | FULL (pragmatic parser) | Phase 8 — react-router `ReferenceError` at hydration |
| `crypto.getRandomValues` / `randomUUID` | FULL (`Math.random`-backed, non-cryptographic) | Phase 15 — react-aria uuid crash |
| `structuredClone` | FULL | Phase 8 |
| `matchMedia` + constant viewport | FULL evaluator vs 1280×720@1x | Phase 21 — was blanket-false, every page took its narrowest branch |
| `IntersectionObserver` / `ResizeObserver` | STUB (synthetic visible/zero-size) | Phase 8 — lazy/infinite-scroll content stayed blank |
| `MutationObserver` | FULL (Go-backed, real mutations) | Phase 8 · settle signal |
| `fetch` / `XMLHttpRequest` | FULL over transport | Phase 8/19 · settle-audit |
| `Headers`/`Request`/`Response`/`Blob`/`File`/`FormData` | FULL | Phase 21 |
| `TextEncoder`/`TextDecoder`, `atob`/`btoa` | FULL (UTF-8) | Phase 21 |
| `performance` (`now`/`mark`/`measure`) | FULL (marks/measures only) | Phase 21 |
| timers, `requestAnimationFrame`, `requestIdleCallback`, `queueMicrotask` | FULL (numeric ids, budget-clamped) | Phase 8/21 · settle-audit |
| `AbortController`/`AbortSignal` | FULL | Phase 8 |
| `postMessage`/`MessageChannel`/`MessagePort` | FULL (same-context FIFO) | Phase 21 |
| `customElements` + composed Shadow DOM | FULL | Phase 8/23 ([ADR 0005](decisions/0005-shadow-dom-composition.md)) |
| `DOMParser` / `XMLSerializer` / `Range.createContextualFragment` | FULL | Phase 8/21 |
| `document.forms` / `submit` / `requestSubmit` | FULL (submit → `pendingNav`) | Phase 8 — clears self-submitting bot-check interstitials |
| `document.implementation.createHTMLDocument` | FULL (inert doc) | — Angular `DomSanitizer` dropped all `[innerHTML]` without it |
| `HTMLMediaElement`/`Audio`/`Video` constructors | STUB (ctor chain only) | — zone.js patch `ReferenceError` without them |
| `WebSocket` / `Worker` / `SharedWorker` | STUB (inert) | Phase 8 |
| `Intl` (`DateTimeFormat`/`NumberFormat`/…) | STUB (en-US crash-avoidance; goja has no native `Intl`) | Phase 21 |
| `getComputedStyle`, element geometry, `setPointerCapture` | STUB (empty/zero/no-op) | Phase 8/14 |
| dynamic `import()` + lazy chunk loading | FULL | Phase 16 · settle-audit |

## Tier 1 — ✅ shipped (Phase 24)

The original "next" working list — all eight clusters shipped in Phase 24, in five
batches (`internal/js/prelude_api.go` for the shims, `internal/js/proto.go` +
`internal/js/domapi.go` for the element gaps). The MCP-level regression net is the
`js-api-smoke` eval case (markers `api-11`…`api-17`); the settle-critical async paths
each carry a test that writes its content only from the async completion, so a broken
[ADR 0004](decisions/0004-provable-idle-settle.md) route would fail the assertion
(`internal/js/webapi_tier1_test.go`). **Tier 2 (below) is now the working list.**

| API | Treatment (as built) | Failure mode it fixed |
|---|---|---|
| `CSS.escape` / `CSS.supports` (the `CSS` namespace) | FULL — WHATWG serialize-an-identifier; `supports` optimistic | selector and CSS-in-JS libraries call `CSS.escape(id)` at import and `ReferenceError` before rendering anything |
| `canvas.getContext('2d')` → inert 2D context | STUB — no-op draw calls, `measureText`→`{width:0}`; `webgl`/`webgpu`→`null`; landed on the shared `HTMLElement.prototype` (guarded by tagName), since instances don't carry `HTMLCanvasElement.prototype` | `canvas.getContext(...)` was `TypeError: not a function`; charting/analytics/fingerprint libs took the whole app down |
| `FileReader` | FULL — `readAsText`/`readAsDataURL`/`readAsArrayBuffer`/`readAsBinaryString` over `Blob.__bytes`; completes via the wrapped timer | upload-preview flows and client-side file/blob parsers threw |
| `indexedDB` → in-memory, non-persistent store | STUB — `open`/stores/transactions backed by `Map`s, discarded with the runtime; **data ops run synchronously, only the success event defers** (via the wrapped timer) so order is race-free | `ReferenceError` at boot in offline-first PWAs. The **stub of the IndexedDB non-goal**, not persistent IndexedDB (see Non-goals) · **settle-audit** |
| `EventSource` (SSE) | STUB — `error`→CLOSED via the wrapped timer, no auto-reconnect (transport is request/response, not streaming) | `ReferenceError` on SSR-first pages that layer live updates on top · **settle-audit** |
| `ReadableStream` / `WritableStream` / `TransformStream` (+ queuing strategies) | FULL — buffer-backed, eager; `read()` resolves from an in-memory queue via microtask (no timer needed) | `Response.body` consumers and streaming-parse libraries threw · **settle-audit** (microtask) |
| `navigator.serviceWorker` / `clipboard` / `permissions` / `geolocation` / `mediaDevices` | STUB — `serviceWorker.register()`/`ready` **resolve** (no await-hang); the rest reject/deny/return empty | accessing these threw or hung; PWAs aborted registration and never mounted |
| Element traversal gaps: `lastElementChild`, `getAttributeNode`, `namespaceURI`/`prefix` | FULL — `Element.prototype` additions in `proto.go` | libraries walking the tree hit `undefined` and misbehaved |

## Tier 2 — situational & upgrades

Tier 2 shipped in **Phase 25** (marked ✅ below), including `crypto.subtle`. One item stays
deliberately open: the full-WHATWG `URL` upgrade (policy: only on a demonstrated break).
Regression nets for the shipped items: `internal/js/webapi_tier2_test.go` and the
`js-api-smoke` eval markers `api-18`…`api-25`.

- ✅ **`crypto.subtle` — broad, Go-backed** (Phase 25, [ADR 0006](decisions/0006-crypto-subtle.md)) —
  reverses the earlier "leave undefined" decision. Byte primitives in `internal/js/subtle.go`
  (`crypto/*` stdlib + real `crypto/rand`), WebCrypto object model in `preludeAPIJS`: `digest`
  (SHA-1/256/384/512), HMAC `sign`/`verify`, AES-GCM/CBC/CTR `encrypt`/`decrypt`, PBKDF2/HKDF
  `deriveBits`/`deriveKey`, symmetric `generateKey`/`importKey`/`exportKey`. **RSA/ECDSA and
  wrap/unwrap reject** (never a sync throw), so a feature-detecting app degrades to a catchable
  rejection. `getRandomValues` re-pointed at `crypto/rand`. No off-loop I/O → settle-neutral.
- **`URL` / `URLSearchParams` → full WHATWG state machine.** Present but a pragmatic parser
  (`globals.go`). Upgrade only on a demonstrated parse divergence, not speculatively.
- ✅ **`DOMMatrix`/`DOMPoint`/`DOMRect`/`DOMQuad`/`Path2D`** (Phase 25) — real pure-JS math
  (matrix compose/inverse/`transformPoint`); `Path2D` records ops, consumed no-op by the canvas
  stub.
- ✅ **`document.fonts` / `FontFace`** (Phase 25) — `fonts.ready` resolves immediately, `check()`
  optimistic, so `await document.fonts.ready` never stalls first paint.
- ✅ **`Element.animate()` / Web Animations (`Animation`/`KeyframeEffect`)** (Phase 25) — a
  finished animation; `finished`/`ready` resolve and `onfinish` fires once via the wrapped timer.
- ✅ **Custom-element lifecycle + `ElementInternals`** (Phase 25) — `disconnectedCallback` now
  fires on removal (Go-side hook in `onMutate`→`disconnectTree`), and `attachInternals()` returns
  an inert `ElementInternals`. *`adoptedCallback` deferred* (needs cross-document adoption, which
  the single-document engine barely exercises).
- ✅ **Navigation API** (Phase 25) — `navigation.navigate`/`currentEntry`/`entries`, and a
  `navigate` event whose `intercept({handler})` runs the router's view update. Same-document
  only; never a real load (consistent with `pushState` not firing `popstate`).
- ✅ **`cookieStore`** (over `document.cookie`), **`reportError`** (→ `console.error` diagnostics),
  **`TextEncoderStream`/`TextDecoderStream`** (Phase 25). *`CompressionStream`/`DecompressionStream`
  deferred* — real gzip/deflate needs a Go-side codec and rarely gates textual content.

## Tier 3 — niche crash-avoidance stubs (✅ shipped Phase 26)

Originally LEAVE-UNDEFINED-until-a-page-crashes. Phase 26 flipped that to **proactive
crash-avoidance stubs** across the niche surface: these APIs essentially never gate textual
content, but a page that touches one *unconditionally* at boot (a `video.play()`, an
`AudioContext` visualizer, `new Notification(...)`, a WebGPU probe) would abort hydration
without them. Every stub is inert — construction/access never throws, promise calls resolve
empty or reject *catchably*, and no device/media/GPU work happens. All in `preludeAPIJS`;
regression nets: `internal/js/webapi_tier3_test.go` and `js-api-smoke` markers `api-26`/`api-27`.

- **Media & Web Audio** — `HTMLMediaElement` `play`(→resolved)/`pause`/`load`/`canPlayType`/
  `addTextTrack`/`captureStream`, `Audio()`, `MediaSource`/`SourceBuffer`, and `AudioContext`/
  `OfflineAudioContext` with the full inert node graph (`createGain`/`createOscillator`/
  `createAnalyser`/… → connect/disconnect/start/stop no-ops) + `decodeAudioData`.
- **navigator device APIs** — `bluetooth`/`usb`/`serial`/`hid` (`requestDevice` rejects),
  `xr`/`gpu` (no session/adapter), `getGamepads`→`[]`, `getBattery`, `requestMIDIAccess`,
  `wakeLock`, `locks` (grants immediately — single context), `presentation`, `ink`, `storage`,
  `credentials`, `contacts`, `setAppBadge`, `share`/`canShare`, `vibrate`.
- **Niche constructors** — `RTCPeerConnection`/`RTCSessionDescription`/`RTCIceCandidate`/
  `MediaStream`, `PaymentRequest`, `speechSynthesis`/`SpeechSynthesisUtterance`/
  `SpeechRecognition`, the Generic Sensor family, **`Notification`** (present,
  `permission:'denied'` — the escape hatch), `BarcodeDetector`, `EyeDropper`, `IdleDetector`,
  `WebTransport` (inert like `WebSocket`), `CloseWatcher`, `ToggleEvent`.
- **Element/document interaction** — Popover (`showPopover`/`hidePopover`/`togglePopover` no-ops;
  content is already in the light tree), Fullscreen (`requestFullscreen`→resolved,
  `fullscreenElement`=null), Picture-in-Picture (rejects), Remote Playback (`video.remote`),
  Pointer Lock, Storage Access, and **View Transitions** (`document.startViewTransition` runs the
  update callback synchronously so a router's new view materializes, then resolves). Background
  Sync/Periodic Sync/Push hang off the (inert) serviceWorker registration.

**Still not stubbed** (add reactively if a target needs it): Encrypted Media Extensions
(`requestMediaKeySystemAccess`), Web NFC (`NDEFReader`), File System Access handles, and the
Privacy Sandbox proposals (Topics / Attribution / Fenced Frames / Shared Storage) — all rare and
none on a first-paint textual-content path.

## Non-goals (with revisit triggers)

These stay out. Restated to stay consistent with [architecture.md](architecture.md) (lines
~595–642) so nothing here contradicts it. Note the STUB/feature split from
[the reframe](#three-treatments-full--stub--leave-undefined): a boot-survival stub for one of
these is *in scope* (and may appear in a tier above); the **full feature** is what is
permanent.

- **Real layout / geometry / CSSOM computed values.** `getBoundingClientRect`/`offset*`/
  `getComputedStyle` return zeros/empty; no pixels are ever computed. *Revisit:* never — this
  defines the product (no layout engine, no cgo/Chromium).
- **Canvas / WebGL / WebGPU *pixels* and readback.** The Tier 1 2D-context *stub* accepts draw
  calls and discards them; it never rasterizes. `toDataURL`→empty, `getImageData`→zeros,
  WebGL/WebGPU contexts→`null`. *Revisit:* never (would need a rasterizer; the content is not
  in the bitmap anyway).
- **Real Workers / real WebSocket / *persistent* IndexedDB / Service Worker execution.** Out by
  construction — a `*goja.Runtime` is single-goroutine and never shared (the quarantine in
  [architecture.md](architecture.md)), and there is no cross-render persistence. Stubs keep boot
  alive; the real features do not exist. *Revisit trigger:* a target whose first-paint content
  genuinely requires cross-thread compute or server-push — accept that unblink cannot see it and
  route the caller to a session/`.json`-API path instead.
- **Shadow-DOM style scoping** (`:host`/`::slotted`/`::part`), slot reprojection, manual slot
  assignment, `slotchange` timing, closed-mode privacy *from extraction*. Composition and
  cross-boundary events are shipped ([ADR 0005](decisions/0005-shadow-dom-composition.md)); only
  the CSS-level behavior is out. *Revisit:* needs a CSS cascade engine.
- **Screenshots / pixels / visual verification of any kind.** *Revisit:* never.
- **Turnstile-class interactive anti-bot challenges and server-side proof-of-work.** A
  *page-computed* self-submitting challenge is already cleared via programmatic form submission
  (`forms.go`); an interactive or server-verified one is not. *Revisit:* would need a `chromedp`
  escape hatch, which breaks the pure-Go / no-Chromium bet.
- **`cgo` / Chromium / Node / V8.** The single-static-binary constraint underneath all of the
  above. Any API that *requires* one of these is out by definition.

## The engine ceiling (goja parser — not API-shimmable)

Some pages fail for reasons **no prelude shim can fix**, because the ceiling is the JS engine,
not the API surface. Called out here so a parser gap is never mis-filed as a missing API.

- **Syntax goja rejects:** async generators, top-level `await`, decorators, `using`, import
  attributes. esbuild downleveling (`internal/js/modules.go`, `scripts.go`) mitigates *some*
  classic-script cases but not all, and it collides with the next item.
- **The goja `WeakMap` bug:** repeated `.set` on the same key does not overwrite, which corrupts
  esbuild-lowered private fields — a latent hazard in module downleveling
  (`docs/specs/js-classic-script-downleveling-and-crypto.md`, architecture.md ~line 307).
- **`crypto.subtle` gap** overlaps here only in that its *cryptographic* strength can't come from
  `Math.random`; the digest proposal in Tier 2 sidesteps that via the Go stdlib.

These move only with a reviewed goja bump under [ADR 0002](decisions/0002-dependency-pinning-policy.md)
(pseudo-version pins are deliberate; a bump is its own tested change), never a prelude edit.
The memory guard ([ADR 0003](decisions/0003-js-memory-guard.md)) is the other cross-cutting
constraint: anything that buffers (Streams, `FileReader`, the IndexedDB stub) lives under the
process heap watchdog and `--js-memory-limit`.

## Working-through checklist (how to add an API safely)

For each API pulled off a tier:

1. **Confirm the metric.** Name the real page and the content that appears *only* once the API
   exists. If you can't, it belongs in Tier 3 or a non-goal.
2. **Pick the treatment.** FULL only if content depends on it *working*; otherwise STUB;
   otherwise leave it undefined. Prefer the cheapest treatment that unblocks the content.
3. **Place it.** Prelude JS in `preludeJS` (`globals.go`) or `preludeAPIJS` (`prelude_api.go`);
   Go-native only if it must read/mutate the `*html.Node` tree (`proto.go`/`bridge.go`).
4. **If async, wire the settle audit.** Route every callback/emitter through the pending
   bracket or timer surface ([ADR 0004](decisions/0004-provable-idle-settle.md)). An async API
   that skips this silently truncates renders — it is the single most common way to break
   correctness here.
5. **Test against a real bundle.** Add a case to `internal/js/spa_engine_test.go` or
   `internal/js/webapi_misc_test.go` using a pinned fixture (`testdata/frameworks/`), asserting
   the *content* now materializes — not just that the symbol is defined.
6. **Record it.** A roadmap phase in [architecture.md](architecture.md); an ADR if it reverses a
   prior decision (as `crypto.subtle` would); and move its row here from a tier to Tier 0.

## Coverage map (every MDN category)

Confirming nothing from the [MDN Web API index](https://developer.mozilla.org/en-US/docs/Web/API)
is silently dropped.

| MDN category | Verdict |
|---|---|
| DOM core, Traversal & Selection, HTML elements, Web Components | **Tier 0** (mostly shipped); `disconnectedCallback` + `ElementInternals` **shipped Phase 25** (`adoptedCallback` deferred); small traversal gaps **shipped Phase 24** |
| Events & event handling | **Tier 0** (full model, composed path); `DragEvent`/`DataTransfer` → **Tier 3** |
| Fetch, XHR, Beacon | **Tier 0** |
| Streams, Compression | **Tier 0** (Streams, Phase 24); `Compression`/`DecompressionStream` **deferred** (needs Go codec) |
| Encoding (`TextEncoder`/`Decoder`) | **Tier 0**; `TextEncoder`/`DecoderStream` **shipped Phase 25** |
| URL / URLSearchParams / URLPattern | **Tier 0**; WHATWG upgrade + `URLPattern` **Tier 2** |
| Web Storage | **Tier 0**; `cookieStore` **shipped Phase 25** |
| IndexedDB, Cache | in-memory **stub shipped Phase 24**; persistent feature **Non-goal** |
| Service Workers, Background Sync/Fetch, Push | `serviceWorker` register-stub **shipped Phase 24**; execution **Non-goal**; rest **Tier 3** |
| Workers & Worklets | inert **stub Tier 0**; real threads **Non-goal** |
| Web Crypto | `getRandomValues`/`randomUUID` **Tier 0** (now `crypto/rand`-backed); `subtle` digest/HMAC/AES/PBKDF2/HKDF **shipped Phase 25** (ADR 0006); RSA/ECDSA reject |
| Observers (Intersection/Resize/Mutation/Performance) | **Tier 0**; `ReportingObserver`/`PressureObserver` **Tier 3** |
| Performance & Timing | **Tier 0** (marks/measures); resource/paint timing **Tier 3** |
| Navigation & History, Location | **Tier 0**; Navigation API **shipped Phase 25** |
| CSSOM & CSS Typed OM | `getComputedStyle`/`CSSStyleSheet` **stub Tier 0**; `CSS.escape`/`supports` **shipped Phase 24**; real cascade **Non-goal** |
| CSS Animations / Web Animations | `Element.animate` **stub shipped Phase 25**; real timing **Non-goal** |
| Geometry (`DOMMatrix`/`DOMPoint`/`DOMRect`/`DOMQuad`/`Path2D`) | **shipped Phase 25** (pure-math) |
| Canvas 2D | `getContext('2d')` **stub shipped Phase 24**; pixels **Non-goal** |
| WebGL / WebGPU / OffscreenCanvas | **Non-goal** (`getContext`→null) |
| SVG DOM | base `instanceof` **Tier 0**; geometry/animated-value DOM **Tier 3** |
| Media (HTMLMediaElement playback, Web Audio, MSE, EME, Media Capabilities) | ctors **stub Tier 0**; playback + Web Audio + MSE **stub shipped Phase 26**; EME not stubbed |
| WebRTC, Media Capture, Screen Capture, Media Recording | WebRTC **stub shipped Phase 26**; Screen Capture (`getDisplayMedia`) Phase 25; Media Recording not stubbed |
| Messaging: postMessage/MessageChannel/BroadcastChannel | **Tier 0**; SSE (`EventSource`) **stub shipped Phase 24**; WebSocket **stub Tier 0**; WebTransport **stub shipped Phase 26** |
| Notifications, Badging | `Notification` + `setAppBadge` **stub shipped Phase 26** (permission denied) |
| File & FileReader, File System Access | `Blob`/`File`/`FormData` **Tier 0**; `FileReader` **shipped Phase 24**; File System Access **Non-goal** (no real FS) |
| Clipboard, Drag & Drop, Pointer Lock, Touch, EditContext | Clipboard **stub shipped Phase 24**; Pointer Lock **shipped Phase 26**; Drag & Drop / Touch / EditContext **Tier 3** |
| Device & Sensors (Geolocation, Orientation, Generic Sensor, Battery, Gamepad, Idle) | Geolocation **stub shipped Phase 24**; Sensors / Battery / Gamepad / Idle **stub shipped Phase 26**; Orientation events simply never fire |
| WebHID / USB / Serial / NFC / MIDI / Bluetooth | HID/USB/Serial/MIDI/Bluetooth **stub shipped Phase 26**; NFC (`NDEFReader`) not stubbed |
| Auth & Credentials (WebAuthn, Credential Mgmt, FedCM) | Credential Management **stub shipped Phase 26** (get→null); WebAuthn / FedCM **Tier 3** |
| Trusted Types, Reporting, Privacy Sandbox | **Tier 3** (not stubbed) |
| Payment, Presentation, Picture-in-Picture, Fullscreen, Remote Playback | **stub shipped Phase 26** |
| Speech, Ink, Barcode/Shape Detection, EyeDropper, Web Locks, View Transitions, Close Watcher, Popover, WebXR | **stub shipped Phase 26**; Scheduling / Network Information / User Preferences **Tier 3** |
| Screenshots / pixels of any kind | **Non-goal** |
| Turnstile-class interactive challenges / server-side PoW | **Non-goal** |
