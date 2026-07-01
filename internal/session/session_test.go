package session_test

import (
	"context"
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
func (f *fakeLive) Snapshot(context.Context) ([]byte, error)          { return nil, nil }
func (f *fakeLive) PendingNavigation(context.Context) (string, error) { return "", nil }
func (f *fakeLive) Close()                                            { f.closed = true }

func makePage(rawURL string) *page.Page {
	u, _ := url.Parse(rawURL)
	return &page.Page{FinalURL: u}
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
	if _, ok := m.Get("a"); ok {
		t.Error("session still present after close")
	}
}

func TestTTLExpiry(t *testing.T) {
	m := session.NewManager(time.Millisecond, 10, newClient, nil)
	s1, _ := m.GetOrCreate("a")
	time.Sleep(15 * time.Millisecond)
	s2, _ := m.GetOrCreate("a")
	if s1 == s2 {
		t.Error("expected a fresh session after TTL expiry")
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
	if _, ok := m.Get("a"); ok {
		t.Error("oldest session 'a' should have been evicted")
	}
	if _, ok := m.Get("c"); !ok {
		t.Error("newest session 'c' missing")
	}
	if m.Len() != 2 {
		t.Errorf("len = %d, want 2", m.Len())
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
