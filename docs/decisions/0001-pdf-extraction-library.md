# ADR 0001: Keep dslipak/pdf for PDF text extraction, contained (and vendored)

Date: 2026-07-02
Status: accepted (amended same day: vendored after a fuzz-found containment failure)

## Context

`internal/content/pdf` extracts text from untrusted PDFs via
`github.com/dslipak/pdf v0.0.2` — a pre-1.0, sparsely-maintained fork of
`rsc.io/pdf`. The 2026-07-01 product evaluation flagged it as the project's top
dependency risk: it parses attacker-controlled bytes and is known to panic on
malformed input.

Alternatives evaluated:

| Candidate | Verdict |
| --- | --- |
| `ledongthuc/pdf` | Same `rsc.io/pdf` lineage and parsing core — swapping changes the name, not the risk. |
| `pdfcpu` | Actively maintained, pure Go — but its strength is validation/manipulation; text extraction is limited and not its contract. Worth re-evaluating as it matures. |
| UniDoc `unipdf` | Capable, but AGPL/commercial dual license — unacceptable for this project. |
| `go-fitz` / pdfium bindings | Best extraction quality, but cgo — violates the pure-Go, single-static-binary constraint. |
| Dropping PDF support | PDFs are a top agent ask (docs, papers, statements); regression in product value. |

## Decision

Keep `dslipak/pdf`, and treat it as **hostile-input code that must be
contained** rather than trusted:

1. Every call runs behind `recover()` (`extract`, `pdfTitle`) — a library panic
   becomes a clean classified error, never a process crash.
2. Extraction runs on a goroutine bounded by the caller's context, so a
   pathological PDF cannot hang a request past its deadline.
3. Input size is already capped upstream by fetch's body limit (10 MiB).
4. Unextractable/encrypted/image-only PDFs degrade to a manifest, not an error.
5. `FuzzConvert` (`internal/content/pdf/fuzz_test.go`, `make fuzz`)
   continuously exercises the containment: for arbitrary bytes the only
   acceptable outcomes are an error or a manifest — never a panic.

## Amendment: fuzz-found containment failure → vendored fork

`FuzzConvert` found (on its first run, in seconds) an input the containment
could NOT hold: a malformed xref sends the library into an **infinite
in-memory loop** — `Page()` re-evaluates the same Pages node forever when its
kids don't resolve, and `readByte` fabricates `'\n'` bytes forever after a
failed reload. `recover()` never fires (nothing panics) and a context deadline
doesn't stop the goroutine (the loop does no I/O and checks nothing) — a
hostile PDF was a per-request CPU-burn DoS.

Per ADR 0002 ("vendor only on failure"), the module is now vendored at
`third_party/pdf` (wired by a `replace` directive; BSD-3 LICENSE retained)
with three minimal patches, each marked "unblink patch":

1. `page.go Page()` — a kids scan that neither descends nor returns ends the
   search, and descents are depth-bounded (cyclic page trees terminate).
2. `lex.go readByte()` — synthetic-newline fabrication from an exhausted
   reader is bounded (panics past it; our recover turns that into an error).
3. `read.go resolve()` — the object-stream `Extends` chain walk is bounded
   (cyclic chains terminate).

`Convert` additionally enforces its own hard wall-clock (`maxExtractTime`)
independent of the caller's context, so any *future* unbounded loop caps a
request's exposure even before the fuzzer finds it.

## Consequences

- Extraction quality stays at `rsc.io/pdf` level (no OCR, struggles with some
  encodings); acceptable for the manifest-fallback contract.
- The vendored copy at `third_party/pdf` is the source of truth; the `go.mod`
  `require` of `v0.0.2` remains for provenance but is overridden by `replace`.
  Sync upstream changes manually and re-run `make fuzz` before adopting them.
- Revisit trigger: pdfcpu (or another maintained pure-Go library) shipping
  first-class text extraction.
