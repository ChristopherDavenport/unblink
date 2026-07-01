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

.PHONY: all build run version test eval vet fmt tidy clean

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

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

clean:
	rm -rf bin
