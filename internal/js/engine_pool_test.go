package js

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

const poolPage = `<html><body><div id="x">before</div>` +
	`<script>document.getElementById('x').textContent = 'ok';</script></body></html>`

func renderOnce(t *testing.T, e *Engine) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(poolPage))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := e.Render(context.Background(), doc, nil, Env{}); err != nil {
		t.Fatalf("render: %v", err)
	}
	var sb strings.Builder
	if err := html.Render(&sb, doc); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return sb.String()
}

func TestPrewarmFillsAndRenders(t *testing.T) {
	e := New(WithTimeout(2*time.Second), WithPrewarm(2))
	defer e.Close()

	// The background refiller should fill the pool.
	deadline := time.Now().Add(time.Second)
	for len(e.pool) < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if len(e.pool) == 0 {
		t.Fatalf("pre-warm pool never filled")
	}

	// Render several pages in a row — the pool drains and refills, every render
	// (pooled or inline-on-miss) must mutate the DOM identically.
	for i := 0; i < 5; i++ {
		out := renderOnce(t, e)
		if !strings.Contains(out, ">ok<") || strings.Contains(out, ">before<") {
			t.Fatalf("render %d did not mutate via pool:\n%s", i, out)
		}
	}
}

func TestNoPoolByDefault(t *testing.T) {
	e := New() // prewarm 0
	if e.pool != nil {
		t.Error("default engine should have no pre-warm pool")
	}
	if out := renderOnce(t, e); !strings.Contains(out, ">ok<") {
		t.Errorf("inline (no-pool) render failed:\n%s", out)
	}
	e.Close() // no-op, safe
}

func TestCloseIdempotent(t *testing.T) {
	e := New(WithPrewarm(1))
	e.Close()
	e.Close() // must not panic on double close
}
