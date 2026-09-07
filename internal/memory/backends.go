package memory

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/NodeSpy/conductor/internal/kv"
)

// ---------------------------------------------------------------------------
// store backend — entries live in a `stores:` KV entry (boltdb file, or
// redis/http for fleet-shared memory), one key per entry under the "memory"
// namespace. The store is resolved by name at each call so the backend
// follows the registry across a config reload.
// ---------------------------------------------------------------------------

const storeNamespace = "memory"

type storeBackend struct{ store string }

// NewStoreBackend backs memory with the named stores: entry. The name is
// verified once here (a load-time error beats a first-write surprise).
func NewStoreBackend(store string) (Backend, error) {
	if _, err := kv.Use(store); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	return storeBackend{store: store}, nil
}

func (b storeBackend) Put(e Entry) error {
	st, err := kv.Use(b.store)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return st.Set(storeNamespace, e.ID, v, 0)
}

func (b storeBackend) Delete(id string) (bool, error) {
	st, err := kv.Use(b.store)
	if err != nil {
		return false, err
	}
	_, found, err := st.Get(storeNamespace, id)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return true, st.Delete(storeNamespace, id)
}

func (b storeBackend) List() ([]Entry, error) {
	st, err := kv.Use(b.store)
	if err != nil {
		return nil, err
	}
	_, entries, err := st.List(storeNamespace, "")
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for id, v := range entries {
		raw, err := json.Marshal(v)
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			continue // a foreign value in the namespace is not a memory
		}
		if e.ID == "" {
			e.ID = id
		}
		if e.Scope == "" {
			e.Scope = "global"
		}
		out = append(out, e)
	}
	return out, nil
}

func (b storeBackend) Close() error { return nil } // the store registry owns the backend

// ---------------------------------------------------------------------------
// ephemeral backend — a guarded in-process map: memory without configuring
// any storage, gone on restart.
// ---------------------------------------------------------------------------

type memBackend struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// NewMemBackend builds the in-process ephemeral backend (`type: memory`).
func NewMemBackend() Backend {
	return &memBackend{entries: map[string]Entry{}}
}

func (b *memBackend) Put(e Entry) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[e.ID] = e
	return nil
}

func (b *memBackend) Delete(id string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.entries[id]
	delete(b.entries, id)
	return ok, nil
}

func (b *memBackend) List() ([]Entry, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Entry, 0, len(b.entries))
	for _, e := range b.entries {
		out = append(out, e)
	}
	return out, nil
}

func (b *memBackend) Close() error { return nil }
