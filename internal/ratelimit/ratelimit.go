// Package ratelimit provides a process-global, per-host request rate limiter so
// unblink throttles requests to each host across all its HTTP clients.
package ratelimit

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// Limiter holds a token-bucket rate limiter per host. The zero value is not
// usable; call New. A nil *Limiter is a no-op (no limiting).
type Limiter struct {
	mu    sync.Mutex
	hosts map[string]*rate.Limiter
	rps   rate.Limit
	burst int
}

// New returns a Limiter allowing rps requests/second per host with the given
// burst. rps <= 0 means unlimited.
func New(rps float64, burst int) *Limiter {
	limit := rate.Limit(rps)
	if rps <= 0 {
		limit = rate.Inf
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{hosts: make(map[string]*rate.Limiter), rps: limit, burst: burst}
}

// Wait blocks until a request to host is permitted, or ctx is done.
func (l *Limiter) Wait(ctx context.Context, host string) error {
	if l == nil {
		return nil
	}
	return l.forHost(host).Wait(ctx)
}

func (l *Limiter) forHost(host string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	rl, ok := l.hosts[host]
	if !ok {
		rl = rate.NewLimiter(l.rps, l.burst)
		l.hosts[host] = rl
	}
	return rl
}
