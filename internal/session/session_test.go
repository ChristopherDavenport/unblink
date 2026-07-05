package session_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/christopherdavenport/unblink/internal/fetch"
	"github.com/christopherdavenport/unblink/internal/js"
	"github.com/christopherdavenport/unblink/internal/page"
	"github.com/christopherdavenport/unblink/internal/session"
)

func newClient(session.Config) (*fetch.Client, error) { return fetch.New() }

// fakeLive is a stub js.LiveContext that records whether Close was called.
type fakeLive struct{ closed bool }

func (f *fakeLive) Dispatch(context.Context, js.Action) (js.DispatchResult, error) {
	return js.DispatchResult{}, nil
}
func (f *fakeLive) Snapshot(context.Context) ([]byte, uint64, error)  { return nil, 0, nil }
func (f *fakeLive) DOMVersion(context.Context) (uint64, error)        { return 0, nil }
func (f *fakeLive) PendingNavigation(context.Context) (string, error) { return "", nil }
func (f *fakeLive) Close()                                            { f.closed = true }

func makePage(rawURL string) *page.Page {
	u, _ := url.Parse(rawURL)
	return &page.Page{FinalURL: u}
}

// TestStorageOriginPartitioned confirms Storage/SessionStorage return a distinct
// bag per origin and the same bag for a repeated origin (ADR 0015).
func TestStorageOriginPartitioned(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s, err := m.GetOrCreate("tab")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	a := "https://a.com"
	bOrigin := "https://b.com"
	s.Storage(a).Set("k", "va")
	s.Storage(bOrigin).Set("k", "vb")
	if v, _ := s.Storage(a).Get("k"); v != "va" {
		t.Errorf("origin a storage = %q, want va", v)
	}
	if v, _ := s.Storage(bOrigin).Get("k"); v != "vb" {
		t.Errorf("origin b storage = %q, want vb (must not see a's value)", v)
	}
	first := s.Storage(a)
	if first != s.Storage(a) {
		t.Error("same origin should return the same store")
	}
	// localStorage and sessionStorage are separate areas even for the same origin.
	s.SessionStorage(a).Set("k", "session-va")
	if v, _ := s.Storage(a).Get("k"); v != "va" {
		t.Errorf("sessionStorage bled into localStorage: %q", v)
	}
}

func TestGetOrCreateLazy(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s1, err := m.GetOrCreate("a")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s2, err := m.GetOrCreate("a")
	if err != nil {
		t.Fatalf("reget: %v", err)
	}
	if s1 != s2 {
		t.Error("GetOrCreate returned a different session for the same id")
	}
	if m.Len() != 1 {
		t.Errorf("len = %d, want 1", m.Len())
	}
}

func TestNewGeneratesID(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s, err := m.New("")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if len(s.ID) == 0 || s.ID[:2] != "s-" {
		t.Errorf("generated id = %q", s.ID)
	}
}

func TestCloseRemoves(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	if _, err := m.GetOrCreate("a"); err != nil {
		t.Fatal(err)
	}
	if !m.Close("a") {
		t.Error("close returned false for existing session")
	}
	// An explicitly closed id is unknown (not expired): the caller freed it.
	var nf *session.NotFoundError
	if _, err := m.Get("a"); !errors.As(err, &nf) {
		t.Errorf("Get after close = %v, want NotFoundError", err)
	}
}

func TestTTLExpiryReportsExpired(t *testing.T) {
	m := session.NewManager(time.Millisecond, 10, newClient, nil)
	if _, err := m.GetOrCreate("a"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)

	// An evicted id must NOT be silently re-created: both lookups report expiry.
	var exp *session.ExpiredError
	if _, err := m.GetOrCreate("a"); !errors.As(err, &exp) {
		t.Fatalf("GetOrCreate after TTL = %v, want ExpiredError", err)
	}
	if exp.Reason != "idle" {
		t.Errorf("reason = %q, want idle", exp.Reason)
	}
	if _, err := m.Get("a"); !errors.As(err, &exp) {
		t.Errorf("Get after TTL = %v, want ExpiredError", err)
	}

	// A deliberate re-creation succeeds and clears the tombstone.
	if _, err := m.New("a"); err != nil {
		t.Fatalf("re-create after expiry: %v", err)
	}
	if _, err := m.GetOrCreate("a"); err != nil {
		t.Errorf("GetOrCreate after re-create: %v", err)
	}
}

func TestExpiredCredentialedSessionKeepsCredentialFlag(t *testing.T) {
	m := session.NewManager(time.Millisecond, 10, newClient, nil)
	if _, err := m.NewWithConfig("auth", session.Config{Bearer: "tok", Origin: "https://e.com"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	var exp *session.ExpiredError
	if _, err := m.GetOrCreate("auth"); !errors.As(err, &exp) {
		t.Fatalf("GetOrCreate after TTL = %v, want ExpiredError", err)
	}
	if !exp.HadCredentials {
		t.Error("ExpiredError should report the evicted session carried credentials")
	}
}

func TestNewWithConfigOnLiveSession(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s1, err := m.New("a")
	if err != nil {
		t.Fatal(err)
	}
	// Anonymous re-new is idempotent.
	s2, err := m.New("a")
	if err != nil || s1 != s2 {
		t.Errorf("anonymous re-new = %v, %v; want same session", s2, err)
	}
	// Re-new with credentials must error, never silently keep the old config.
	var ex *session.ExistsError
	if _, err := m.NewWithConfig("a", session.Config{Bearer: "tok", Origin: "https://e.com"}); !errors.As(err, &ex) {
		t.Errorf("credentialed re-new = %v, want ExistsError", err)
	}
}

func TestCapEviction(t *testing.T) {
	m := session.NewManager(time.Hour, 2, newClient, nil)
	if _, err := m.GetOrCreate("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetOrCreate("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetOrCreate("c"); err != nil { // evicts oldest ("a")
		t.Fatal(err)
	}
	var exp *session.ExpiredError
	if _, err := m.Get("a"); !errors.As(err, &exp) || exp.Reason != "capacity" {
		t.Errorf("Get(a) = %v, want ExpiredError(capacity)", err)
	}
	if _, err := m.Get("c"); err != nil {
		t.Errorf("newest session 'c' missing: %v", err)
	}
	if m.Len() != 2 {
		t.Errorf("len = %d, want 2", m.Len())
	}
}

func TestList(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	if _, err := m.GetOrCreate("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetOrCreate("b"); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range m.List() {
		got[s.ID] = true
	}
	if len(got) != 2 || !got["a"] || !got["b"] {
		t.Errorf("List = %v, want a and b", got)
	}
}

func TestLiveClosedOnNavigation(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s, _ := m.GetOrCreate("live")
	s.Visit(makePage("https://e.com/1"))

	fl := &fakeLive{}
	s.SetLive(fl)
	if s.Live() != fl {
		t.Fatal("Live() should return the attached context")
	}
	s.Visit(makePage("https://e.com/2")) // navigation drops the prior runtime
	if !fl.closed {
		t.Error("navigation should close the live runtime")
	}
	if s.Live() != nil {
		t.Error("Live() should be nil after navigation")
	}
}

func TestLiveClosedOnSessionClose(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s, _ := m.GetOrCreate("live")
	s.Visit(makePage("https://e.com/1"))
	fl := &fakeLive{}
	s.SetLive(fl)
	s.Close()
	if !fl.closed {
		t.Error("Session.Close should close the live runtime")
	}
}

func TestLiveClosedOnEviction(t *testing.T) {
	m := session.NewManager(time.Hour, 1, newClient, func(s *session.Session) { s.Close() })
	a, _ := m.GetOrCreate("a")
	a.Visit(makePage("https://e.com/a"))
	fl := &fakeLive{}
	a.SetLive(fl)

	// Creating a second session over the cap evicts "a", which must close its runtime.
	if _, err := m.GetOrCreate("b"); err != nil {
		t.Fatal(err)
	}
	if !fl.closed {
		t.Error("cap eviction should close the evicted session's live runtime")
	}
}

func TestHistoryNavigation(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s, _ := m.GetOrCreate("nav")

	s.Visit(makePage("https://e.com/1"))
	s.Visit(makePage("https://e.com/2"))
	s.Visit(makePage("https://e.com/3"))

	if cur := s.Current(); cur.FinalURL.Path != "/3" {
		t.Fatalf("current = %v", cur.FinalURL)
	}

	if p, ok := s.Back(); !ok || p.FinalURL.Path != "/2" {
		t.Fatalf("back = %v %v", p, ok)
	}
	if p, ok := s.Back(); !ok || p.FinalURL.Path != "/1" {
		t.Fatalf("back = %v %v", p, ok)
	}
	if _, ok := s.Back(); ok {
		t.Error("back past start should fail")
	}
	if p, ok := s.Forward(); !ok || p.FinalURL.Path != "/2" {
		t.Fatalf("forward = %v %v", p, ok)
	}

	// Visiting now truncates the forward history (drops /3).
	s.Visit(makePage("https://e.com/4"))
	urls, pos := s.HistoryURLs()
	if len(urls) != 3 || pos != 2 || urls[2] != "https://e.com/4" {
		t.Fatalf("history = %v pos=%d", urls, pos)
	}
}

func TestHistoryCapped(t *testing.T) {
	m := session.NewManager(0, 0, newClient, nil)
	s, _ := m.GetOrCreate("long")
	for i := 0; i < 75; i++ {
		s.Visit(makePage(fmt.Sprintf("https://e.com/%d", i)))
	}
	urls, pos := s.HistoryURLs()
	if len(urls) > 50 {
		t.Errorf("history len = %d, want <= 50 (each entry retains a full DOM)", len(urls))
	}
	if pos != len(urls)-1 || urls[pos] != "https://e.com/74" {
		t.Errorf("current = %q at %d, want the latest visit last", urls[pos], pos)
	}
}
