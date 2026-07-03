# unblink — pure-Go "browser for AI"
#
# The official MCP SDK requires Go >= 1.25, newer than the Go that may be
# installed locally. We set GOTOOLCHAIN=auto so the go command downloads and
# uses the toolchain pinned in go.mod automatically, without touching your
# global `go env` configuration.
export GOTOOLCHAIN := auto

BIN := bin/unblink

# Stamp the release version from the nearest git tag (v0.16.0 -> 0.16.0).
# Falls back to the in-source dev default when there is no tag yet.
VERSION := $(shell git describe --tags --match 'v*' --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS := $(if $(VERSION),-ldflags "-X github.com/christopherdavenport/unblink/internal/mcpserver.version=$(VERSION)")

.PHONY: all build run version test eval fuzz bench membench vet lint fmt tidy clean

all: build

build:
	go build $(LDFLAGS) -o $(BIN) ./cmd/unblink

run: build
	./$(BIN)

version: build
	./$(BIN) --version

test:
	go test ./...

# Deterministic, offline, in-process MCP eval harness. Gated behind the `eval`
# build tag so it never runs as part of `make test`; prints a scorecard to
# stderr and exits nonzero on regression.
eval:
	go test -tags eval ./eval -run TestEval -count=1 -v

# Short coverage-guided fuzzing over every parser that consumes untrusted
# bytes (HTML, reduction, PDF, robots.txt, pagination cursors). The seed
# corpora already run in `make test`; this explores beyond them. Tune the
# per-target budget with FUZZTIME.
FUZZTIME ?= 15s
fuzz:
	go test ./internal/dom -run '^$$' -fuzz '^FuzzParseExtract$$' -fuzztime $(FUZZTIME)
	go test ./internal/reduce -run '^$$' -fuzz '^FuzzReduce$$' -fuzztime $(FUZZTIME)
	go test ./internal/content/pdf -run '^$$' -fuzz '^FuzzConvert$$' -fuzztime $(FUZZTIME)
	go test ./internal/robots -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME)
	go test ./internal/tokens -run '^$$' -fuzz '^FuzzPaginateCursor$$' -fuzztime $(FUZZTIME)

# Performance benchmarks over the hot-path packages. Workflow for proving an
# optimization: `make bench > /tmp/base.txt` on the baseline, apply the change,
# `make bench > /tmp/new.txt`, then compare with
# `go run golang.org/x/perf/cmd/benchstat@latest /tmp/base.txt /tmp/new.txt`.
# Narrow with BENCH (regexp) and trade time for precision with BENCHCOUNT.
BENCH ?= .
BENCHCOUNT ?= 10
bench:
	go test -run '^$$' -bench '$(BENCH)' -benchmem -count $(BENCHCOUNT) \
		./internal/js ./internal/reduce ./internal/dom ./internal/emit ./internal/browser ./internal/session

# membench measures unblink's footprint against a headless Chromium baseline on
# identical local fixtures (docs/comparison.md). It lives in a SEPARATE module
# (scripts/membench, its own go.mod with chromedp) so the published binary stays
# dependency-clean. Requires `make build` first and a system chrome/chromium
# (auto-detected, or pass CHROME=/path). Nested module is invisible to the
# root ./... targets above.
membench: build
	cd scripts/membench && go run . -root ../.. $(if $(CHROME),-chrome $(CHROME))

# third_party/ carries vendored upstream code (ADR-0001) that is kept
# byte-close to upstream for easy syncing — it is exempt from vet, not fixed.
vet:
	go vet $$(go list ./... | grep -v /third_party/)

# Needs the golangci-lint binary (https://golangci-lint.run/docs/welcome/install/);
# CI runs the same config via golangci-lint-action.
lint:
	golangci-lint run

fmt:
	gofmt -w .

tidy:
	go mod tidy

clean:
	rm -rf bin
