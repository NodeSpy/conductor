// Package kv is conductor's built-in durable key/value store: a single
// bbolt file (pure Go, ACID, fsync on commit) living beside conductor's
// other data. It backs the always-available `kv` connector, the `{{ kv … }}`
// template function, and the ctx.kv binding in in-process code steps —
// state written through any of them survives restarts (including the
// daemon's own auto-update restart) and is shared across runs.
package kv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// DefaultNamespace is the bucket used when a call names none.
const DefaultNamespace = "default"

// sweepInterval is how often the background sweep deletes expired entries.
// Expired entries are already invisible to reads; the sweep only reclaims
// space, so the cadence is relaxed.
const sweepInterval = time.Minute

// entry is the stored envelope: the value's JSON and an optional expiry.
type entry struct {
	V json.RawMessage `json:"v"`
	E int64           `json:"e,omitempty"` // unix nanoseconds; 0 = no expiry
}

func (e entry) expired(now time.Time) bool {
	return e.E != 0 && now.UnixNano() >= e.E
}

// Store is one open kv database — the boltdb KVBackend.
type Store struct {
	db      *bolt.DB
	stop    chan struct{}
	wg      sync.WaitGroup
	closeMu sync.Once
}

// Open opens (creating if absent) the store at path and starts the TTL
// sweep. The parent directory is created; bolt.Open fsyncs commits, so
// anything written here survives a crash.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("kv: create data dir: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("kv: open %s: %w", path, err)
	}
	s := &Store{db: db, stop: make(chan struct{})}
	s.wg.Add(1)
	go s.sweepLoop()
	return s, nil
}

// Close stops the sweep and closes the database. Idempotent — the registry
// and a test cleanup may both close the same store.
func (s *Store) Close() error {
	var err error
	s.closeMu.Do(func() {
		close(s.stop)
		s.wg.Wait()
		err = s.db.Close()
	})
	return err
}

func ns(namespace string) []byte {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	return []byte(namespace)
}

// Set stores value (any JSON-serializable Go value) under namespace/key,
// with an optional ttl (0 = no expiry). The bucket is created on demand.
func (s *Store) Set(namespace, key string, value any, ttl time.Duration) error {
	if key == "" {
		return fmt.Errorf("kv: set: key is required")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("kv: set %s/%s: value is not JSON-serializable: %w", namespace, key, err)
	}
	e := entry{V: raw}
	if ttl > 0 {
		e.E = time.Now().Add(ttl).UnixNano()
	}
	buf, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(ns(namespace))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), buf)
	})
}

// Get reads namespace/key. An absent or expired key returns found=false.
func (s *Store) Get(namespace, key string) (value any, found bool, err error) {
	if key == "" {
		return nil, false, fmt.Errorf("kv: get: key is required")
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(ns(namespace))
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(key))
		if raw == nil {
			return nil
		}
		var e entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("kv: get %s/%s: corrupt entry: %w", namespace, key, err)
		}
		if e.expired(time.Now()) {
			return nil
		}
		var v any
		if err := json.Unmarshal(e.V, &v); err != nil {
			return fmt.Errorf("kv: get %s/%s: corrupt value: %w", namespace, key, err)
		}
		value, found = v, true
		return nil
	})
	return value, found, err
}

// Delete removes namespace/key (a no-op when absent).
func (s *Store) Delete(namespace, key string) error {
	if key == "" {
		return fmt.Errorf("kv: delete: key is required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(ns(namespace))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}

// Incr atomically adds by to the number at namespace/key (absent or expired
// counts as 0) and returns the new value. The read-modify-write happens
// inside one bolt Update transaction, so concurrent increments never lose
// updates.
func (s *Store) Incr(namespace, key string, by int64) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("kv: incr: key is required")
	}
	var out int64
	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(ns(namespace))
		if err != nil {
			return err
		}
		var cur int64
		if raw := b.Get([]byte(key)); raw != nil {
			var e entry
			if err := json.Unmarshal(raw, &e); err != nil {
				return fmt.Errorf("kv: incr %s/%s: corrupt entry: %w", namespace, key, err)
			}
			if !e.expired(time.Now()) {
				// A JSON number decodes as float64; reject non-numbers.
				var v any
				if err := json.Unmarshal(e.V, &v); err != nil {
					return fmt.Errorf("kv: incr %s/%s: corrupt value: %w", namespace, key, err)
				}
				f, ok := v.(float64)
				if !ok {
					return fmt.Errorf("kv: incr %s/%s: current value is not a number (%T)", namespace, key, v)
				}
				cur = int64(f)
			}
		}
		out = cur + by
		raw, _ := json.Marshal(entry{V: json.RawMessage(fmt.Sprintf("%d", out))})
		return b.Put([]byte(key), raw)
	})
	return out, err
}

// typeName names a decoded JSON value's type for error messages.
func typeName(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "list"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

// jsonEq compares two JSON-shaped values by canonical encoding (Go's
// encoding/json sorts map keys, so object equality is order-independent).
func jsonEq(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

// liveEntry decodes the bucket's entry at key, treating expired as absent.
// The envelope comes back too so mutations can preserve the expiry.
func liveEntry(b *bolt.Bucket, namespace, key string, now time.Time) (v any, e entry, found bool, err error) {
	raw := b.Get([]byte(key))
	if raw == nil {
		return nil, entry{}, false, nil
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, entry{}, false, fmt.Errorf("kv: %s/%s: corrupt entry: %w", namespace, key, err)
	}
	if e.expired(now) {
		return nil, entry{}, false, nil
	}
	if err := json.Unmarshal(e.V, &v); err != nil {
		return nil, entry{}, false, fmt.Errorf("kv: %s/%s: corrupt value: %w", namespace, key, err)
	}
	return v, e, true, nil
}

// putValue writes value at key, keeping expiry (unix ns; 0 = none).
func putValue(b *bolt.Bucket, key string, value any, expiry int64) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("kv: %s: value is not JSON-serializable: %w", key, err)
	}
	buf, err := json.Marshal(entry{V: raw, E: expiry})
	if err != nil {
		return err
	}
	return b.Put([]byte(key), buf)
}

// SetNX writes value only when the key is absent (or expired). It returns
// the key's resulting value and whether this call created it — an existing
// key comes back unchanged with created=false. One Update transaction.
func (s *Store) SetNX(namespace, key string, value any, ttl time.Duration) (out any, created bool, err error) {
	if key == "" {
		return nil, false, fmt.Errorf("kv: setnx: key is required")
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(ns(namespace))
		if err != nil {
			return err
		}
		cur, _, found, err := liveEntry(b, namespace, key, time.Now())
		if err != nil {
			return err
		}
		if found {
			out, created = cur, false
			return nil
		}
		var exp int64
		if ttl > 0 {
			exp = time.Now().Add(ttl).UnixNano()
		}
		if err := putValue(b, key, value, exp); err != nil {
			return err
		}
		out, created = value, true
		return nil
	})
	return out, created, err
}

// Merge shallow-merges patch into the object at key (creating it when
// absent) and returns the merged object. A non-object existing value is an
// error naming the key and its actual type. One Update transaction.
func (s *Store) Merge(namespace, key string, patch map[string]any) (map[string]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: merge: key is required")
	}
	var out map[string]any
	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(ns(namespace))
		if err != nil {
			return err
		}
		cur, e, found, err := liveEntry(b, namespace, key, time.Now())
		if err != nil {
			return err
		}
		merged := map[string]any{}
		if found {
			obj, ok := cur.(map[string]any)
			if !ok {
				return fmt.Errorf("kv: merge %s/%s: existing value is %s, not an object", namespace, key, typeName(cur))
			}
			for k, v := range obj {
				merged[k] = v
			}
		}
		for k, v := range patch {
			merged[k] = v
		}
		if err := putValue(b, key, merged, e.E); err != nil {
			return err
		}
		out = merged
		return nil
	})
	return out, err
}

// listAt reads the list value at key inside a txn ([] when absent). A
// non-list value is an error naming the key and its actual type.
func listAt(b *bolt.Bucket, namespace, key string, now time.Time) ([]any, entry, error) {
	cur, e, found, err := liveEntry(b, namespace, key, now)
	if err != nil {
		return nil, entry{}, err
	}
	if !found {
		return []any{}, e, nil
	}
	lst, ok := cur.([]any)
	if !ok {
		return nil, entry{}, fmt.Errorf("kv: %s/%s: existing value is %s, not a list", namespace, key, typeName(cur))
	}
	return lst, e, nil
}

// Append appends items to the list at key (created as [] when absent) and
// returns the resulting list. unique skips items already present (set
// semantics, JSON equality). One Update transaction.
func (s *Store) Append(namespace, key string, items []any, unique bool) ([]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: append: key is required")
	}
	var out []any
	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(ns(namespace))
		if err != nil {
			return err
		}
		lst, e, err := listAt(b, namespace, key, time.Now())
		if err != nil {
			return fmt.Errorf("%w (append)", err)
		}
		for _, it := range items {
			if unique {
				dup := false
				for _, have := range lst {
					if jsonEq(have, it) {
						dup = true
						break
					}
				}
				if dup {
					continue
				}
			}
			lst = append(lst, it)
		}
		if err := putValue(b, key, lst, e.E); err != nil {
			return err
		}
		out = lst
		return nil
	})
	return out, err
}

// Remove deletes all occurrences of each item (JSON equality) from the list
// at key and returns the result. An absent key is a no-op ([]). One Update
// transaction.
func (s *Store) Remove(namespace, key string, items []any) ([]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: remove: key is required")
	}
	var out []any
	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(ns(namespace))
		if err != nil {
			return err
		}
		lst, e, err := listAt(b, namespace, key, time.Now())
		if err != nil {
			return fmt.Errorf("%w (remove)", err)
		}
		kept := make([]any, 0, len(lst))
		for _, have := range lst {
			drop := false
			for _, it := range items {
				if jsonEq(have, it) {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, have)
			}
		}
		if err := putValue(b, key, kept, e.E); err != nil {
			return err
		}
		out = kept
		return nil
	})
	return out, err
}

// Contains reports whether the list at key contains item (JSON equality).
// An absent key is false; a non-list value is an error.
func (s *Store) Contains(namespace, key string, item any) (bool, error) {
	if key == "" {
		return false, fmt.Errorf("kv: contains: key is required")
	}
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(ns(namespace))
		if b == nil {
			return nil
		}
		lst, _, err := listAt(b, namespace, key, time.Now())
		if err != nil {
			return fmt.Errorf("%w (contains)", err)
		}
		for _, have := range lst {
			if jsonEq(have, item) {
				found = true
				return nil
			}
		}
		return nil
	})
	return found, err
}

// readList reads the list at key outside a mutation ([] when absent; error
// when the value is not a list).
func (s *Store) readList(namespace, key, op string) ([]any, error) {
	if key == "" {
		return nil, fmt.Errorf("kv: %s: key is required", op)
	}
	var lst []any
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(ns(namespace))
		if b == nil {
			lst = []any{}
			return nil
		}
		l, _, err := listAt(b, namespace, key, time.Now())
		if err != nil {
			return fmt.Errorf("%w (%s)", err, op)
		}
		lst = l
		return nil
	})
	return lst, err
}

// resolveIndex maps a possibly-negative index onto a list of length n;
// ok=false when it falls out of range.
func resolveIndex(idx, n int) (int, bool) {
	if idx < 0 {
		idx += n
	}
	return idx, idx >= 0 && idx < n
}

// Index returns the element at idx of the list at key; negative idx counts
// from the end (-1 = last). Out of range (or absent/empty) is found=false.
func (s *Store) Index(namespace, key string, idx int) (any, bool, error) {
	lst, err := s.readList(namespace, key, "index")
	if err != nil {
		return nil, false, err
	}
	i, ok := resolveIndex(idx, len(lst))
	if !ok {
		return nil, false, nil
	}
	return lst[i], true, nil
}

// First returns the first element of the list at key (found=false when
// empty or absent).
func (s *Store) First(namespace, key string) (any, bool, error) {
	return s.Index(namespace, key, 0)
}

// Last returns the last element of the list at key.
func (s *Store) Last(namespace, key string) (any, bool, error) {
	return s.Index(namespace, key, -1)
}

// Slice returns lst[start:end] of the list at key, Python-style: end is
// exclusive, negatives count from the end, and out-of-range bounds clamp
// (never error). endSet=false means "to the end".
func (s *Store) Slice(namespace, key string, start, end int, endSet bool) ([]any, error) {
	lst, err := s.readList(namespace, key, "slice")
	if err != nil {
		return nil, err
	}
	n := len(lst)
	clamp := func(i int) int {
		if i < 0 {
			i += n
		}
		if i < 0 {
			return 0
		}
		if i > n {
			return n
		}
		return i
	}
	lo := clamp(start)
	hi := n
	if endSet {
		hi = clamp(end)
	}
	if lo >= hi {
		return []any{}, nil
	}
	return lst[lo:hi], nil
}

// Len returns the length of the list at key (0 when absent).
func (s *Store) Len(namespace, key string) (int, error) {
	lst, err := s.readList(namespace, key, "len")
	if err != nil {
		return 0, err
	}
	return len(lst), nil
}

// Pop removes and returns one element from an end of the list at key
// (front=false pops the back). Empty or absent is found=false with no
// mutation and no error. One Update transaction, so concurrent pops each
// take a distinct element.
func (s *Store) Pop(namespace, key string, front bool) (value any, found bool, length int, err error) {
	if key == "" {
		return nil, false, 0, fmt.Errorf("kv: pop: key is required")
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(ns(namespace))
		if b == nil {
			return nil
		}
		lst, e, err := listAt(b, namespace, key, time.Now())
		if err != nil {
			return fmt.Errorf("%w (pop)", err)
		}
		if len(lst) == 0 {
			return nil
		}
		if front {
			value, lst = lst[0], lst[1:]
		} else {
			value, lst = lst[len(lst)-1], lst[:len(lst)-1]
		}
		found, length = true, len(lst)
		return putValue(b, key, lst, e.E)
	})
	return value, found, length, err
}

// List returns the live (non-expired) keys in namespace with the given
// prefix ("" = all), sorted, plus their decoded values.
func (s *Store) List(namespace, prefix string) (keys []string, entries map[string]any, err error) {
	entries = map[string]any{}
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(ns(namespace))
		if b == nil {
			return nil
		}
		now := time.Now()
		c := b.Cursor()
		p := []byte(prefix)
		for k, raw := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, raw = c.Next() {
			var e entry
			if err := json.Unmarshal(raw, &e); err != nil || e.expired(now) {
				continue
			}
			var v any
			if err := json.Unmarshal(e.V, &v); err != nil {
				continue
			}
			keys = append(keys, string(k))
			entries[string(k)] = v
		}
		return nil
	})
	sort.Strings(keys)
	return keys, entries, err
}

// sweepLoop periodically deletes expired entries (reads already skip them;
// this reclaims the space).
func (s *Store) sweepLoop() {
	defer s.wg.Done()
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			_ = s.Sweep(time.Now())
		}
	}
}

// Sweep deletes every entry expired as of now, across all namespaces.
func (s *Store) Sweep(now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			var dead [][]byte
			_ = b.ForEach(func(k, raw []byte) error {
				var e entry
				if err := json.Unmarshal(raw, &e); err == nil && e.expired(now) {
					dead = append(dead, append([]byte(nil), k...))
				}
				return nil
			})
			for _, k := range dead {
				if err := b.Delete(k); err != nil {
					return err
				}
			}
			return nil
		})
	})
}
