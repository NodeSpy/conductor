package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// host.state quotas (plugin-contract.md §1.9, decision Q9): per instance, so
// one plugin cannot crowd another out, and small, because this is a source's
// memory (review claims, own-status contexts, poll cursors), not a database.
const (
	stateMaxKeys      = 4096
	stateMaxKeyBytes  = 512
	stateMaxValBytes  = 64 << 10
	stateMaxInstBytes = 4 << 20
)

// StateStore is the durable store behind host.state: one bucket per
// (plugin, instance). Entries survive plugin and daemon restarts and go away
// with the instance (Drop).
type StateStore struct {
	dir string
	mu  sync.Mutex
	db  *bolt.DB
	now func() time.Time
}

// NewStateStore is the store at dir/plugin-state.db, opened on first use —
// so a CLI command that builds plugin deps never takes the file lock a
// running daemon holds.
func NewStateStore(dir string) *StateStore { return &StateStore{dir: dir, now: time.Now} }

// OpenStateStore opens (creating) the store now.
func OpenStateStore(dir string) (*StateStore, error) {
	s := NewStateStore(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s, s.openLocked()
}

func (s *StateStore) openLocked() error {
	if s.db != nil {
		return nil
	}
	db, err := bolt.Open(filepath.Join(s.dir, "plugin-state.db"), 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("plugin state: %w", err)
	}
	s.db = db
	return nil
}

// Close closes the store if it was opened.
func (s *StateStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

type stateEntry struct {
	Value   json.RawMessage `json:"v"`
	Expires time.Time       `json:"e,omitempty"`
}

func bucketName(plugin, instance string) []byte { return []byte(plugin + "\x00" + instance) }

// Do answers one host.state request for plugin. The caller has already
// checked that the plugin serves req.Instance.
func (s *StateStore) Do(plugin string, req sdk.HostStateRequest) sdk.HostStateResult {
	if s == nil {
		return sdk.HostStateResult{Error: "this daemon has no plugin state store"}
	}
	fail := func(f string, a ...any) sdk.HostStateResult { return sdk.HostStateResult{Error: fmt.Sprintf(f, a...)} }
	if req.Op != "list" && (req.Key == "" || len(req.Key) > stateMaxKeyBytes) {
		return fail("key must be 1..%d bytes", stateMaxKeyBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return fail("%v", err)
	}
	b := bucketName(plugin, req.Instance)
	now := s.now()
	switch req.Op {
	case "get":
		var out sdk.HostStateResult
		err := s.db.View(func(tx *bolt.Tx) error {
			bk := tx.Bucket(b)
			if bk == nil {
				out = sdk.HostStateResult{OK: true}
				return nil
			}
			e, ok := decodeEntry(bk.Get([]byte(req.Key)))
			if !ok || e.expired(now) {
				out = sdk.HostStateResult{OK: true}
				return nil
			}
			var v any
			_ = json.Unmarshal(e.Value, &v)
			out = sdk.HostStateResult{OK: true, Value: v}
			return nil
		})
		if err != nil {
			return fail("%v", err)
		}
		return out
	case "put":
		raw, err := json.Marshal(req.Value)
		if err != nil {
			return fail("value: %v", err)
		}
		if len(raw) > stateMaxValBytes {
			return fail("value is %d bytes; the limit is %d", len(raw), stateMaxValBytes)
		}
		e := stateEntry{Value: raw}
		if req.TTL != "" {
			d, err := time.ParseDuration(req.TTL)
			if err != nil || d <= 0 {
				return fail("ttl %q: must be a positive Go duration", req.TTL)
			}
			e.Expires = now.Add(d)
		}
		enc, _ := json.Marshal(e)
		err = s.db.Update(func(tx *bolt.Tx) error {
			bk, err := tx.CreateBucketIfNotExists(b)
			if err != nil {
				return err
			}
			s.sweepExpired(bk, now)
			keys, size := 0, 0
			_ = bk.ForEach(func(k, v []byte) error {
				if string(k) != req.Key {
					keys++
					size += len(k) + len(v)
				}
				return nil
			})
			if keys+1 > stateMaxKeys {
				return fmt.Errorf("instance %q has %d keys; the limit is %d", req.Instance, keys, stateMaxKeys)
			}
			if size+len(req.Key)+len(enc) > stateMaxInstBytes {
				return fmt.Errorf("instance %q would hold more than %d bytes", req.Instance, stateMaxInstBytes)
			}
			return bk.Put([]byte(req.Key), enc)
		})
		if err != nil {
			return fail("%v", err)
		}
		return sdk.HostStateResult{OK: true}
	case "delete":
		err := s.db.Update(func(tx *bolt.Tx) error {
			if bk := tx.Bucket(b); bk != nil {
				return bk.Delete([]byte(req.Key))
			}
			return nil
		})
		if err != nil {
			return fail("%v", err)
		}
		return sdk.HostStateResult{OK: true}
	case "list":
		var keys []any
		err := s.db.View(func(tx *bolt.Tx) error {
			bk := tx.Bucket(b)
			if bk == nil {
				return nil
			}
			var ks []string
			_ = bk.ForEach(func(k, v []byte) error {
				if e, ok := decodeEntry(v); ok && !e.expired(now) && strings.HasPrefix(string(k), req.Key) {
					ks = append(ks, string(k))
				}
				return nil
			})
			sort.Strings(ks)
			for _, k := range ks {
				keys = append(keys, k)
			}
			return nil
		})
		if err != nil {
			return fail("%v", err)
		}
		return sdk.HostStateResult{OK: true, Value: keys}
	}
	return fail("unknown op %q (get, put, delete, list)", req.Op)
}

// Drop deletes everything an instance stored — the instance was removed.
func (s *StateStore) Drop(plugin, instance string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketName(plugin, instance)) == nil {
			return nil
		}
		return tx.DeleteBucket(bucketName(plugin, instance))
	})
}

func (s *StateStore) sweepExpired(bk *bolt.Bucket, now time.Time) {
	var dead [][]byte
	_ = bk.ForEach(func(k, v []byte) error {
		if e, ok := decodeEntry(v); !ok || e.expired(now) {
			dead = append(dead, append([]byte(nil), k...))
		}
		return nil
	})
	for _, k := range dead {
		_ = bk.Delete(k)
	}
}

func decodeEntry(b []byte) (stateEntry, bool) {
	if b == nil {
		return stateEntry{}, false
	}
	var e stateEntry
	if json.Unmarshal(b, &e) != nil {
		return stateEntry{}, false
	}
	return e, true
}

func (e stateEntry) expired(now time.Time) bool { return !e.Expires.IsZero() && !now.Before(e.Expires) }
