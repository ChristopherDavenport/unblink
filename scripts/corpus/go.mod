// This is a SEPARATE module (not part of the root github.com/christopherdavenport/unblink
// module), like scripts/membench: the capture tool needs a real Chromium and the
// live network, neither of which the published binary or its tests may depend
// on. It is stdlib-only (Chromium is driven over --remote-debugging-pipe, so
// there is no CDP client dependency to drift behind the browser). Nested modules
// are invisible to the root's `go ./...` targets. Run it via `make corpus-capture`.
module github.com/christopherdavenport/unblink/scripts/corpus

go 1.25.0
