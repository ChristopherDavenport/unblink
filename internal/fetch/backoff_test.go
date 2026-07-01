package fetch

import (
	"net/http"
	"testing"
	"time"
)

func TestBackoffDelayClampsRetryAfter(t *testing.T) {
	resp := func(retryAfter string) *http.Response {
		h := http.Header{}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		return &http.Response{Header: h}
	}

	// A hostile/huge Retry-After (seconds) must be clamped, not honored verbatim.
	if d := backoffDelay(0, resp("999999")); d > maxRetryAfter {
		t.Errorf("Retry-After: 999999 not clamped: got %v, want <= %v", d, maxRetryAfter)
	}
	// A far-future HTTP-date must also be clamped.
	future := time.Now().Add(72 * time.Hour).UTC().Format(http.TimeFormat)
	if d := backoffDelay(0, resp(future)); d > maxRetryAfter {
		t.Errorf("far-future Retry-After date not clamped: got %v, want <= %v", d, maxRetryAfter)
	}
	// A small, reasonable Retry-After is honored.
	if d := backoffDelay(0, resp("2")); d != 2*time.Second {
		t.Errorf("Retry-After: 2 = %v, want 2s", d)
	}
	// No Retry-After falls back to bounded exponential backoff (<= 5s + 20% jitter).
	if d := backoffDelay(10, resp("")); d > 6*time.Second {
		t.Errorf("exponential backoff not capped: got %v", d)
	}
}
