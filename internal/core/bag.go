package core

import "sync"

// Bag is per-pipeline, concurrency-safe scratch state threaded through every
// stage alongside context.Context. Use it for values that do not fit the fixed
// schema — the namespace Provision created, the Job name Run produced, a token
// Teardown must revoke. It is deliberately NOT context.Value, which is an
// anti-pattern for mutable shared state. See decisions/0003.
//
// Convention: document the keys each stage reads/writes in the tool package so
// the Bag does not become an undocumented dumping ground.
type Bag struct {
	mu sync.RWMutex
	m  map[string]any
}

// NewBag returns an empty Bag ready for use.
func NewBag() *Bag {
	return &Bag{m: make(map[string]any)}
}

// Set stores a value under key.
func (b *Bag) Set(key string, v any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = make(map[string]any)
	}
	b.m[key] = v
}

// Get returns the value stored under key, if present.
func (b *Bag) Get(key string) (any, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.m[key]
	return v, ok
}

// GetAs returns the value under key typed as T. ok is false if the key is
// missing or the stored value is not a T.
func GetAs[T any](b *Bag, key string) (T, bool) {
	var zero T
	v, ok := b.Get(key)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		return zero, false
	}
	return t, true
}
