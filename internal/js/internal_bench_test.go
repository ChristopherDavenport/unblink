package js

// In-package benchmarks — a deliberate deviation from the external _test
// convention: they isolate unexported per-render setup pieces (prelude compile,
// bridge install) that a full render hides behind the settle floor.

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"golang.org/x/net/html"
)

// BenchmarkPreludeCompile is the cost of parsing+compiling the prelude source —
// paid on every render until the prelude is a shared goja.Program.
func BenchmarkPreludeCompile(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := goja.Compile("prelude.js", preludeJS, false); err != nil {
			b.Fatalf("compile: %v", err)
		}
	}
}

// BenchmarkBridgeInstall is the full per-render setup: fresh loop + bridge
// (prototypes, constructors, globals) + prelude, with no page scripts and no
// settle poll.
func BenchmarkBridgeInstall(b *testing.B) {
	base, err := url.Parse("https://example.com/")
	if err != nil {
		b.Fatalf("base: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		doc, err := html.Parse(strings.NewReader("<html><body><p>x</p></body></html>"))
		if err != nil {
			b.Fatalf("parse: %v", err)
		}
		loop := newLoop()
		loop.Start()
		done := make(chan struct{})
		loop.RunOnLoop(func(vm *goja.Runtime) {
			br := newBridge(vm, loop, doc, base, nil, nil, nil, nil, context.Background(), time.Second, nil)
			br.install()
			_, _ = vm.RunProgram(preludeProgram)
			close(done)
		})
		<-done
		loop.Terminate()
	}
}
