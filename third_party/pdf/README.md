# Vendored fork of github.com/dslipak/pdf v0.0.2

Vendored after fuzzing found a containment failure
(see docs/decisions/0001-pdf-extraction-library.md): a malformed xref sends
the library into infinite in-memory loops that neither recover() nor a
context deadline can stop. Three loop-bounding patches, each marked
"unblink patch", live in `page.go`, `lex.go`, and `read.go` — see the ADR.
Imported as a plain package of the unblink module
(`github.com/christopherdavenport/unblink/third_party/pdf`); a `replace`
directive would break `go install <module>@version`. Upstream license
(BSD-3, Go Authors) retained in LICENSE.
