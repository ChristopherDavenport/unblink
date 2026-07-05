# ADR 0012: Subresource Integrity (SRI) verification, block on mismatch

Date: 2026-07-04
Status: accepted

## Context

A page can pin a subresource with `integrity="sha256-…"/"sha384-…"/"sha512-…"`;
a real browser refuses to execute a `<script>` (or apply a stylesheet) whose
fetched bytes don't match. unblink never read the attribute — every subresource
executed unconditionally once fetched. Since unblink runs the page's untrusted
JavaScript, executing a *tampered or CDN-drifted* script the author never intended
is both a fidelity gap and a security gap.

The building blocks already existed: the SHA-256/384/512 primitives
(`subtleHash`, `internal/js/subtle.go`) and clean hook points where the fetched
bytes and the requesting DOM node are both in scope.

## Decision

Verify SRI on the subresources the engine executes, **blocking on mismatch, on by
default**.

- **Verifier: `verifySRI` (`internal/js/sri.go`)** — a pure, fuzzed
  (`FuzzVerifySRI`) function. It parses the whitespace-separated metadata,
  ignores unknown/malformed tokens (browser leniency), selects the **strongest
  algorithm** present, and accepts if **any** digest of that algorithm matches
  `base64(hash(raw))`. An attribute naming no recognized hash is "not enforced"
  (allow), matching a browser's treatment of absent/unparseable integrity.
- **Hash the RAW bytes, not the executed body.** `fetch` charset-decodes JS to
  UTF-8 (`internal/fetch isTextualResponse`), but SRI must hash the octets the
  server delivered — otherwise a legitimate non-UTF-8/BOM'd script would
  false-mismatch and, because we block by default, break the page. So
  `fetch.Result` and `js.Response` now carry a `Raw` field (the post-decompress,
  pre-transcode bytes; it shares the `Body` slice when no transcode happened, so
  ~zero extra memory in the common case), and SRI hashes `Raw` (falling back to
  `Body` for synthesized responses without it).
- **Hook points** (`internal/js/scripts.go`, `modules.go`): classic external
  `<script src>` (`runScripts`), dynamically inserted webpack-JSONP chunks
  (`loadExternalScript` — a mismatch fires the `error` event instead of running),
  and the **top-level** `<script type=module>` entry (`runModules` — transitive
  static imports are not checked, matching browsers). A mismatch records a
  diagnostic (`recordError`) and skips execution.
- **Integrity'd nodes bypass the asset cache.** The cache stores only the
  transcoded body, not `Raw`, so a pinned resource is fetched fresh to obtain the
  bytes to hash. Pinned bundles are few; the lost cross-render caching is
  acceptable. (Alternative: cache `Raw` too — deferred.)
- **Escape hatch.** `--no-sri` (`WithoutSRI`) disables verification; default is
  verify.

## Consequences

- A subresource whose bytes don't match its declared integrity is not executed —
  the same outcome a browser produces, now reflected in unblink's render and
  diagnostics.
- SRI on `<link rel=stylesheet>` is moot: unblink never fetches passive
  stylesheet bytes (they are not applied), so there is nothing to guard.
- The extra fresh fetch for pinned resources is the only added cost, and only for
  resources that actually carry `integrity`.
