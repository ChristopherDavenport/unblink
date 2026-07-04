# ADR 0011: CORS enforcement over untrusted page JavaScript (with history preservation)

Date: 2026-07-04
Status: accepted

## Context

unblink executes the page's own untrusted JavaScript in the goja engine. Until
now that code could `fetch()`/`XMLHttpRequest` **any** cross-origin URL and read
the full response body unconditionally: `Request.mode`/`credentials` were set on
the JS object but never read, and were dropped at the JS→Go boundary
(`__unblinkFetch` only received `method,url,headers,body`). There was no
preflight, no `Access-Control-*` validation, and cross-origin requests carried
the session's cookies. The SSRF guard (block private/metadata IPs) and
credential-scoping (strip *injected auth headers* on cross-origin redirects) are
different mechanisms — neither implements the same-origin policy.

The concern is not fidelity but **security posture over code we run for the
agent**: without the same-origin policy, a page's script can read an arbitrary
cross-origin response (e.g. exfiltrate data from an endpoint that trusts ambient
cookies) exactly the way a browser refuses to let it.

The counter-concern is that unblink is a *read tool*: the operator/agent, at a
higher trust tier than the page, legitimately wants to see cross-origin traffic.

## Decision

Enforce CORS over page-JS fetch/XHR **on by default**, in the Go layer, while
**preserving the request in the network history** so the agent keeps devtools-grade
visibility.

- **Enforcement point: `doFetch` (`internal/js/cors.go`), called from
  `fetchPromise`.** It sits *above* `b.transport.Do` but *below* the JS promise,
  so every request — a preflight `OPTIONS`, the real request, or a to-be-blocked
  one — is still issued through the transport chain (top of which is the
  `countingTransport` that backs the `requests` tool). The layer decides only
  what the page's code may **read**; the traffic is always logged. This is the
  history-preservation invariant: *block the page, not the operator*.
- **State machine (Fetch spec):** same-origin → today's path (type `basic`).
  Cross-origin `cors` → preflight for non-simple requests, then validate
  `Access-Control-Allow-Origin` (and `-Credentials`/`-Methods`/`-Headers`); on
  failure reject with a `TypeError` (type `cors` on success). `no-cors` → send
  (and log) but hand JS an **opaque** response (type `opaque`, status 0, empty
  body) so beacons/pixels still fire. `same-origin` mode cross-origin → reject
  without sending.
- **Credentials.** Default `same-origin` credentials means cookies are **not**
  sent cross-origin. `fetchPromise` marks non-credentialed cross-origin requests
  with `WithOmitCredentials`; the shared JS RoundTripper (`cookieStripper` in
  `internal/browser/transport.go`) drops the `Cookie` header just before the
  wire. Credentialed (`include`) requests require an echoed (non-`*`) ACAO plus
  `Access-Control-Allow-Credentials: true`. This composes with the existing
  auth-header scoping (which already strips `Authorization`/custom headers
  cross-origin).
- **Scope.** CORS applies only to fetch/XHR. Cross-origin `<script>`/module loads
  stay allowed (browser parity — scripts are not CORS-read-restricted); their
  integrity is governed by SRI (ADR 0012). Non-HTTP schemes (`chrome-extension://`,
  `data:`, `blob:`) are not policed. The **extension background worker bypasses
  CORS** (`allowCrossOrigin = true`): extension code is operator-supplied and
  legitimately fetches cross-origin.
- **Settle.** The added preflight is issued inside `fetchPromise`'s existing
  `pending`/keepalive bracket — no new async primitive, so ADR 0004's
  provable-idle invariant holds.
- **Escape hatch.** `--js-allow-cross-origin` (`WithJSAllowCrossOrigin`) disables
  enforcement for extraction flows that need cross-origin reads to succeed;
  default off (enforce).

## Consequences

- Page JS can no longer read cross-origin responses a browser would deny, and
  can no longer make credentialed cross-origin requests with the session's
  cookies unless the server opts in. The agent still sees every such request in
  the `requests` tool.
- **Known approximation:** `fetch.Client` follows redirects transparently, so a
  cross-origin redirect mid-request is validated only on the **final** hop against
  the document origin, not per-hop as a browser would. Acceptable for a read
  tool; documented rather than re-plumbed through the redirect chain.
- One extra preflight round-trip per non-simple cross-origin request — the same
  cost a real browser pays, only when enforcement is on.
