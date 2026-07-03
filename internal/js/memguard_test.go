package js_test

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/js"
)

// TestMemoryGuardInterruptsAllocationBomb proves a render whose script allocates
// unboundedly is interrupted by the memory guard rather than ballooning the
// process. The bomb allocates in a tight loop; with a low limit the watchdog
// fires within a sample interval or two and the render returns.
func TestMemoryGuardInterruptsAllocationBomb(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates aggressively; skipped under -short")
	}
	doc, err := html.Parse(strings.NewReader(`<html><body><div id="out">start</div>
		<script>
		  var sink = [];
		  // Grow the heap without bound; each push retains ~8MB.
		  while (true) { sink.push(new Array(1024 * 1024).fill(7)); }
		</script></body></html>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/")
	// 64 MiB limit, generous 10s budget: the guard (not the wall clock) must be
	// what ends this. A returned render within the budget proves containment.
	eng := js.New(js.WithTimeout(10*time.Second), js.WithMemoryLimit(64*1024*1024))
	defer eng.Close()

	var diag js.RenderResult
	done := make(chan error, 1)
	go func() { done <- eng.Render(context.Background(), doc, base, js.Env{Diag: &diag}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("render returned error (want clean interrupt): %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("render did not return: memory guard failed to interrupt the allocation bomb")
	}
	// The interrupt surfaces as a diagnostic error mentioning the memory limit.
	joined := strings.Join(diag.Errors, "\n")
	if !strings.Contains(joined, "memory limit") {
		t.Errorf("expected a memory-limit interrupt error, got: %v", diag.Errors)
	}
}

// TestMemoryGuardDisabledByZero proves WithMemoryLimit(0) installs no guard and a
// normal page still renders (the guard must never interfere with legitimate work).
func TestMemoryGuardDisabledByZero(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body><div id="out">?</div>
		<script>document.getElementById('out').textContent = 'ok';</script></body></html>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base, _ := url.Parse("https://example.com/")
	eng := js.New(js.WithTimeout(2*time.Second), js.WithMemoryLimit(0))
	defer eng.Close()
	if err := eng.Render(context.Background(), doc, base, js.Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var buf bytes.Buffer
	_ = html.Render(&buf, doc)
	if !strings.Contains(buf.String(), ">ok<") {
		t.Errorf("normal render did not complete with guard disabled:\n%s", buf.String())
	}
}
