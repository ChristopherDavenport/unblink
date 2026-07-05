# ADR 0014: Content-Security-Policy enforcement over untrusted page JS

Date: 2026-07-04
Status: accepted

## Context

Continuing the browser-parity series (CORS/SRI 0011/0012, Fetch-Metadata/isolation
0013), the remaining large gap was CSP. unblink runs the page's untrusted
JavaScript; a page's `Content-Security-Policy` constrains what that code may do
(which scripts run, where it may connect, whether `eval` is allowed). Enforcing it
is defense-in-depth over executed page code and a fidelity improvement — a browser
would refuse the same scripts/connections. It reuses the `Env.ResponseHeaders`
wire added in 0013 and the SRI hash helpers.

The cost is real: CSP is the only feature in the series that can **blank a page**
(block a script the render needs, or disable `eval` a framework relies on), and it
is subtle (nonce/hash, `strict-dynamic`, multiple policies, report-only).

## Decision

Enforce CSP over page JS **on by default**, escape hatch `--no-csp` / `WithoutCSP`.

**Parsing (`internal/js/csp.go`, pure + fuzzed):** every `Content-Security-Policy`
response header (and comma-separated policy within one) becomes an enforced policy;
every `Content-Security-Policy-Report-Only` a report-only policy; `<meta
http-equiv>` an enforced policy (dropping `frame-ancestors`/`report-uri`/`sandbox`
which meta can't host). A resource must satisfy **every** enforced policy.

**Directives enforced** (with fallback): `script-src-elem`→`script-src`→`default-src`
(script loading + inline), `script-src`→`default-src` (`unsafe-eval`),
`connect-src`→`default-src` (fetch/XHR). Source expressions: `'none'`/`'self'`/
`'unsafe-inline'`/`'unsafe-eval'`/`'strict-dynamic'`, `'nonce-…'`, `'sha256/384/512-…'`
(reusing `sriHash`/`decodeSRIDigest`), host-source (scheme/host/`*` wildcard/port/
path), scheme-source.

**Enforcement points** (mirroring the CORS/SRI hooks):
- **Inline scripts** — `runScripts` (`scripts.go`): nonce match, else hash of the
  source text (before `stripSourceMappingURL`), else `'unsafe-inline'` (ignored when
  a nonce/hash source or `strict-dynamic` is present — the spec quirk).
- **External scripts** — `runScripts` (parser-inserted) and `loadInsertedScript`
  (script-inserted). **`strict-dynamic`** ignores host/`self` and lets a
  trusted-script-inserted chunk through — the critical modern-CSP path; a
  parser-inserted script needs a matching nonce. A block fires the `error` event.
- **Module entry** — `runModules`: the top-level tag only; transitive imports
  inherit its decision (documented approximation).
- **connect-src** — checked **on-loop in `fetchPromise`** (a pure URL check;
  `recordError` is only loop-safe there, not in `doFetch`'s off-loop goroutine). A
  block rejects the promise **without dispatching** (a browser blocks the
  connection) — unlike CORS's send-and-log, since connect-src prevents the
  connection itself; the diagnostic keeps it visible to the operator.
- **unsafe-eval** — when `script-src` lacks `'unsafe-eval'`, `installCSPEvalGate`
  (after the prelude, before page scripts) replaces `eval`/`Function` with
  `EvalError`-throwing shims, re-pointing `F.prototype = Function.prototype` so
  `instanceof`/`Function.prototype.*` still work. The string-timer path
  (`setTimeout("code")`) then correctly throws, matching a browser. (Verified goja
  honors reassigning the global `eval`/`Function` bindings and has `EvalError`.)

**Report-only** evaluates the same predicates but only records a diagnostic
("report-only: would block …") — never blocks. All violations (enforced and
report-only) surface via `recordError` → `RenderResult.Errors`.

## Consequences

- Page JS that a browser's CSP would block no longer runs in unblink either; the
  render reflects that (and the diagnostics say why). The `--no-csp` hatch restores
  the permissive behavior when extraction needs a page's own-CSP-suppressed scripts.
- **Render-regression risk:** `unsafe-eval` blocking a template-compiling framework
  (Vue full build, old Angular) blanks such pages — correct browser behavior, but a
  behavior change; report-only and `--no-csp` mitigate.
- **Non-goals / approximations:** `img/style/font/media/frame-src` are no-ops
  (unblink fetches no such passive subresources); `frame-ancestors`/`form-action`/
  `base-uri`/`sandbox` are out of scope; `<meta>` CSP is applied whole-document
  (not position-scoped); transitive module imports inherit the entry decision; a
  scheme-less host-source matches network schemes only.
