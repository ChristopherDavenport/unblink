package browser

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/christopherdavenport/unblink/internal/robots"
	"github.com/christopherdavenport/unblink/internal/tokens"
)

// maxRulePatterns caps how many Allow/Disallow patterns the site tool reports, so
// a pathological robots.txt cannot bloat the response.
const maxRulePatterns = 20

// SiteResult is the agent-facing metadata for a host's origin: its robots.txt
// rules and its llms.txt guide. It is context only — unblink never blocks a fetch
// based on robots.txt.
type SiteResult struct {
	Origin            string         `json:"origin"`
	Robots            *RobotsSummary `json:"robots,omitempty"`
	LLMsTxt           *LLMsTxt       `json:"llms_txt,omitempty"`
	LLMsFullAvailable bool           `json:"llms_full_available"`
	LLMsFullURL       string         `json:"llms_full_url,omitempty"`
}

// RobotsSummary is the parsed, exposed view of a host's robots.txt for a standard
// browser user-agent (the "*" group).
type RobotsSummary struct {
	Status       int      `json:"status"`         // HTTP status of /robots.txt (0 = fetch error)
	Present      bool     `json:"present"`        // a usable robots.txt was found
	AllowedForUs bool     `json:"allowed_for_us"` // is the requested path allowed for "*"
	CrawlDelay   float64  `json:"crawl_delay,omitempty"`
	Sitemaps     []string `json:"sitemaps,omitempty"`
	Disallow     []string `json:"disallow,omitempty"`
	Allow        []string `json:"allow,omitempty"`
	Note         string   `json:"note,omitempty"`
}

// LLMsTxt is a host's llms.txt guide, returned inline as clean Markdown.
type LLMsTxt struct {
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	Tokens    int    `json:"tokens"`
	Truncated bool   `json:"truncated,omitempty"`
}

// RobotsHint is the lightweight robots summary folded into Browse output.
type RobotsHint struct {
	AllowedForUs bool     `json:"allowed_for_us"`
	CrawlDelay   float64  `json:"crawl_delay,omitempty"`
	Sitemaps     []string `json:"sitemaps,omitempty"`
}

// siteInfo is the cached, path-independent metadata for one origin. The
// path-specific allowed_for_us is computed per call from policy, not stored.
type siteInfo struct {
	origin string

	robotsStatus  int
	robotsPresent bool
	robotsNote    string
	policy        *robots.Policy
	crawlDelay    float64
	sitemaps      []string

	llmsPresent bool
	llmsURL     string
	llmsTitle   string
	llmsContent string
	llmsTokens  int
	llmsTrunc   bool

	llmsFull    bool
	llmsFullURL string
}

// Site returns the robots.txt + llms.txt metadata for req's host. It resolves the
// origin without mutating session state (a URL is resolved via the stateless page
// cache; a session is read at its current page) and never records history.
func (b *Browser) Site(ctx context.Context, req Request) (*SiteResult, error) {
	origin, path, err := b.originForSite(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("browser: site: %w", err)
	}
	info := b.gatherSite(ctx, origin)
	return siteResultFrom(info, path), nil
}

// originForSite resolves the origin (scheme://host) and request path for a Site
// or Browse-hint lookup, deliberately avoiding session navigation/history.
func (b *Browser) originForSite(ctx context.Context, req Request) (*url.URL, string, error) {
	var final *url.URL
	switch {
	case req.SessionID != "" && (req.UseCurrent || req.URL == ""):
		sess, ok := b.sessions.Get(req.SessionID)
		if !ok {
			return nil, "", fmt.Errorf("unknown session %q", req.SessionID)
		}
		cur := sess.Current()
		if cur == nil {
			return nil, "", fmt.Errorf("session %q has no current page; provide a url", req.SessionID)
		}
		final = cur.FinalURL
	case req.URL != "":
		// Redirect-aware origin without touching a session's jar or history.
		p, err := b.fetchStateless(ctx, req.URL, renderOpts{})
		if err != nil {
			return nil, "", err
		}
		final = p.FinalURL
	default:
		return nil, "", fmt.Errorf("a url or a session with a current page is required")
	}
	if final == nil {
		return nil, "", fmt.Errorf("could not determine the page URL")
	}
	origin := &url.URL{Scheme: final.Scheme, Host: final.Host}
	return origin, requestPath(final), nil
}

// requestPath returns the path (with query) of u for robots matching.
func requestPath(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

// gatherSite returns the cached siteInfo for origin, probing /robots.txt,
// /llms.txt, and /llms-full.txt concurrently on a miss. Probes are best-effort;
// negative results are cached too.
func (b *Browser) gatherSite(ctx context.Context, origin *url.URL) *siteInfo {
	key := origin.Scheme + "://" + origin.Host
	if info, ok := b.siteCache.get(key); ok {
		return info
	}
	info := &siteInfo{origin: key}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); b.probeRobots(ctx, key, info) }()
	go func() { defer wg.Done(); b.probeLLMs(ctx, key, info) }()
	go func() { defer wg.Done(); b.probeLLMsFull(ctx, key, info) }()
	wg.Wait()
	b.siteCache.put(key, info)
	return info
}

func (b *Browser) probeRobots(ctx context.Context, origin string, info *siteInfo) {
	res, err := b.client.Fetch(ctx, http.MethodGet, origin+"/robots.txt", nil, nil)
	if err != nil {
		slog.Debug("site: robots.txt probe failed", "origin", origin, "err", err.Error())
		info.robotsNote = "robots.txt could not be fetched (treating as allow-all)"
		return
	}
	info.robotsStatus = res.Status
	if res.Status != http.StatusOK || !looksLikeRobots(res.Header.Get("Content-Type"), res.Body) {
		info.robotsNote = robotsAbsentNote(res.Status)
		return
	}
	pol := robots.Parse(res.Body)
	info.robotsPresent = true
	info.policy = pol
	info.sitemaps = pol.Sitemaps
	if g := pol.StarGroup(); g != nil {
		info.crawlDelay = g.CrawlDelay
	}
}

func (b *Browser) probeLLMs(ctx context.Context, origin string, info *siteInfo) {
	llmsURL := origin + "/llms.txt"
	res, err := b.client.Fetch(ctx, http.MethodGet, llmsURL, nil, nil)
	if err != nil {
		slog.Debug("site: llms.txt probe failed", "origin", origin, "err", err.Error())
		return
	}
	if res.Status != http.StatusOK || !looksLikeLLMsTxt(res.Header.Get("Content-Type"), res.Body) {
		return
	}
	content, count, trunc := truncateToTokens(string(res.Body), DefaultLLMsMaxTokens)
	info.llmsPresent = true
	info.llmsURL = llmsURL
	info.llmsTitle = llmsTitle(content)
	info.llmsContent = content
	info.llmsTokens = count
	info.llmsTrunc = trunc
}

func (b *Browser) probeLLMsFull(ctx context.Context, origin string, info *siteInfo) {
	fullURL := origin + "/llms-full.txt"
	res, err := b.client.Fetch(ctx, http.MethodHead, fullURL, nil, nil)
	if err != nil {
		slog.Debug("site: llms-full.txt HEAD failed", "origin", origin, "err", err.Error())
		return
	}
	// Require a plausible text content-type: a soft-404 HTML shell answers 200
	// with text/html, which we must not mistake for an llms-full.txt.
	if res.Status == http.StatusOK && plausibleTextType(res.Header.Get("Content-Type")) {
		info.llmsFull = true
		info.llmsFullURL = fullURL
	}
}

// siteResultFrom builds the public SiteResult, computing the path-specific
// allowed_for_us from the cached policy.
func siteResultFrom(info *siteInfo, path string) *SiteResult {
	res := &SiteResult{
		Origin:            info.origin,
		LLMsFullAvailable: info.llmsFull,
		LLMsFullURL:       info.llmsFullURL,
	}
	rs := &RobotsSummary{
		Status:       info.robotsStatus,
		Present:      info.robotsPresent,
		AllowedForUs: true,
		CrawlDelay:   info.crawlDelay,
		Sitemaps:     info.sitemaps,
		Note:         info.robotsNote,
	}
	if g := info.policy.StarGroup(); g != nil {
		rs.AllowedForUs = g.Allowed(path)
		for _, r := range g.Rules {
			if r.Pattern == "" {
				continue
			}
			if r.Allow && len(rs.Allow) < maxRulePatterns {
				rs.Allow = append(rs.Allow, r.Pattern)
			} else if !r.Allow && len(rs.Disallow) < maxRulePatterns {
				rs.Disallow = append(rs.Disallow, r.Pattern)
			}
		}
	}
	res.Robots = rs
	if info.llmsPresent {
		res.LLMsTxt = &LLMsTxt{
			URL:       info.llmsURL,
			Title:     info.llmsTitle,
			Content:   info.llmsContent,
			Tokens:    info.llmsTokens,
			Truncated: info.llmsTrunc,
		}
	}
	return res
}

// siteHint produces the lightweight llms.txt/robots hints folded into Browse,
// reusing the host cache so repeat browses pay nothing.
func (b *Browser) siteHint(ctx context.Context, final *url.URL) (bool, *RobotsHint) {
	if final == nil {
		return false, nil
	}
	origin := &url.URL{Scheme: final.Scheme, Host: final.Host}
	info := b.gatherSite(ctx, origin)
	hint := &RobotsHint{
		AllowedForUs: info.policy.StarGroup().Allowed(requestPath(final)),
		CrawlDelay:   info.crawlDelay,
		Sitemaps:     capStrings(info.sitemaps, 3),
	}
	return info.llmsPresent, hint
}

// --- content sniffing & shaping ---

// looksLikeRobots guards against soft-404 / SPA shells that 200 their HTML for
// every path: a usable robots.txt is non-HTML and is either served as text or
// parses to at least one directive.
func looksLikeRobots(contentType string, body []byte) bool {
	if isHTML(contentType, body) {
		return false
	}
	if plausibleTextType(contentType) {
		return true
	}
	pol := robots.Parse(body)
	return len(pol.Groups) > 0 || len(pol.Sitemaps) > 0
}

// looksLikeLLMsTxt rejects HTML shells and accepts a body that is served as text
// or whose first line looks like Markdown.
func looksLikeLLMsTxt(contentType string, body []byte) bool {
	if isHTML(contentType, body) {
		return false
	}
	if plausibleTextType(contentType) {
		return true
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return false
	}
	first := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		first = s[:i]
	}
	first = strings.TrimSpace(first)
	return strings.HasPrefix(first, "#") || strings.HasPrefix(first, ">") || strings.HasPrefix(first, "-")
}

func plausibleTextType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/plain") ||
		strings.Contains(ct, "text/markdown") ||
		strings.Contains(ct, "text/x-markdown")
}

func isHTML(ct string, body []byte) bool {
	if strings.Contains(strings.ToLower(ct), "text/html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))
	return strings.HasPrefix(head, "<!doctype html") ||
		strings.HasPrefix(head, "<html") ||
		strings.HasPrefix(head, "<?xml")
}

// truncateToTokens caps s to roughly maxTokens, cutting on a line boundary, and
// reports the estimated token count and whether it truncated.
func truncateToTokens(s string, maxTokens int) (string, int, bool) {
	if tokens.Estimate(s) <= maxTokens {
		return s, tokens.Estimate(s), false
	}
	limit := maxTokens * 4 // ~4 chars per token (matches tokens.Heuristic)
	if limit > len(s) {
		limit = len(s)
	}
	cut := s[:limit]
	if nl := strings.LastIndexByte(cut, '\n'); nl > 0 {
		cut = cut[:nl]
	}
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	cut = strings.TrimRight(cut, "\n") + "\n\n…(content truncated)\n"
	return cut, tokens.Estimate(cut), true
}

// llmsTitle returns the first Markdown H1 (`# Title`) near the top of s.
func llmsTitle(s string) string {
	for _, line := range strings.SplitN(s, "\n", 32) {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(t, "# "))
		}
	}
	return ""
}

func robotsAbsentNote(status int) string {
	switch {
	case status == http.StatusNotFound:
		return "no robots.txt (allow-all)"
	case status >= 500:
		return fmt.Sprintf("robots.txt returned %d; treating as allow-all", status)
	default:
		return "no usable robots.txt (allow-all)"
	}
}

func capStrings(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
