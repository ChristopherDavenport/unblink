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
   pathological PDF cannot hang a request past its deadline. (This bounded
   the request only, not the goroutine — see Amendment 3.)
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
`third_party/pdf` (BSD-3 LICENSE retained) with three minimal patches, each
marked "unblink patch":

1. `page.go Page()` — a kids scan that neither descends nor returns ends the
   search, and descents are depth-bounded (cyclic page trees terminate).
2. `lex.go readByte()` — synthetic-newline fabrication from an exhausted
   reader is bounded (panics past it; our recover turns that into an error).
3. `read.go resolve()` — the object-stream `Extends` chain walk is bounded
   (cyclic chains terminate).

`Convert` additionally enforces its own hard wall-clock (`maxExtractTime`)
independent of the caller's context, so any *future* unbounded loop caps a
request's exposure even before the fuzzer finds it.

## Amendment 2 (v0.17.1): in-repo import path, not a `replace` directive

The vendoring was originally wired as `require github.com/dslipak/pdf` plus a
`replace` directive pointing at `third_party/pdf`. That broke the front-door
install path: `go install <module>@version` refuses any module whose `go.mod`
contains `replace` directives (surfaced by the first public-release smoke
test). The vendored copy is now a plain package of the unblink module —
imported as `github.com/christopherdavenport/unblink/third_party/pdf`, no
nested `go.mod`, no `require`/`replace` — which `go install` accepts.

## Amendment 3: cooperative cancellation (a fourth patch)

Decision item 2 and `maxExtractTime` only ever bounded the *request*.
`Convert` returned when its context ended, but the extraction goroutine could
not be stopped: a document that kept the library busy went on burning a CPU
after the request had returned, and repeated requests stacked such goroutines
up. Loop-bounding patches fix known loops one at a time; they do nothing for
the next one. Two that the three patches above do not cover: an xref `/Prev`
that points at its own table (opening the document never finishes), and a page
tree listing one page object 200,000 times (page lookup rescans the kids from
the start, so extraction is quadratic — from a ~1.2 MB file).

The vendored copy now carries a fourth patch, marked "unblink patch" like the
others, that makes an extraction stop when its context ends:

4. `cancel.go` (new file, not in upstream) — `NewReaderContext(ctx, f, size)`
   binds a `Reader` to a context. Once the context is done:
   - every read of the document bytes fails (a wrapping `io.ReaderAt`), which
     starves the lexer and every stream filter;
   - `read.go resolve()` panics — one line at the single path behind
     `Value.Key`/`Value.Index`, so every walk over the object graph (page
     tree, `Parent` chains, fonts) is covered. Supporting hunks: a `ctx` field
     on `Reader`, set in `NewReaderEncrypted`;
   - `page.go cmap.Decode()` panics once per decoded character code — that
     loop is quadratic over in-memory data and resolves nothing. Supporting
     hunks: an `r` field on `cmap`, set in `readCmap`.

   The checkpoints panic because that is how the library already reports
   malformed input, and the caller already recovers. `NewReader` and the rest
   of the upstream API are unchanged.

`Convert` passes its context (capped at `maxExtractTime`) to
`NewReaderContext` and reports the context's error for anything that comes
back after the context ended. `TestExtractionStopsWhenContextEnds` runs the
two documents above and requires the extraction itself to return.

The limit of the guarantee: cancellation is cooperative. A loop that reaches
none of the three checkpoints is not interrupted; `Convert` still returns on
time (it does not wait for the goroutine), but that goroutine runs until its
loop ends. No such loop is known. Concurrent extractions are not capped.

## Consequences

- Extraction quality stays at `rsc.io/pdf` level (no OCR, struggles with some
  encodings); acceptable for the manifest-fallback contract.
- The vendored copy at `third_party/pdf` is the source of truth, imported by
  its in-repo path. To sync upstream: copy the upstream files over
  `third_party/pdf`, keep `cancel.go`, re-apply every "unblink patch" hunk
  (the three loop bounds and the cancellation hunks in `read.go`/`page.go`),
  and re-run `make test` and `make fuzz` before adopting.
- Revisit trigger: pdfcpu (or another maintained pure-Go library) shipping
  first-class text extraction.
