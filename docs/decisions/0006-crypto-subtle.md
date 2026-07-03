# ADR 0006: Implement a broad, Go-backed `crypto.subtle` (reversing "leave it undefined")

Date: 2026-07-03

Status: accepted

## Context

Earlier (Phase 15, recorded in `internal/js/globals.go`) `crypto.subtle` was left
**undefined on purpose**: `getRandomValues`/`randomUUID` covered the id-generation need,
and a *half-working* `subtle` was judged worse than none, because libraries feature-detect
`crypto.subtle` and a partial shim that then throws on `generateKey`/`encrypt` is a
synchronous crash mid-flow.

That reasoning held while the gap cost little. It stopped holding once real pages showed up
that call `subtle` **unconditionally** on the first-paint path — PKCE `code_challenge`
(`base64url(SHA-256(verifier))`), OAuth/OIDC helpers, subresource-integrity-style hashing,
and cache-key digests. For those, an undefined `subtle` is a hard `TypeError` at boot and the
content never renders. The priorities doc (`docs/web-api-priorities.md`, Tier 2) flagged this
as an open decision.

Two facts changed the calculus:

1. **The feature-detection trap is avoidable by construction.** If `subtle` is present and its
   *unsupported* methods return a **rejected promise** (not a missing property), a
   feature-detecting app degrades to a `.catch` instead of a synchronous crash — strictly safer
   than absence for any app that actually calls `subtle`.
2. **The common surface is pure-Go stdlib.** Digest, HMAC, AES-GCM/CBC/CTR, and PBKDF2/HKDF are
   all `crypto/*` (PBKDF2/HKDF implemented by hand over `crypto/hmac`, no new dependency), and
   the work is synchronous CPU — no I/O, so it never touches the settle audit ([ADR 0004](0004-provable-idle-settle.md)).

## Decision

1. Implement `crypto.subtle` as **Go byte-primitives** (`internal/js/subtle.go`: `__unblinkDigest`,
   `__unblinkHmacSign`, `__unblinkAesEncrypt/Decrypt`, `__unblinkPbkdf2`, `__unblinkHkdf`,
   `__unblinkRandomBytes`) wrapped by the **WebCrypto object model in JS** (`preludeAPIJS`:
   Promises, `CryptoKey`, `raw`/`jwk` formats, algorithm normalization, extractable checks).
2. Support: `digest` (SHA-1/256/384/512), `sign`/`verify` (HMAC), `encrypt`/`decrypt`
   (AES-GCM/CBC/CTR), `deriveBits`/`deriveKey` (PBKDF2/HKDF), and `generateKey`/`importKey`/
   `exportKey` for **symmetric** keys.
3. **Everything unsupported returns a rejected promise, never a synchronous throw** — RSA/ECDSA/
   ECDH `sign`/`verify`/`generateKey`, `wrapKey`/`unwrapKey`, and unknown formats all
   `Promise.reject(NotSupportedError)`.
4. Re-point `crypto.getRandomValues` at `crypto/rand` (via `__unblinkRandomBytes`) so keys
   generated here have real entropy; the Math.random baseline in `globals.go` stays only as the
   no-native fallback.

## Still non-goals

- **RSA and elliptic-curve** (`RSASSA-PKCS1-v1_5`, `RSA-OAEP`, `RSA-PSS`, `ECDSA`, `ECDH`,
  `Ed25519`) — heavy, key-format-laden, and rarely on the first-paint content path. They reject.
- **`wrapKey`/`unwrapKey`** — reject.
- `crypto.subtle` is **not a security boundary** here: `CryptoKey.__raw` holds key material in
  the JS heap (this is a headless content extractor, not a place secrets are protected).

## Consequences

- Pages gated on `subtle.digest`/PKCE/AES now render; feature-detecting apps that reach an
  unsupported method get a catchable rejection instead of a crash.
- **Behavior can shift**: an app that branches on `if (crypto.subtle)` now takes its
  crypto path. If that path relies on an unsupported algorithm it will reject where absence
  would have sent it down a fallback — the residual risk this ADR accepts as smaller than the
  crash-at-boot it removes.
- Regression nets: `internal/js/webapi_tier2_test.go` (`TestCryptoSubtle`: the SHA-256('abc')
  vector, HMAC + AES-GCM round-trips, PBKDF2, and an unsupported-algorithm rejection) and the
  `js-api-smoke` eval marker `api-25`.
- **Revisit trigger:** a concrete target whose first-paint content requires RSA/ECDSA/ECDH (a
  WebAuthn or JOSE/JWT-verify flow) — implement that curve/algorithm then, not speculatively.
