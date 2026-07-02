# ADR 0002: Dependency pinning policy (goja pseudo-versions and friends)

Date: 2026-07-02
Status: accepted

## Context

Several load-bearing dependencies cannot be pinned to semver releases:

- `github.com/dop251/goja` and `github.com/dop251/goja_nodejs` publish **no
  tagged releases** — the only pin is a commit pseudo-version. goja is the
  entire JS engine (Phase 8+ framework rendering depends on parser behaviors
  verified commit-by-commit, e.g. native private-field parsing and the WeakMap
  bug that ruled out esbuild ES2017 downleveling).
- `codeberg.org/readeck/go-readability/v2` lives on Codeberg (availability is
  fine via the module proxy, but it is off the beaten GitHub path).
- `github.com/dslipak/pdf` is pre-1.0 (see ADR 0001).

## Decision

1. **Pseudo-version pins are deliberate, not accidents.** goja/goja_nodejs are
   pinned to specific commits on purpose; `go mod tidy` never changes them
   unless a human bumps them.
2. **Bumping goja is a reviewed change** with this checklist: `make test`
   (`internal/js` carries the densest suite), `make eval` (framework-render
   cases are must-pass), and a manual read of the goja changelog for parser/
   runtime behavior changes. Never bump goja as part of an unrelated PR.
3. **The module proxy is the availability story.** All modules (including the
   Codeberg one) resolve through proxy.golang.org, which caches them
   independently of the origin host; no vendoring is required for
   availability alone.
4. **Vendor only on failure.** If an upstream disappears or a needed fix stops
   landing, vendor the module (or fork under `christopherdavenport/`) at that
   point — not preemptively.

## Consequences

- `go.mod` shows pseudo-versions; this file is the answer to "why isn't this
  tagged?".
- Dependabot-style auto-bumps must exclude goja/goja_nodejs (no CI bot is
  configured today; if one is added, encode the exclusion there).
