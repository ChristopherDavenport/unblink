package js

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// extStore is the backing for chrome.storage (local/session/sync/managed): a flat map
// keyed by "<extID>\x00<area>\x00<key>", engine-lifetime and shared across renders and
// the background. Values are plain Go (JSON-compatible) so they cross goja runtimes
// safely. local + sync are persisted to disk (so an extension's one-time setup — e.g.
// uBlock's compiled filter lists — survives process restarts); session + managed are
// memory-only. onChanged notifications fan out to registered listeners on their own
// event loops.
type extStore struct {
	mu     sync.Mutex
	m      map[string]any
	dir    string          // disk root; "" disables persistence
	loaded map[string]bool // "<extID>\x00<area>" areas already read from disk
	subs   []storeSub
}

// storeSub is one chrome.storage.onChanged subscriber. notify delivers a change set on
// the subscriber's loop and returns false when that loop is gone (so it can be pruned).
type storeSub struct {
	extID  string
	notify func(changes map[string]any, area string) bool
}

func newExtStore(dir string) *extStore {
	return &extStore{m: map[string]any{}, dir: dir, loaded: map[string]bool{}}
}

// extStorageDir returns the on-disk root for persisted extension storage, or "" if the
// user cache dir is unavailable. Tests isolate it via XDG_CACHE_HOME / os.UserCacheDir.
func extStorageDir() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "unblink", "ext")
}

func persistArea(area string) bool { return area == "local" || area == "sync" }

func areaKey(extID, area string) string { return extID + "\x00" + area }
func fullKey(extID, area, key string) string {
	return extID + "\x00" + area + "\x00" + key
}

func (s *extStore) get(extID, area string, keys []string, all bool) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded(extID, area)
	prefix := fullKey(extID, area, "")
	out := map[string]any{}
	if all {
		for k, v := range s.m {
			if rest, ok := strings.CutPrefix(k, prefix); ok {
				out[rest] = v
			}
		}
		return out
	}
	for _, k := range keys {
		if v, ok := s.m[prefix+k]; ok {
			out[k] = v
		}
	}
	return out
}

func (s *extStore) set(extID, area string, kv map[string]any) {
	s.mu.Lock()
	s.ensureLoaded(extID, area)
	changes := make(map[string]any, len(kv))
	for k, v := range kv {
		fk := fullKey(extID, area, k)
		changes[k] = changeEntry(s.m[fk], v)
		s.m[fk] = v
	}
	s.saveArea(extID, area)
	s.mu.Unlock()
	s.fire(extID, area, changes)
}

func (s *extStore) remove(extID, area string, keys []string) {
	s.mu.Lock()
	s.ensureLoaded(extID, area)
	changes := make(map[string]any, len(keys))
	for _, k := range keys {
		fk := fullKey(extID, area, k)
		if old, ok := s.m[fk]; ok {
			changes[k] = changeEntry(old, nil)
			delete(s.m, fk)
		}
	}
	s.saveArea(extID, area)
	s.mu.Unlock()
	s.fire(extID, area, changes)
}

func (s *extStore) clear(extID, area string) {
	s.mu.Lock()
	s.ensureLoaded(extID, area)
	prefix := fullKey(extID, area, "")
	changes := map[string]any{}
	for k, v := range s.m {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			changes[rest] = changeEntry(v, nil)
			delete(s.m, k)
		}
	}
	s.saveArea(extID, area)
	s.mu.Unlock()
	s.fire(extID, area, changes)
}

// subscribe registers an onChanged listener for an extension.
func (s *extStore) subscribe(extID string, notify func(changes map[string]any, area string) bool) {
	s.mu.Lock()
	s.subs = append(s.subs, storeSub{extID: extID, notify: notify})
	s.mu.Unlock()
}

// fire delivers a change set to matching subscribers (outside the store lock, so a
// listener can touch storage), pruning subscribers whose loop has been torn down.
func (s *extStore) fire(extID, area string, changes map[string]any) {
	if len(changes) == 0 {
		return
	}
	s.mu.Lock()
	subs := append([]storeSub(nil), s.subs...)
	s.mu.Unlock()

	live := make([]storeSub, 0, len(subs))
	pruned := false
	for _, sub := range subs {
		if sub.extID != extID {
			live = append(live, sub)
			continue
		}
		if sub.notify(changes, area) {
			live = append(live, sub)
		} else {
			pruned = true
		}
	}
	if pruned {
		s.mu.Lock()
		s.subs = live
		s.mu.Unlock()
	}
}

// ensureLoaded lazily reads a persisted area from disk on first access. Caller holds
// the lock.
func (s *extStore) ensureLoaded(extID, area string) {
	ak := areaKey(extID, area)
	if s.loaded[ak] {
		return
	}
	s.loaded[ak] = true
	if s.dir == "" || !persistArea(area) {
		return
	}
	data, err := os.ReadFile(s.areaPath(extID, area))
	if err != nil {
		return
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return
	}
	for k, v := range m {
		s.m[fullKey(extID, area, k)] = v
	}
}

// saveArea writes a persisted area's snapshot to disk. Caller holds the lock.
func (s *extStore) saveArea(extID, area string) {
	if s.dir == "" || !persistArea(area) {
		return
	}
	prefix := fullKey(extID, area, "")
	out := map[string]any{}
	for k, v := range s.m {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			out[rest] = v
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	path := s.areaPath(extID, area)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func (s *extStore) areaPath(extID, area string) string {
	return filepath.Join(s.dir, extID, area+".json")
}

// changeEntry builds a chrome.storage.onChanged change record. Absent old/new values
// are represented by an empty map (the property is simply not set).
func changeEntry(oldVal, newVal any) map[string]any {
	e := map[string]any{}
	if oldVal != nil {
		e["oldValue"] = oldVal
	}
	if newVal != nil {
		e["newValue"] = newVal
	}
	return e
}
