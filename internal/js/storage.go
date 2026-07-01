package js

import (
	"sort"
	"sync"

	"github.com/dop251/goja"
)

// Storage is a minimal string key-value store backing window.localStorage, so
// values survive beyond a single render. The browser hands a session-scoped
// implementation via Env.Storage; without one, localStorage is a fresh
// per-render in-memory map (the prelude fallback).
type Storage interface {
	Get(key string) (string, bool)
	Set(key, value string)
	Remove(key string)
	Clear()
	Keys() []string
}

// MemStorage is a mutex-guarded in-memory Storage. Safe for concurrent use —
// a session's one-shot renders and live runtime may touch it from different
// goroutines over the session's life.
type MemStorage struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemStorage returns an empty MemStorage.
func NewMemStorage() *MemStorage { return &MemStorage{m: make(map[string]string)} }

func (s *MemStorage) Get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok
}

func (s *MemStorage) Set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Bound a hostile page writing unbounded keys; real browsers cap ~5MB/origin.
	if _, exists := s.m[key]; !exists && len(s.m) >= maxStorageKeys {
		return
	}
	if len(value) > maxStorageValueLen {
		value = value[:maxStorageValueLen]
	}
	s.m[key] = value
}

func (s *MemStorage) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

func (s *MemStorage) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = make(map[string]string)
}

func (s *MemStorage) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	return keys
}

// Bounds for a persistent storage area (a hostile page must not grow a
// session's memory without limit).
const (
	maxStorageKeys     = 1024
	maxStorageValueLen = 64 << 10
)

// installStorage backs window.localStorage with the supplied session-scoped
// store so values persist across renders and interacts (SPA auth/state flows
// depend on this). A no-op when Env.Storage was nil — the prelude's per-render
// in-memory fallback then applies (it checks `if (!window.localStorage)`).
func (b *bridge) installStorage() {
	if b.storage == nil {
		return
	}
	vm := b.vm
	o := vm.NewObject()
	_ = o.Set("getItem", func(call goja.FunctionCall) goja.Value {
		if v, ok := b.storage.Get(call.Argument(0).String()); ok {
			return vm.ToValue(v)
		}
		return goja.Null()
	})
	_ = o.Set("setItem", func(call goja.FunctionCall) goja.Value {
		b.storage.Set(call.Argument(0).String(), call.Argument(1).String())
		return goja.Undefined()
	})
	_ = o.Set("removeItem", func(call goja.FunctionCall) goja.Value {
		b.storage.Remove(call.Argument(0).String())
		return goja.Undefined()
	})
	_ = o.Set("clear", func(goja.FunctionCall) goja.Value {
		b.storage.Clear()
		return goja.Undefined()
	})
	_ = o.Set("key", func(call goja.FunctionCall) goja.Value {
		keys := b.storage.Keys()
		sort.Strings(keys) // map order is random; key(i) must be stable within a call
		if i := int(call.Argument(0).ToInteger()); i >= 0 && i < len(keys) {
			return vm.ToValue(keys[i])
		}
		return goja.Null()
	})
	_ = o.DefineAccessorProperty("length",
		vm.ToValue(func(goja.FunctionCall) goja.Value { return vm.ToValue(len(b.storage.Keys())) }),
		nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	// window IS the global object here, so one Set covers window.localStorage,
	// self.localStorage, and the bare localStorage global.
	_ = vm.Set("localStorage", o)
}
