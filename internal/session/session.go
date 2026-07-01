// Package session holds unblink's per-session state: a cookie jar (via a
// dedicated fetch.Client), a navigation history, and an optional persistent
// JavaScript runtime ("live context") bound to the current page. Sessions let an
// agent perform multi-step flows — log in, follow links, submit forms, drive a JS
// app — with cookies and live page state carried across calls. It imports fetch,
// page, and js (for the LiveContext handle); none of those import session, so there
// is no dependency cycle with browser.
package session

import (
	"net/http"
	"sync"
	"time"

	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/page"
)

// Config carries a session's injected credentials. All of it is pinned to a single
// Origin ("scheme://host"): headers/auth are sent only to that origin, and cookies
// are seeded into the jar for it. The zero value configures an anonymous session.
type Config struct {
	Headers   map[string]string
	Bearer    string
	BasicUser string
	BasicPass string
	Cookies   []*http.Cookie
	Origin    string
}

// HasCredentials reports whether any auth/header/cookie is configured.
func (c Config) HasCredentials() bool {
	return c.Bearer != "" || c.BasicUser != "" || len(c.Headers) > 0 || len(c.Cookies) > 0
}

// Session is a single browsing context: one cookie jar, a history stack with a
// current-position pointer (back/forward navigation), and at most one live JS
// runtime bound to the current page. It is safe for concurrent use.
type Session struct {
	ID     string
	client *fetch.Client
	cfg    Config // injected credentials; immutable after creation

	mu       sync.Mutex
	history  []*page.Page
	pos      int            // index of the current page; -1 when empty
	live     js.LiveContext // persistent JS runtime for the current page; nil when none
	livePos  int            // history index live is bound to; -1 when none
	lastUsed time.Time
}

func newSession(id string, client *fetch.Client, cfg Config) *Session {
	return &Session{ID: id, client: client, cfg: cfg, pos: -1, livePos: -1, lastUsed: time.Now()}
}

// Client returns the session's fetch client (which owns its cookie jar).
func (s *Session) Client() *fetch.Client { return s.client }

// Config returns the session's credential configuration (set at creation).
func (s *Session) Config() Config { return s.cfg }

// Current returns the page at the history pointer, or nil if none.
func (s *Session) Current() *page.Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos < 0 || s.pos >= len(s.history) {
		return nil
	}
	return s.history[s.pos]
}

// Live returns the persistent JS runtime bound to the current page, or nil if the
// current page has none (e.g. after a navigation, which drops the prior runtime).
func (s *Session) Live() js.LiveContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != nil && s.livePos == s.pos {
		return s.live
	}
	return nil
}

// SetLive attaches a live runtime to the current page, closing any previous one.
func (s *Session) SetLive(lc js.LiveContext) {
	s.mu.Lock()
	old := s.live
	s.live, s.livePos = lc, s.pos
	s.mu.Unlock()
	if old != nil && old != lc {
		old.Close()
	}
}

// dropLiveLocked clears the live runtime and returns it so the caller can Close it
// after releasing the lock (Close terminates an event loop and must not run under
// the session mutex).
func (s *Session) dropLiveLocked() js.LiveContext {
	lc := s.live
	s.live, s.livePos = nil, -1
	return lc
}

// Close tears down the session's live runtime (if any). Used by the manager on
// eviction/close and at shutdown.
func (s *Session) Close() {
	s.mu.Lock()
	old := s.dropLiveLocked()
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// Visit appends p as the new current page, discarding any forward history (the
// standard browser model). The prior page's live runtime is dropped — a fresh
// navigation is a new page, hence a new runtime.
func (s *Session) Visit(p *page.Page) {
	s.mu.Lock()
	if s.pos < len(s.history)-1 {
		s.history = s.history[:s.pos+1]
	}
	s.history = append(s.history, p)
	s.pos = len(s.history) - 1
	old := s.dropLiveLocked()
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// ReplaceCurrentPage swaps the current page in place (no history push, no live
// runtime change). Used to store a fresh snapshot of a live page after an
// interaction or read — a same-page DOM update, not a navigation.
func (s *Session) ReplaceCurrentPage(p *page.Page) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pos >= 0 && s.pos < len(s.history) {
		s.history[s.pos] = p
	}
}

// Back moves the pointer to the previous page and returns it. ok is false if
// already at the start. The current page's live runtime is dropped.
func (s *Session) Back() (*page.Page, bool) {
	s.mu.Lock()
	if s.pos <= 0 {
		s.mu.Unlock()
		return nil, false
	}
	s.pos--
	old := s.dropLiveLocked()
	p := s.history[s.pos]
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return p, true
}

// Forward moves the pointer to the next page and returns it. ok is false if
// already at the end. The current page's live runtime is dropped.
func (s *Session) Forward() (*page.Page, bool) {
	s.mu.Lock()
	if s.pos >= len(s.history)-1 {
		s.mu.Unlock()
		return nil, false
	}
	s.pos++
	old := s.dropLiveLocked()
	p := s.history[s.pos]
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return p, true
}

// HistoryURLs returns the final URLs of visited pages and the current position.
func (s *Session) HistoryURLs() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	urls := make([]string, 0, len(s.history))
	for _, p := range s.history {
		u := ""
		if p.FinalURL != nil {
			u = p.FinalURL.String()
		}
		urls = append(urls, u)
	}
	return urls, s.pos
}

func (s *Session) touch() {
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

func (s *Session) idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastUsed)
}
