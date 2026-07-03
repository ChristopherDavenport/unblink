# Contributing to unblink

Thanks for your interest. unblink is a pure-Go (no cgo, no Chromium) MCP server;
the authoritative design doc is [docs/architecture.md](docs/architecture.md) —
read it before any non-trivial change.

## Workflow

The `Makefile` is the canonical task runner. CI runs the gofmt check plus
`make vet test eval` on every push and PR; `gofmt` + `go vet` are the only static
tooling (no separate linter).

```sh
make build     # go build (version stamped from the nearest v* tag)
make test      # go test ./... (includes every fuzz target's seed corpus)
make eval      # offline in-process MCP eval gate (scorecard on stderr)
make fuzz      # coverage-guided fuzzing of the untrusted-input parsers
make bench     # perf benchmarks over the hot-path packages
make membench  # footprint benchmark vs headless Chromium (separate module)
make vet       # go vet ./...
make fmt       # gofmt -w .
make tidy      # go mod tidy
```

Before opening a PR: `make fmt vet test eval` must be clean, and new behavior
needs a test. The MCP SDK needs Go ≥ 1.25; the Makefile exports `GOTOOLCHAIN=auto`
so the toolchain is downloaded automatically — if you invoke `go` directly,
prefix with `GOTOOLCHAIN=auto`.

## Where code goes

Respect the enforced dependency direction
`page → capability packages → browser → mcpserver` (see the architecture doc's
package map). New capability code lives in the relevant `internal/<pkg>` and is
routed to the MCP layer through `internal/browser`, never directly. No MCP types
appear below `internal/mcpserver`.

- **Tests** use external `_test` packages with fixtures in `testdata/`.
  `internal/js` carries the densest suite; `internal/browser/browser_test.go` is
  the integration-level test of the whole pipeline.
- **The eval gate** (`eval/`, build tag `eval`) scores the real MCP server over a
  fixture corpus. Add a case when you add or change a tool's behavior; every seed
  case is must-pass.
- **stdout is reserved for MCP JSON-RPC.** All logging goes to stderr via `slog`.

## Decisions & dependency pins

Architectural decisions that would otherwise live only in a PR description get an
ADR under [docs/decisions/](docs/decisions/) (numbered `NNNN-slug.md`). The
goja/goja_nodejs pins are deliberate pseudo-versions — bumping goja is its own
reviewed change ([ADR-0002](docs/decisions/0002-dependency-pinning-policy.md)),
never folded into an unrelated PR.

## Releasing

Pushing a `v*` tag fires two workflows:

- **`release.yml`** — the test/eval gate, then goreleaser: cross-platform
  archives + checksums, multi-arch Docker images and manifests on
  `ghcr.io/christopherdavenport/unblink`, a GitHub Release with auto-generated
  notes, and finally a bot commit on `main` syncing
  `.claude-plugin/plugin.json` to the new tag.
- **`publish-mcp-registry.yml`** — stamps `server.json` from the tag, waits
  until `ghcr.io/christopherdavenport/unblink:X.Y.Z` is anonymously pullable,
  then publishes to the [MCP registry](https://registry.modelcontextprotocol.io)
  via GitHub OIDC.

Before tagging:

1. Bump `version` in `internal/mcpserver/server.go` and add a `CHANGELOG.md`
   entry; `make fmt vet test eval` must be clean.
2. Do **not** hand-edit the versions in `server.json` or
   `.claude-plugin/plugin.json` — both are machine-stamped from the tag.

Tag and push (`git tag vX.Y.Z && git push origin vX.Y.Z`), then verify:

3. **Smoke-test `go install`** from a clean module cache — the module path is
   lowercase (`github.com/christopherdavenport/unblink`) even though the GitHub
   repo is mixed-case, so confirm the canonical command resolves and that the
   mixed-case path fails with the expected go.mod mismatch:
   ```sh
   GOTOOLCHAIN=auto GOPROXY=https://proxy.golang.org,direct \
     go install github.com/christopherdavenport/unblink/cmd/unblink@latest
   ```
   Keep every install snippet in the README lowercase-only.
4. **Smoke-test the image**:
   `docker run -i --rm ghcr.io/christopherdavenport/unblink:X.Y.Z --version`.
5. **Check the registry**:
   `curl "https://registry.modelcontextprotocol.io/v0/servers?search=unblink"`
   should show the new version.
6. `git pull` — the plugin-sync bot commit lands on `main` after goreleaser.

One-time gotcha: the first-ever push to a new ghcr package creates it
**private**, and the MCP registry validates images anonymously. Flip the
package public at
<https://github.com/users/ChristopherDavenport/packages/container/unblink/settings>
(web UI only) and re-run the registry workflow if it timed out.

## Reporting security issues

See [SECURITY.md](SECURITY.md) — please report privately, not in a public issue.
