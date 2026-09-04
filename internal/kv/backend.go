package kv

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// KVBackend is the contract every KV store implements — the bolt default,
// redis, http, and any build-tagged extension (firestore/dynamodb slot in
// here). Semantics are uniform across backends:
//
//   - Namespaces isolate keyspaces; "" means DefaultNamespace.
//   - Values are JSON-shaped (string/float64/bool/map[string]any/[]any/nil).
//   - Absent and expired keys read as found=false.
//   - Every read-modify-write op — Incr, SetNX, Merge, Append, Remove,
//     Pop — is ONE backend transaction (bolt Update / redis Lua script /
//     the http shim's server-side op), so concurrent steps stay correct.
//   - Type mismatches (Merge on a non-object, list ops on a non-list)
//     error naming the key and the actual type.
//
// A backend that cannot serve one of these ops must say so via
// Capabilities — the load-time capability check turns that into a clear
// config error instead of a runtime surprise.
type KVBackend interface {
	Get(namespace, key string) (value any, found bool, err error)
	Set(namespace, key string, value any, ttl time.Duration) error
	SetNX(namespace, key string, value any, ttl time.Duration) (value2 any, created bool, err error)
	Merge(namespace, key string, patch map[string]any) (map[string]any, error)
	Delete(namespace, key string) error
	Incr(namespace, key string, by int64) (int64, error)
	Append(namespace, key string, items []any, unique bool) ([]any, error)
	Remove(namespace, key string, items []any) ([]any, error)
	Contains(namespace, key string, item any) (bool, error)
	First(namespace, key string) (any, bool, error)
	Last(namespace, key string) (any, bool, error)
	Index(namespace, key string, idx int) (any, bool, error)
	Slice(namespace, key string, start, end int, endSet bool) ([]any, error)
	Len(namespace, key string) (int, error)
	Pop(namespace, key string, front bool) (value any, found bool, length int, err error)
	List(namespace, prefix string) (keys []string, entries map[string]any, err error)
	Close() error
}

// Ops is the full operation set, in the order the verbs publish them. A
// backend's Capabilities response is checked against the ops a config
// actually uses.
var Ops = []string{
	"get", "set", "setnx", "merge", "delete", "incr",
	"append", "remove", "contains", "first", "last",
	"index", "slice", "len", "pop", "list",
}

// Capabilities reports which ops a backend serves. Backends implementing
// everything (bolt, redis, http) need not implement it — full coverage is
// assumed; a partial backend implements this to be capability-checked at
// load.
type Capabilities interface {
	Capabilities() map[string]bool
}

// Supports reports whether backend b can serve op.
func Supports(b KVBackend, op string) bool {
	c, ok := b.(Capabilities)
	if !ok {
		return true
	}
	return c.Capabilities()[op]
}

// CheckCapability returns a clear error when the named store can't serve op.
func CheckCapability(storeName string, b KVBackend, op string) error {
	if !Supports(b, op) {
		return fmt.Errorf("kv: store %q does not support %s", storeName, op)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The store registry. Every store is explicit: a `stores:` entry names it,
// gives it a type (boltdb/redis/http), and only then can a `store:` selector
// reach it. There is NO implicit or default store — no stores: section means
// no stores. boltdb entries get one file each: <data dir>/<name>.db, or
// wherever path: points.
// ---------------------------------------------------------------------------

var (
	regMu   sync.Mutex
	dataDir string // where boltdb stores without an explicit path: live
	named   = map[string]KVBackend{}
)

// SetDataDir points path-less boltdb stores at dir (the daemon's data dir;
// tests use a temp dir).
func SetDataDir(dir string) {
	regMu.Lock()
	defer regMu.Unlock()
	dataDir = dir
}

// BoltPath resolves a boltdb store's file: the explicit path, else
// <data dir>/<store-name>.db.
func BoltPath(storeName, path string) (string, error) {
	if path != "" {
		return path, nil
	}
	regMu.Lock()
	dir := dataDir
	regMu.Unlock()
	if dir == "" {
		return "", fmt.Errorf("kv: store %q: data dir not configured", storeName)
	}
	return filepath.Join(dir, storeName+".db"), nil
}

// OpenBoltStore builds the boltdb backend for a named store.
func OpenBoltStore(storeName, path string) (KVBackend, error) {
	p, err := BoltPath(storeName, path)
	if err != nil {
		return nil, err
	}
	b, err := Open(p)
	if err != nil {
		return nil, fmt.Errorf("kv: store %q: %w", storeName, err)
	}
	return b, nil
}

// Register adds a named backend (a `stores:` entry). Duplicates error.
func Register(name string, b KVBackend) error {
	regMu.Lock()
	defer regMu.Unlock()
	if name == "" {
		return fmt.Errorf("kv: stores: empty store name")
	}
	if _, dup := named[name]; dup {
		return fmt.Errorf("kv: store %q registered twice", name)
	}
	named[name] = b
	return nil
}

// ResetStores closes and clears every registered backend (config reload,
// tests). The data dir stays configured.
func ResetStores() {
	regMu.Lock()
	defer regMu.Unlock()
	for _, b := range named {
		_ = b.Close()
	}
	named = map[string]KVBackend{}
}

// Names returns the registered store names, sorted.
func Names() []string {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]string, 0, len(named))
	for n := range named {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Use resolves a store selector to its backend. Every kv operation names a
// store explicitly; there is no default.
func Use(name string) (KVBackend, error) {
	if name == "" {
		return nil, fmt.Errorf("kv: store: is required (defined stores: %s)", nameList())
	}
	regMu.Lock()
	b, ok := named[name]
	regMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("kv: no store named %q (defined stores: %s)", name, nameList())
	}
	return b, nil
}

func nameList() string {
	names := Names()
	if len(names) == 0 {
		return "none — add a stores: section"
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
