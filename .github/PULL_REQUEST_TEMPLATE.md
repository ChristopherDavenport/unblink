## Summary

<!-- What changes and why. Link the issue if there is one. -->

## Checklist

- [ ] `make fmt vet lint test eval` is clean
- [ ] New or changed tool behavior has a test, and an eval case (`eval/`) where it affects extraction quality
- [ ] Architectural decisions are recorded as an ADR under `docs/decisions/`
- [ ] Docs updated where relevant (README flags table, `docs/architecture.md`)
- [ ] No goja/goja_nodejs pin bumps folded in ([ADR-0002](docs/decisions/0002-dependency-pinning-policy.md) — those are their own reviewed PR)
