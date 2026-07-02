# Vendored fork of github.com/dslipak/pdf v0.0.2

Vendored via a `replace` directive after fuzzing found a containment failure
(see docs/decisions/0001-pdf-extraction-library.md): a malformed xref sends
the tokenizer into an infinite in-memory loop that neither recover() nor a
context deadline can stop. The only local change is in `lex.go` (`readByte`
bounds its synthetic-newline fabrication). Upstream license (BSD-3, Go
Authors) retained in LICENSE.
