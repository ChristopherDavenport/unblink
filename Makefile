# unblink — pure-Go "browser for AI"
#
# The dependencies require Go >= 1.27, possibly newer than the Go that may be
# installed locally. We set GOTOOLCHAIN=auto so the go command downloads and
# uses the toolchain pinned in go.mod automatically, without touching your
# global `go env` configuration.
export GOTOOLCHAIN := auto

BIN := bin/unblink

# Stamp the release version from the nearest git tag (v0.16.0 -> 0.16.0).
# Falls back to the in-source dev default when there is no tag yet.
VERSION := $(shell git describe --tags --match 'v*' --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS := $(if $(VERSION),-ldflags "-X github.com/christopherdavenport/unblink/internal/mcpserver.version=$(VERSION)")

.PHONY: all build run version test eval wpt wpt-sync corpus corpus-capture corpus-compare fuzz bench membench crossbench vet lint fmt tidy clean

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

# Offline web-platform-tests conformance report over the pinned, vendored corpus
# (testdata/wpt). Gated behind the `wpt` build tag so it never runs in `make test`;
# runs the in-scope testharness.js subset directly against the JS engine and prints
# a bucketed per-directory scorecard to stderr + writes wpt/last-run.json. This is
# an on-demand analysis tool, NOT a CI gate — see docs/decisions/0016-wpt-conformance.md.
wpt:
	go test -tags wpt ./wpt -run '^TestWPT$$' -count=1 -v -timeout 50m

# Refresh the vendored WPT subset to the SHA in testdata/wpt/WPT_VERSION. Needs
# network and is run locally on purpose; never invoked by CI or `make test`. A
# corpus bump is its own reviewed, re-run-the-report change (ADRs 0001/0002 posture).
wpt-sync:
	./scripts/wpt-sync.sh

# Offline real-page differential report (ADR 0022): replays the captured pages in
# testdata/corpus/ through the full pipeline and scores them against the Chromium
# oracle saved at capture time. On-demand like `make wpt`, never a CI gate; skips
# when there are no captures.
corpus:
	go test -tags corpus ./corpus -run '^TestCorpus$$' -count=1 -v -timeout 50m

# Capture corpus/manifest.json with a local headless Chromium (needs network;
# local-only — captures are third-party content and are not committed).
corpus-capture:
	cd scripts/corpus && go run . $(if $(CHROME),-chrome $(CHROME)) $(ARGS)

# unblink vs. Charlotte on the captured real pages, offline, both scored against
# the capture's Chromium oracle by the corpus package's scorer (docs/corpus.md).
# Needs the captures, Node (npx fetches the pinned Charlotte) and a Chromium —
# pass the build that made the captures: CHROME=/path. CORPUS_DIR must be absolute.
corpus-compare:
	cd scripts/membench && go run -tags corpus . -root ../.. -corpus-compare $(if $(TOOLS),-tools $(TOOLS)) $(if $(CHROME),-chrome $(CHROME)) $(if $(CORPUS_DIR),-corpus-dir $(CORPUS_DIR)) $(ARGS)

# Short coverage-guided fuzzing over every parser that consumes untrusted
# bytes (HTML, reduction, PDF, robots.txt, pagination cursors). The seed
# corpora already run in `make test`; this explores beyond them. Tune the
# per-target budget with FUZZTIME.
FUZZTIME ?= 15s
fuzz:
	go test ./internal/dom -run '^$$' -fuzz '^FuzzParseExtract$$' -fuzztime $(FUZZTIME)
	go test ./internal/reduce -run '^$$' -fuzz '^FuzzReduce$$' -fuzztime $(FUZZTIME)
	go test ./internal/content/pdf -run '^$$' -fuzz '^FuzzConvert$$' -fuzztime $(FUZZTIME)
	go test ./internal/content/feed -run '^$$' -fuzz '^FuzzConvert$$' -fuzztime $(FUZZTIME)
	go test ./internal/content -run '^$$' -fuzz '^FuzzMarkdown$$' -fuzztime $(FUZZTIME)
	go test ./internal/js -run '^$$' -fuzz '^FuzzXPath$$' -fuzztime $(FUZZTIME)
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

# crossbench is membench across every AI web-tool in docs/comparison.md:
# unblink, the raw-Chromium baseline, Playwright MCP + Charlotte (fetched via
# npx at pinned versions — needs Node >= 20), and Obscura + Lightpanda (external
# binaries; see scripts/membench/README.md for install + env vars). Tools that
# aren't installed are skipped with a note. Narrow with TOOLS=name,name.
crossbench: build
	cd scripts/membench && go run . -root ../.. -tools $(or $(TOOLS),all) $(if $(CHROME),-chrome $(CHROME)) $(ARGS)

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
