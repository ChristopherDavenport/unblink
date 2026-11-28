// This is a SEPARATE module (not part of the root github.com/christopherdavenport/unblink
// module) so its dependencies — chromedp for the raw-Chromium baseline and
// tiktoken-go for the offline BPE token meter — never touch the published
// binary's go.mod. Nested modules are invisible to the root's `go ./...`
// targets. Run it via `make membench` / `make crossbench` from the repo root.
//
// It requires the root module through a relative replace, for the `corpus`
// package the corpus comparison scores with (corpus_*.go, build tag `corpus`,
// `make corpus-compare`). The dependency points this way only: the replace lives
// in a module nobody installs, and the root go.mod knows nothing of this one.
module github.com/christopherdavenport/unblink/scripts/membench

go 1.27.0

toolchain go1.27.4

require (
	github.com/christopherdavenport/unblink v0.0.0
	github.com/chromedp/chromedp v0.11.2
	github.com/pkoukk/tiktoken-go v0.1.8
	github.com/pkoukk/tiktoken-go-loader v0.0.2
)

require (
	codeberg.org/readeck/go-readability/v2 v2.1.3 // indirect
	github.com/JohannesKaufmann/dom v0.3.1 // indirect
	github.com/JohannesKaufmann/html-to-markdown/v2 v2.5.2 // indirect
	github.com/andybalholm/brotli v1.3.0 // indirect
	github.com/andybalholm/cascadia v1.3.5 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/chromedp/cdproto v0.0.0-20241022234722-4d5d5faf59fb // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/dlclark/regexp2/v2 v2.11.0 // indirect
	github.com/dop251/goja v0.0.0-20261125210501-5e92a7e24d0c // indirect
	github.com/dop251/goja_nodejs v0.0.0-20260918173711-b481721df8a2 // indirect
	github.com/evanw/esbuild v0.28.2 // indirect
	github.com/go-shiori/dom v0.0.0-20230515143342-73569d674e1c // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/gogs/chardet v0.0.0-20211120154057-b7413eaefb8f // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/itlightning/dateparse v0.2.1 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/klauspost/compress v1.20.5 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/mmcdole/gofeed v1.5.0 // indirect
	github.com/mmcdole/goxpp/v2 v2.0.0 // indirect
	github.com/modelcontextprotocol/go-sdk v1.8.0 // indirect
	github.com/nationallibraryofnorway/whatwg-url v0.9.1 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/image v0.48.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

replace github.com/christopherdavenport/unblink => ../..
