package fetch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// A WithRedirectCheck hook sees each redirect hop before it is requested; a
// refusal stops the chain there, is not retried, and is not logged as a fetch
// failure. Without the hook on the context the same client follows the redirect.
func TestRedirectCheckRefusesHop(t *testing.T) {
	var targetHits, startHits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			atomic.AddInt64(&startHits, 1)
			http.Redirect(w, r, "/target", http.StatusFound)
		default:
			atomic.AddInt64(&targetHits, 1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("target body"))
		}
	}))
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	c, err := New(WithRetries(2))
	if err != nil {
		t.Fatal(err)
	}
	errPolicy := errors.New("policy says no")
	var seen []string
	ctx := WithRedirectCheck(context.Background(), func(u *url.URL) error {
		seen = append(seen, u.Path)
		return errPolicy
	})

	_, err = c.Get(ctx, srv.URL+"/start")
	if !errors.Is(err, errPolicy) {
		t.Fatalf("err = %v, want it to wrap the hook's error", err)
	}
	var refused *RedirectRefusedError
	if !errors.As(err, &refused) || refused.URL.Path != "/target" {
		t.Fatalf("err = %v, want a RedirectRefusedError for /target", err)
	}
	if len(seen) != 1 || seen[0] != "/target" {
		t.Errorf("hook saw %v, want exactly [/target]", seen)
	}
	if n := atomic.LoadInt64(&targetHits); n != 0 {
		t.Errorf("refused target was requested %d time(s)", n)
	}
	if n := atomic.LoadInt64(&startHits); n != 1 {
		t.Errorf("start requested %d time(s), want 1 (a refusal must not be retried)", n)
	}
	if strings.Contains(logs.String(), "fetch error") || strings.Contains(logs.String(), "fetch retry") {
		t.Errorf("a policy refusal was logged at warn:\n%s", logs.String())
	}

	// The hook is per-request: the same client without it follows the redirect.
	p, err := c.Get(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatalf("unhooked get: %v", err)
	}
	if string(p.Raw) != "target body" {
		t.Errorf("unhooked body = %q", p.Raw)
	}
}
