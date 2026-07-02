// This is a SEPARATE module (not part of the root github.com/christopherdavenport/unblink
// module) so its chromedp dependency never touches the published binary's go.mod.
// Nested modules are invisible to the root's `go ./...` targets. Run it via
// `make membench` from the repo root.
module github.com/christopherdavenport/unblink/scripts/membench

go 1.25.0

toolchain go1.25.11

require github.com/chromedp/chromedp v0.11.2

require (
	github.com/chromedp/cdproto v0.0.0-20241022234722-4d5d5faf59fb // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	golang.org/x/sys v0.26.0 // indirect
)
