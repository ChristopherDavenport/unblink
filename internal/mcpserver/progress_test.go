package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/christopherdavenport/unblink/internal/browser"
)

// A map call that carries a progress token streams progress notifications while
// the walk runs, ending with the completion update; a call without a token
// stays silent.
func TestMapProgressNotifications(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "User-agent: *\nSitemap: http://%s/sitemap.xml\n", r.Host)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>http://%s/a</loc></url></urlset>`, r.Host)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><head><title>Seed</title></head><body><a href="/a">a</a></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b, err := browser.New(browser.WithAllowPrivate(true))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := New(b, Config{JSEnabled: true, SearchEnabled: true})

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	var mu sync.Mutex
	var got []*mcp.ProgressNotificationParams
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			got = append(got, req.Params)
			mu.Unlock()
		},
	})
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	params := &mcp.CallToolParams{Name: "map", Arguments: map[string]any{"url": srv.URL, "max_urls": 10}}
	params.SetProgressToken("map-progress-1")
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("call map: %v", err)
	}
	if res.IsError {
		t.Fatalf("map errored: %v", res.Content)
	}

	// Notifications ride the same in-memory conn but land on another goroutine;
	// wait briefly for the final ("map complete") one.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		var last *mcp.ProgressNotificationParams
		if n > 0 {
			last = got[n-1]
		}
		mu.Unlock()
		if last != nil && last.Message == "map complete" {
			if last.ProgressToken != "map-progress-1" {
				t.Errorf("progress token = %v, want map-progress-1", last.ProgressToken)
			}
			if last.Total != 10 {
				t.Errorf("total = %v, want 10 (the max_urls budget)", last.Total)
			}
			if last.Progress <= 0 {
				t.Errorf("final progress = %v, want > 0", last.Progress)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no completion notification after %d notification(s): %+v", n, got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Without a token: no notifications for the second call.
	mu.Lock()
	before := len(got)
	mu.Unlock()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "map", Arguments: map[string]any{"url": srv.URL, "max_urls": 10}}); err != nil {
		t.Fatalf("tokenless map: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	after := len(got)
	mu.Unlock()
	if after != before {
		t.Errorf("tokenless call produced %d notifications, want 0", after-before)
	}
}
