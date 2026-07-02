// Package ratelimit provides a process-global, per-host request rate limiter so
// unblink throttles requests to each host across all its HTTP clients.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Idle host entries are pruned so a long-lived server crawling many hosts does
// not retain one limiter per host forever. Pruning only kicks in past the
// threshold, so small maps never pay the sweep.
const (
	pruneThreshold = 1024
	pruneIdle      = time.Hour
)

type hostLimiter struct {
	rl      *rate.Limiter
	lastUse time.Time
}

// Limiter holds a token-bucket rate limiter per host. The zero value is not
// usable; call New. A nil *Limiter is a no-op (no limiting).
type Limiter struct {
	mu    sync.Mutex
	hosts map[string]*hostLimiter
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
	return &Limiter{hosts: make(map[string]*hostLimiter), rps: limit, burst: burst}
}

// Wait blocks until a request to host is permitted, or ctx is done.
func (l *Limiter) Wait(ctx context.Context, host string) error {
	if l == nil {
		return nil
	}
	return l.forHost(host).Wait(ctx)
}

func (l *Limiter) forHost(host string) *rate.Limiter {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.hosts[host]
	if !ok {
		if len(l.hosts) >= pruneThreshold {
			l.pruneLocked(now)
		}
		h = &hostLimiter{rl: rate.NewLimiter(l.rps, l.burst)}
		l.hosts[host] = h
	}
	h.lastUse = now
	return h.rl
}

// pruneLocked drops hosts idle past pruneIdle. An idle host's bucket is full
// anyway, so recreating it later is behaviorally identical.
func (l *Limiter) pruneLocked(now time.Time) {
	for host, h := range l.hosts {
		if now.Sub(h.lastUse) > pruneIdle {
			delete(l.hosts, host)
		}
	}
}
