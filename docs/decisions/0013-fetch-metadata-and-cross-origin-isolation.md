# ADR 0013: Fetch Metadata (Sec-Fetch-*) and cross-origin isolation fidelity

Date: 2026-07-04
Status: accepted

## Context

Continuing the browser-parity series (CORS/SRI in ADRs 0011/0012), two response/
request-header fidelity gaps remained:

- **Sec-Fetch-\*** (Fetch Metadata Request Headers). A real Chrome sends
  `Sec-Fetch-Site/Mode/Dest` (and `Sec-Fetch-User` on user-activated navigations) on
  every http(s) request, describing the request's context; servers gate on them
  (resource-isolation policies, some anti-bot). unblink sent only a **static**
  `document/navigate/none/?1` set, only on the **primary** fetch, and only under
  `--tls-mimic` (`setChromeMimicHeaders`). Page-JS subrequests (scripts, modules,
  fetch/XHR) sent none.
- **Cross-origin isolation.** `window.crossOriginIsolated` and `window.isSecureContext`
  were absent, so page code branching on them (WebCrypto, high-res timers, feature
  detection) took a wrong path.

Both need the document's response headers (COOP/COEP), which did not reach the engine.

## Decision

**Shared plumbing:** thread the document's HTTP response headers into the engine —
`Env.ResponseHeaders http.Header` (`internal/js/env.go`), populated from `page.Page.Header`
at both Env construction sites (`internal/browser/browser.go`; the live path reads
`cur.Header`, not the throwaway `tmp`). This same wire also feeds CSP (ADR 0014).

**Sec-Fetch — send by default, truthful, per-context, decoupled from `--tls-mimic`**
(these are honest request metadata, not a fingerprint persona):
- Subrequests: a `secFetchTransport` decorator (`internal/js/secfetch.go`) sits just
  inside `countingTransport` and sets `Sec-Fetch-Dest/Mode/Site` from the request's
  resource type (`reqTypeFrom` — script vs empty) and the page origin (same-origin /
  same-site via `publicsuffix.EffectiveTLDPlusOne` / cross-site). It **strips** any
  page-supplied `Sec-Fetch-*` first — these are forbidden header names untrusted page
  JS must not spoof. `Sec-Fetch-User` is never added to a subrequest.
- Primary document: `setFetchMetadataDefaults` (`internal/fetch/client.go`) sends the
  navigation set (`document/navigate/none/?1`) on every primary fetch, gated on
  `Sec-Fetch-Mode` being absent so it never overwrites a subrequest's per-context values.
- **Mode is an approximation:** classic `<script>`→no-cors and fetch/XHR→cors are
  correct; ES-module fetches (also tagged script) are really cors and no-cors beacons
  are really no-cors. The common cases are right; the rest is a documented gap.

**Cross-origin isolation — fidelity globals only, no enforcement:**
- `window.isSecureContext` (https/wss or loopback) and `window.crossOriginIsolated`
  (secure context AND `COOP: same-origin` AND `COEP: require-corp`/`credentialless`),
  resolved Go-side (`internal/js/isolation.go`) and exposed in the prelude
  (`window === self === globalThis`, one assignment).
- **No COEP subresource enforcement** (require-corp is rare and would add block risk).
  COOP opener-severance is already moot (`window.open`/`opener` are null).
- **`crossOriginIsolated` may report `true` while `SharedArrayBuffer` stays absent** —
  it is a fidelity signal, not a capability gate.

## Consequences

- Origins that gate on Fetch Metadata see a coherent request context for both the
  navigation and every subresource. Page JS reads truthful isolation/secure-context
  signals.
- No render-regression risk: this only adds truthful signals and request headers,
  nothing is blocked. (CSP, which can block, lands separately in ADR 0014.)
- The existing `--tls-mimic` persona keeps the client hints (`sec-ch-ua*`) and the
  utls fingerprint; only the honest Sec-Fetch metadata moved to the default path.
