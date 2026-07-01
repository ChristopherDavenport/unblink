package search_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/search"
)

func TestSearXNGSearch(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("format = %q, want json", r.URL.Query().Get("format"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"First","url":"https://a.example/1","content":"snippet one"},
			{"title":"Second","url":"https://a.example/2","content":"snippet two"},
			{"title":"NoURL","url":"","content":"dropped"}
		]}`))
	}))
	defer srv.Close()

	p, err := search.New(search.Config{Provider: "searxng", Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "searxng" {
		t.Errorf("Name = %q", p.Name())
	}
	res, err := p.Search(context.Background(), "hello", search.Options{Count: 1, Site: "a.example"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotQuery != "hello site:a.example" {
		t.Errorf("query = %q, want %q", gotQuery, "hello site:a.example")
	}
	if len(res) != 1 { // Count=1 truncates
		t.Fatalf("got %d results, want 1 (count-capped): %+v", len(res), res)
	}
	if res[0].Title != "First" || res[0].URL != "https://a.example/1" || res[0].Snippet != "snippet one" {
		t.Errorf("result[0] = %+v", res[0])
	}
}

func TestBraveSearch(t *testing.T) {
	var gotToken, gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		gotCount = r.URL.Query().Get("count")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"B1","url":"https://b.example/1","description":"desc one"},
			{"title":"B2","url":"https://b.example/2","description":"desc two"}
		]}}`))
	}))
	defer srv.Close()

	p, err := search.New(search.Config{Provider: "brave", APIKey: "secret-key", Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := p.Search(context.Background(), "hello", search.Options{Count: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotToken != "secret-key" {
		t.Errorf("token header = %q, want secret-key", gotToken)
	}
	if gotCount != "5" {
		t.Errorf("count = %q, want 5", gotCount)
	}
	if len(res) != 2 || res[0].Snippet != "desc one" {
		t.Fatalf("results = %+v", res)
	}
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  search.Config
	}{
		{"searxng needs endpoint", search.Config{Provider: "searxng"}},
		{"brave needs key", search.Config{Provider: "brave"}},
		{"unknown provider", search.Config{Provider: "bing"}},
		{"empty provider", search.Config{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := search.New(tc.cfg); err == nil {
				t.Fatalf("expected error for %+v", tc.cfg)
			}
		})
	}
}

func TestNewValidConfigs(t *testing.T) {
	if _, err := search.New(search.Config{Provider: "searxng", Endpoint: "http://localhost:8888"}); err != nil {
		t.Errorf("searxng: %v", err)
	}
	if _, err := search.New(search.Config{Provider: "brave", APIKey: "k"}); err != nil {
		t.Errorf("brave: %v", err)
	}
	// Case-insensitive provider name.
	if _, err := search.New(search.Config{Provider: "SearXNG", Endpoint: "http://x"}); err != nil {
		t.Errorf("mixed-case provider: %v", err)
	}
}

func TestProviderStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p, _ := search.New(search.Config{Provider: "searxng", Endpoint: srv.URL})
	_, err := p.Search(context.Background(), "q", search.Options{})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("expected status 500 error, got %v", err)
	}
}
