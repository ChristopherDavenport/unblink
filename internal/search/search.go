// Package search adds an optional web-search entry point behind a small Provider
// interface, so unblink stays fully self-contained by default and only reaches an
// external service when the operator configures one. Two pure-HTTP+JSON adapters
// ship: self-hosted SearXNG (no key) and the Brave Search API (one key). HTML
// scraping is deliberately excluded — it would drag internal/dom into search.
//
// Security notes:
//   - The search client is a plain timeout http.Client with no SSRF dial guard:
//     the endpoint is operator-configured and trusted, and a result URL is only
//     ever fetched later through unblink's SSRF-guarded page path (read/browse),
//     never by this package.
//   - The Brave API key is supplied from the environment by the caller, sent only
//     as a request header, and never logged, echoed, or exposed via Name().
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// Options tunes a single search.
type Options struct {
	Count int    // desired maximum results (0 = provider default)
	Site  string // restrict to this domain via a "site:" filter (optional)
}

// Provider runs a query against a search backend.
type Provider interface {
	Search(ctx context.Context, query string, opts Options) ([]Result, error)
	// Name reports the provider id (e.g. "searxng", "brave"). It never returns
	// any secret.
	Name() string
}

// Config selects and configures a Provider. APIKey must come from the
// environment (never a CLI flag) so it stays out of the process argv.
type Config struct {
	Provider string        // "searxng" | "brave"
	Endpoint string        // SearXNG base URL; optional Brave endpoint override
	APIKey   string        // Brave subscription token (from env)
	Timeout  time.Duration // per-request timeout (0 = defaultTimeout)
}

const (
	defaultTimeout = 10 * time.Second
	maxBodyBytes   = 4 << 20 // 4 MiB cap on a provider response
	braveMaxCount  = 20      // Brave's per-request result ceiling
)

// New builds a Provider from cfg. It validates the fields each provider requires
// and returns an error for an unknown provider or missing configuration.
func New(cfg Config) (Provider, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	hc := &http.Client{Timeout: timeout}

	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "searxng":
		endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
		if endpoint == "" {
			return nil, fmt.Errorf("search: searxng requires --search-endpoint (the SearXNG base URL)")
		}
		return &searxng{endpoint: endpoint, http: hc}, nil
	case "brave":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, fmt.Errorf("search: brave requires an API key in UNBLINK_SEARCH_API_KEY")
		}
		endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
		if endpoint == "" {
			endpoint = "https://api.search.brave.com/res/v1/web/search"
		}
		return &brave{apiKey: cfg.APIKey, endpoint: endpoint, http: hc}, nil
	default:
		return nil, fmt.Errorf("search: unknown provider %q (want searxng|brave)", cfg.Provider)
	}
}

// withSite appends a "site:<domain>" filter to the query when site is set.
func withSite(query, site string) string {
	site = strings.TrimSpace(site)
	if site == "" {
		return query
	}
	return query + " site:" + site
}

// getJSON performs a GET and decodes the JSON body into v, applying the body cap
// and a status check. hdr may be nil.
func getJSON(ctx context.Context, hc *http.Client, endpoint string, q url.Values, hdr map[string]string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, val := range hdr {
		req.Header.Set(k, val)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// truncate caps results to n (n <= 0 leaves them unchanged).
func truncate(rs []Result, n int) []Result {
	if n > 0 && len(rs) > n {
		return rs[:n]
	}
	return rs
}

// --- SearXNG (self-hosted, /search?format=json) ---

type searxng struct {
	endpoint string
	http     *http.Client
}

func (s *searxng) Name() string { return "searxng" }

func (s *searxng) Search(ctx context.Context, query string, opts Options) ([]Result, error) {
	q := url.Values{}
	q.Set("q", withSite(query, opts.Site))
	q.Set("format", "json")
	q.Set("categories", "general")

	var body struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := getJSON(ctx, s.http, s.endpoint+"/search", q, nil, &body); err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	out := make([]Result, 0, len(body.Results))
	for _, r := range body.Results {
		if r.URL == "" {
			continue
		}
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return truncate(out, opts.Count), nil
}

// --- Brave Search API ---

type brave struct {
	apiKey   string
	endpoint string
	http     *http.Client
}

func (b *brave) Name() string { return "brave" }

func (b *brave) Search(ctx context.Context, query string, opts Options) ([]Result, error) {
	q := url.Values{}
	q.Set("q", withSite(query, opts.Site))
	if opts.Count > 0 {
		count := opts.Count
		if count > braveMaxCount {
			count = braveMaxCount
		}
		q.Set("count", fmt.Sprintf("%d", count))
	}
	hdr := map[string]string{"X-Subscription-Token": b.apiKey}

	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := getJSON(ctx, b.http, b.endpoint, q, hdr, &body); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	out := make([]Result, 0, len(body.Web.Results))
	for _, r := range body.Web.Results {
		if r.URL == "" {
			continue
		}
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return truncate(out, opts.Count), nil
}
