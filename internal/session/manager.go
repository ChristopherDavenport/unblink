package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/christopherdavenport/unblink/internal/fetch"
)

// Defaults for the session manager.
const (
	DefaultTTL = 30 * time.Minute
	DefaultCap = 256
)

// Manager owns the live sessions. Sessions are created lazily on first use,
// evicted after an idle TTL, and capped in number (oldest evicted on overflow).
type Manager struct {
	mu        sync.Mutex
	sessions  map[string]*Session
	ttl       time.Duration
	cap       int
	newClient func(Config) (*fetch.Client, error)
	onEvict   func(*Session) // called (outside the lock) when a session leaves the map
}

// NewManager returns a Manager. newClient builds a fresh fetch.Client (with its own
// cookie jar) for each new session, configured with the session's credentials.
// onEvict, if non-nil, is invoked for every session removed by GC, capacity
// eviction, Close, or CloseAll — always after the manager lock is released, so it
// may block (e.g. tearing down a live JS runtime).
func NewManager(ttl time.Duration, capacity int, newClient func(Config) (*fetch.Client, error), onEvict func(*Session)) *Manager {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if capacity <= 0 {
		capacity = DefaultCap
	}
	return &Manager{
		sessions:  make(map[string]*Session),
		ttl:       ttl,
		cap:       capacity,
		newClient: newClient,
		onEvict:   onEvict,
	}
}

// GetOrCreate returns the session for id, creating an anonymous one on first use.
// Use NewWithConfig to create a credentialed session.
func (m *Manager) GetOrCreate(id string) (*Session, error) {
	return m.getOrCreate(id, Config{})
}

// getOrCreate returns the session for id, creating it with cfg on first use. cfg is
// ignored when a session already exists (credentials are fixed at creation).
func (m *Manager) getOrCreate(id string, cfg Config) (s *Session, err error) {
	var evicted []*Session
	m.mu.Lock()
	defer func() { m.mu.Unlock(); m.evictAll(evicted) }()

	evicted = m.gcLocked()
	if ex, ok := m.sessions[id]; ok {
		ex.touch()
		return ex, nil
	}
	if len(m.sessions) >= m.cap {
		if v := m.evictOldestLocked(); v != nil {
			evicted = append(evicted, v)
		}
	}
	client, cerr := m.newClient(cfg)
	if cerr != nil {
		return nil, fmt.Errorf("session: new client: %w", cerr)
	}
	s = newSession(id, client, cfg)
	m.sessions[id] = s
	return s, nil
}

// Get returns an existing session without creating one.
func (m *Manager) Get(id string) (s *Session, ok bool) {
	var evicted []*Session
	m.mu.Lock()
	defer func() { m.mu.Unlock(); m.evictAll(evicted) }()

	evicted = m.gcLocked()
	s, ok = m.sessions[id]
	if ok {
		s.touch()
	}
	return s, ok
}

// New creates an anonymous session, generating a random id when id is empty.
func (m *Manager) New(id string) (*Session, error) {
	return m.NewWithConfig(id, Config{})
}

// NewWithConfig creates a session with the given credential config, generating a
// random id when id is empty. If a session with that id already exists, it is
// returned unchanged (credentials are fixed at creation).
func (m *Manager) NewWithConfig(id string, cfg Config) (*Session, error) {
	if id == "" {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("session: generate id: %w", err)
		}
		id = "s-" + hex.EncodeToString(b)
	}
	return m.getOrCreate(id, cfg)
}

// Close removes a session. It reports whether one existed.
func (m *Manager) Close(id string) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if ok && m.onEvict != nil {
		m.onEvict(s)
	}
	return ok
}

// CloseAll removes and tears down every session (shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		all = append(all, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	m.evictAll(all)
}

// Len returns the number of live sessions (after sweeping expired ones).
func (m *Manager) Len() int {
	var evicted []*Session
	m.mu.Lock()
	defer func() { m.mu.Unlock(); m.evictAll(evicted) }()

	evicted = m.gcLocked()
	return len(m.sessions)
}

func (m *Manager) gcLocked() []*Session {
	var evicted []*Session
	for id, s := range m.sessions {
		if s.idle() > m.ttl {
			evicted = append(evicted, s)
			delete(m.sessions, id)
		}
	}
	return evicted
}

func (m *Manager) evictOldestLocked() *Session {
	var oldestID string
	oldest := time.Duration(-1)
	for id, s := range m.sessions {
		if d := s.idle(); d > oldest {
			oldest, oldestID = d, id
		}
	}
	if oldestID != "" {
		s := m.sessions[oldestID]
		delete(m.sessions, oldestID)
		return s
	}
	return nil
}

// evictAll runs the onEvict hook for each removed session. Must be called with the
// manager lock released.
func (m *Manager) evictAll(evicted []*Session) {
	if m.onEvict == nil {
		return
	}
	for _, s := range evicted {
		m.onEvict(s)
	}
}
