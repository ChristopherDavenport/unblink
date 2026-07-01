package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/ratelimit"
)

func TestPerHostSpacing(t *testing.T) {
	// 10 rps, burst 1: the first Wait is immediate, the second waits ~100ms.
	l := ratelimit.New(10, 1)
	ctx := context.Background()

	if err := l.Wait(ctx, "example.com"); err != nil {
		t.Fatalf("wait 1: %v", err)
	}
	start := time.Now()
	if err := l.Wait(ctx, "example.com"); err != nil {
		t.Fatalf("wait 2: %v", err)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("second request was not throttled: waited %s", d)
	}
}

func TestPerHostIndependent(t *testing.T) {
	// Different hosts have independent buckets, so a fresh host is immediate.
	l := ratelimit.New(1, 1)
	ctx := context.Background()
	_ = l.Wait(ctx, "a.com")

	start := time.Now()
	if err := l.Wait(ctx, "b.com"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("a different host should not be throttled: waited %s", d)
	}
}

func TestNilLimiter(t *testing.T) {
	var l *ratelimit.Limiter
	if err := l.Wait(context.Background(), "example.com"); err != nil {
		t.Errorf("nil limiter Wait should be a no-op, got %v", err)
	}
}
