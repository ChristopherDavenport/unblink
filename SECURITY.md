# Security Policy

## Reporting a vulnerability

Please report security issues privately through GitHub's **[Report a
vulnerability](https://github.com/ChristopherDavenport/unblink/security/advisories/new)**
(Security → Advisories) rather than opening a public issue. We aim to acknowledge
a report within a few days and will coordinate a fix and disclosure timeline with
you.

## Supported versions

unblink is pre-1.0; security fixes land on `main` and in the next tagged release.
Run the latest release.

## Threat model

unblink's job is to feed the web to an AI agent, so it treats **all fetched
content as untrusted** and, under `--js`, runs **untrusted page JavaScript**. The
design assumes the page is adversarial and bounds what it can do:

- **Untrusted content is fenced by default.** Returned page content is wrapped in
  a provenance/"untrusted content" fence so the model treats it as data, not
  instructions; human-hidden text and comments are stripped; and Markdown image
  beacons (a zero-click exfiltration channel) are defanged to inert text. This is
  defense-in-depth against indirect prompt injection, not a guarantee.
  `--no-safe-output` opts out.
- **SSRF guard on every fetch.** The primary, per-session, one-shot, *and* page-JS
  subrequest clients all reject connections to private/loopback/link-local/
  metadata IPs — including CGNAT `100.64.0.0/10` — checked against the *resolved*
  address. On by default; `--allow-private` / `--js-allow-private` are the
  deliberate escape hatches.
- **Origin-scoped credentials.** Injected bearer/basic/header/cookie credentials
  are pinned to one origin and stripped on any cross-origin redirect (including a
  same-domain port change), so a token can't leak to another host. Secrets resolve
  from the environment (`token_env`/`password_env`), never appear in session state,
  and are masked in logs.
- **Quarantined JS engine.** Page JavaScript runs on an embedded goja interpreter
  with `require()` disabled (no host file reads), no access to HttpOnly cookies via
  `document.cookie`, and hard bounds on wall-clock time (`--js-timeout`), network
  (`--js-max-requests`), live runtimes (`--js-max-live`), and **heap memory**
  (`--js-memory-limit`, see [ADR-0003](docs/decisions/0003-js-memory-guard.md)).
- **Fuzzed parsers.** Every parser that eats untrusted bytes (HTML, reduce, PDF,
  robots, token cursors) has a fuzz target; a fuzz-found PDF CPU-DoS led to
  vendoring and bounding that dependency
  ([ADR-0001](docs/decisions/0001-pdf-extraction-library.md)).

## Out of scope

- unblink is **not a compliant crawler**: robots.txt is surfaced as context, never
  enforced. Operating it against a site is the operator's responsibility.
- The safe-output pass reduces, but cannot eliminate, prompt-injection risk. Treat
  agent actions taken on web-derived content accordingly.
- unblink has no layout engine, so CSS-only attacks that depend on the full cascade
  (e.g. color-based hiding) are not detected.
- Anti-bot evasion (`--tls-mimic`) is best-effort and explicitly not a security
  boundary.
