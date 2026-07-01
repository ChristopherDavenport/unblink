# unblink — pure-Go "browser for AI"
#
# The official MCP SDK requires Go >= 1.25, newer than the Go that may be
# installed locally. We set GOTOOLCHAIN=auto so the go command downloads and
# uses the toolchain pinned in go.mod automatically, without touching your
# global `go env` configuration.
export GOTOOLCHAIN := auto

BIN := bin/unblink

.PHONY: all build run version test eval vet fmt tidy clean

all: build

build:
	go build -o $(BIN) ./cmd/unblink

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
