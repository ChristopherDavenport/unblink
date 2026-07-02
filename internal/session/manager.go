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

	// Tombstones remember evicted ids so later use reports "expired" instead of
	// silently re-creating an anonymous session. Bounded in age and count.
	tombstoneTTL = 24 * time.Hour
	tombstoneCap = 4096
)

// tombstone records why a session id left the map, so the distinction between
// "never existed" and "evicted" survives the eviction itself.
type tombstone struct {
	at       time.Time
	reason   string // "idle" | "capacity"
	hadCreds bool
}

// Manager owns the live sessions. Sessions are created lazily on first use,
// evicted after an idle TTL, and capped in number (oldest evicted on overflow).
type Manager struct {
	mu         sync.Mutex
	sessions   map[string]*Session
	tombstones map[string]tombstone
	ttl        time.Duration
	cap        int
	lastGC     time.Time // last full expiry sweep (see gcInterval)
	newClient  func(Config) (*fetch.Client, error)
	onEvict    func(*Session) // called (outside the lock) when a session leaves the map
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
		sessions:   make(map[string]*Session),
		tombstones: make(map[string]tombstone),
		ttl:        ttl,
		cap:        capacity,
		newClient:  newClient,
		onEvict:    onEvict,
	}
}

// GetOrCreate returns the session for id, creating an anonymous one on first use.
// An id that was evicted (idle TTL or capacity) returns *ExpiredError rather than
// silently re-creating the session — the caller must re-create it deliberately so
// credentials and cookies are never dropped without notice. Use NewWithConfig to
// (re-)create a session.
func (m *Manager) GetOrCreate(id string) (*Session, error) {
	var evicted []*Session
	m.mu.Lock()
	defer func() { m.mu.Unlock(); m.evictAll(evicted) }()

	evicted = m.gcLocked(false)
	if ex, ok := m.sessions[id]; ok {
		if m.expireLocked(id, ex) {
			evicted = append(evicted, ex)
			return nil, &ExpiredError{ID: id, Reason: "idle", HadCredentials: ex.Config().HasCredentials()}
		}
		ex.touch()
		return ex, nil
	}
	if ts, ok := m.tombstones[id]; ok {
		return nil, &ExpiredError{ID: id, Reason: ts.reason, HadCredentials: ts.hadCreds}
	}
	return m.createLocked(id, Config{}, &evicted)
}

// createLocked builds and registers a new session. Must be called with the
// manager lock held; capacity eviction appends to *evicted.
func (m *Manager) createLocked(id string, cfg Config, evicted *[]*Session) (*Session, error) {
	if len(m.sessions) >= m.cap {
		if v := m.evictOldestLocked(); v != nil {
			*evicted = append(*evicted, v)
		}
	}
	client, cerr := m.newClient(cfg)
	if cerr != nil {
		return nil, fmt.Errorf("session: new client: %w", cerr)
	}
	s := newSession(id, client, cfg)
	m.sessions[id] = s
	delete(m.tombstones, id) // a deliberate creation clears the id's history
	return s, nil
}

// Get returns an existing session. A missing id returns *NotFoundError; an
// evicted id returns *ExpiredError.
func (m *Manager) Get(id string) (*Session, error) {
	var evicted []*Session
	m.mu.Lock()
	defer func() { m.mu.Unlock(); m.evictAll(evicted) }()

	evicted = m.gcLocked(false)
	if s, ok := m.sessions[id]; ok {
		if m.expireLocked(id, s) {
			evicted = append(evicted, s)
			return nil, &ExpiredError{ID: id, Reason: "idle", HadCredentials: s.Config().HasCredentials()}
		}
		s.touch()
		return s, nil
	}
	if ts, ok := m.tombstones[id]; ok {
		return nil, &ExpiredError{ID: id, Reason: ts.reason, HadCredentials: ts.hadCreds}
	}
	return nil, &NotFoundError{ID: id}
}

// New creates an anonymous session, generating a random id when id is empty.
func (m *Manager) New(id string) (*Session, error) {
	return m.NewWithConfig(id, Config{})
}

// NewWithConfig creates a session with the given credential config, generating a
// random id when id is empty. Creating an evicted id succeeds (a deliberate
// re-creation clears its tombstone). If a live session with that id already
// exists: an anonymous request returns it unchanged (idempotent), but a request
// carrying credentials returns *ExistsError — credentials are fixed at creation
// and never silently ignored.
func (m *Manager) NewWithConfig(id string, cfg Config) (*Session, error) {
	if id == "" {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("session: generate id: %w", err)
		}
		id = "s-" + hex.EncodeToString(b)
	}

	var evicted []*Session
	m.mu.Lock()
	defer func() { m.mu.Unlock(); m.evictAll(evicted) }()

	evicted = m.gcLocked(false)
	if ex, ok := m.sessions[id]; ok && !m.expireLocked(id, ex) {
		if cfg.HasCredentials() || cfg.Origin != "" {
			return nil, &ExistsError{ID: id}
		}
		ex.touch()
		return ex, nil
	} else if ok {
		// Expired inline: fall through to deliberate re-creation, which clears
		// the tombstone — exactly what a post-sweep create would have done.
		evicted = append(evicted, ex)
	}
	return m.createLocked(id, cfg, &evicted)
}

// List returns the live sessions (after sweeping expired ones), in no particular
// order. Listing does not refresh the sessions' idle clocks.
func (m *Manager) List() []*Session {
	var evicted []*Session
	m.mu.Lock()
	defer func() { m.mu.Unlock(); m.evictAll(evicted) }()

	evicted = m.gcLocked(true)
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Close removes a session. It reports whether one existed. An explicit close
// frees the id for reuse (no tombstone: the caller chose to end the session).
func (m *Manager) Close(id string) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	delete(m.tombstones, id)
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

	evicted = m.gcLocked(true)
	return len(m.sessions)
}

// gcInterval amortizes the full-map expiry sweep: every lookup used to pay
// O(sessions) under the global lock. Between sweeps, the looked-up id's own
// expiry is still enforced inline (expireLocked), so per-id semantics are
// unchanged; only unrelated zombies linger up to gcInterval longer (bounded:
// the TTL default is 30m). List forces a sweep — it's the enumerator callers
// trust, and it's rare.
const gcInterval = 30 * time.Second

func (m *Manager) gcLocked(force bool) []*Session {
	if !force && time.Since(m.lastGC) < gcInterval {
		return nil
	}
	m.lastGC = time.Now()
	var evicted []*Session
	for id, s := range m.sessions {
		if s.idle() > m.ttl {
			evicted = append(evicted, s)
			delete(m.sessions, id)
			m.tombstoneLocked(id, "idle", s)
		}
	}
	m.pruneTombstonesLocked()
	return evicted
}

// expireLocked evicts s (registered under id) if it has idled past the TTL,
// reporting whether it did — the O(1) per-lookup complement to the amortized
// sweep, preserving exact eviction semantics for the id being accessed.
func (m *Manager) expireLocked(id string, s *Session) bool {
	if s.idle() <= m.ttl {
		return false
	}
	delete(m.sessions, id)
	m.tombstoneLocked(id, "idle", s)
	return true
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
		m.tombstoneLocked(oldestID, "capacity", s)
		return s
	}
	return nil
}

func (m *Manager) tombstoneLocked(id, reason string, s *Session) {
	m.tombstones[id] = tombstone{at: time.Now(), reason: reason, hadCreds: s.Config().HasCredentials()}
}

// pruneTombstonesLocked bounds the tombstone map in age and, if still over the
// cap, drops the oldest entries.
func (m *Manager) pruneTombstonesLocked() {
	for id, ts := range m.tombstones {
		if time.Since(ts.at) > tombstoneTTL {
			delete(m.tombstones, id)
		}
	}
	for len(m.tombstones) > tombstoneCap {
		var oldestID string
		var oldest time.Time
		for id, ts := range m.tombstones {
			if oldestID == "" || ts.at.Before(oldest) {
				oldestID, oldest = id, ts.at
			}
		}
		delete(m.tombstones, oldestID)
	}
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
