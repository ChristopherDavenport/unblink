package js

import (
	"context"
	"net/url"
	"testing"

	"github.com/christopherdavenport/unblink/internal/webext"
)

// stubTransport records whether it was reached and returns a benign response.
type stubTransport struct{ called bool }

func (s *stubTransport) Do(ctx context.Context, method, url string, h map[string]string, b []byte) (*Response, error) {
	s.called = true
	return &Response{Status: 200}, nil
}

func newTestHost(t *testing.T, rulesJSON string) *ExtensionHost {
	t.Helper()
	rules, err := webext.ParseRules([]byte(rulesJSON))
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	m, err := webext.NewRuleMatcher(rules)
	if err != nil {
		t.Fatalf("NewRuleMatcher: %v", err)
	}
	return &ExtensionHost{bundles: []*webext.Bundle{{Net: m}}}
}

func TestBlockingTransportBlocksAndForwards(t *testing.T) {
	host := newTestHost(t, `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"||ads.example.com^","resourceTypes":["script","xmlhttprequest"]}}]`)
	inner := &stubTransport{}
	bt := &blockingTransport{inner: inner, host: host, initiator: "example.com"}

	// A matching script request is cancelled before reaching the inner transport.
	ctx := withReqType(context.Background(), webext.TypeScript)
	if _, err := bt.Do(ctx, "GET", "https://ads.example.com/a.js", nil, nil); err != errBlockedByExtension {
		t.Fatalf("blocked request: got err %v, want errBlockedByExtension", err)
	}
	if inner.called {
		t.Fatal("inner transport must not be reached for a blocked request")
	}
	if bt.blocked.Load() != 1 {
		t.Errorf("blocked counter = %d, want 1", bt.blocked.Load())
	}

	// A non-matching request is forwarded untouched.
	if _, err := bt.Do(ctx, "GET", "https://cdn.example.com/a.js", nil, nil); err != nil {
		t.Fatalf("allowed request: unexpected err %v", err)
	}
	if !inner.called {
		t.Fatal("inner transport must be reached for an allowed request")
	}
}

func TestBlockingTransportResourceTypeScoping(t *testing.T) {
	host := newTestHost(t, `[{"id":1,"action":{"type":"block"},"condition":{"urlFilter":"||t.example^","resourceTypes":["script"]}}]`)
	inner := &stubTransport{}
	bt := &blockingTransport{inner: inner, host: host, initiator: "page.test"}

	// Same URL, but issued as an XHR — the script-scoped rule must not match.
	if _, err := bt.Do(withReqType(context.Background(), webext.TypeXHR), "GET", "https://t.example/x", nil, nil); err != nil {
		t.Fatalf("xhr should be allowed by a script-scoped rule, got %v", err)
	}
	if !inner.called {
		t.Fatal("xhr should have been forwarded")
	}
}

func TestExtensionHostNilSafe(t *testing.T) {
	var h *ExtensionHost
	u, _ := url.Parse("https://x.com/a")
	if d := h.matchNetwork(webext.Request{URL: u, Type: webext.TypeScript}); d.Block {
		t.Error("nil host must never block")
	}
}
