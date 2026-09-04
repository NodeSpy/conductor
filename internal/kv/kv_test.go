package kv

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRoundTripAndFound: set/get/delete with the found flag, and JSON value
// fidelity for object/array/number/bool/string.
func TestRoundTripAndFound(t *testing.T) {
	s := openTemp(t)
	values := map[string]any{
		"str":  "hello",
		"num":  float64(42.5),
		"bool": true,
		"obj":  map[string]any{"a": float64(1), "b": []any{"x", "y"}},
		"arr":  []any{float64(1), "two", false, map[string]any{"k": "v"}},
	}
	for k, v := range values {
		if err := s.Set("", k, v, 0); err != nil {
			t.Fatal(err)
		}
	}
	for k, want := range values {
		got, found, err := s.Get("", k)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", k, found, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %#v != %#v", k, got, want)
		}
	}
	if _, found, err := s.Get("", "missing"); found || err != nil {
		t.Fatalf("missing key: found=%v err=%v", found, err)
	}
	if err := s.Delete("", "str"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Get("", "str"); found {
		t.Fatal("deleted key still found")
	}
	if err := s.Delete("", "str"); err != nil { // absent delete is a no-op
		t.Fatal(err)
	}
}

// TestIncrAtomicity: many concurrent increments on one key never lose an
// update (the read-modify-write is one bolt Update txn).
func TestIncrAtomicity(t *testing.T) {
	s := openTemp(t)
	const workers, per = 16, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if _, err := s.Incr("counters", "hits", 1); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	v, err := s.Incr("counters", "hits", 0)
	if err != nil || v != workers*per {
		t.Fatalf("final count: %d err=%v (want %d)", v, err, workers*per)
	}
	// by defaults are the caller's concern; negative works too.
	if v, _ := s.Incr("counters", "hits", -400); v != 0 {
		t.Fatalf("decrement: %d", v)
	}
	// A non-numeric value refuses to increment.
	_ = s.Set("counters", "label", "abc", 0)
	if _, err := s.Incr("counters", "label", 1); err == nil {
		t.Fatal("incr on a string must error")
	}
}

// TestTTL: an expired key reads absent, is skipped by list, and the sweep
// physically deletes it.
func TestTTL(t *testing.T) {
	s := openTemp(t)
	if err := s.Set("", "ephemeral", "x", 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.Get("", "ephemeral"); !found {
		t.Fatal("fresh ttl key must be readable")
	}
	time.Sleep(30 * time.Millisecond)
	if _, found, _ := s.Get("", "ephemeral"); found {
		t.Fatal("expired key must read absent")
	}
	keys, _, err := s.List("", "")
	if err != nil || len(keys) != 0 {
		t.Fatalf("expired key in list: %v %v", keys, err)
	}
	// The sweep reclaims the raw entry.
	if err := s.Sweep(time.Now()); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	_ = s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket([]byte(DefaultNamespace)); b != nil {
			raw = b.Get([]byte("ephemeral"))
		}
		return nil
	})
	if raw != nil {
		t.Fatal("sweep left the expired entry on disk")
	}
}

// TestNamespaceIsolation: the same key in two namespaces is independent, and
// list/prefix scope correctly.
func TestNamespaceIsolation(t *testing.T) {
	s := openTemp(t)
	_ = s.Set("alpha", "key", "A", 0)
	_ = s.Set("beta", "key", "B", 0)
	_ = s.Set("alpha", "prefix-1", 1, 0)
	_ = s.Set("alpha", "prefix-2", 2, 0)
	if v, _, _ := s.Get("alpha", "key"); v != "A" {
		t.Fatalf("alpha: %v", v)
	}
	if v, _, _ := s.Get("beta", "key"); v != "B" {
		t.Fatalf("beta: %v", v)
	}
	_ = s.Delete("alpha", "key")
	if _, found, _ := s.Get("beta", "key"); !found {
		t.Fatal("delete leaked across namespaces")
	}
	keys, entries, err := s.List("alpha", "prefix-")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "prefix-1" || entries["prefix-2"] != float64(2) {
		t.Fatalf("prefix list: %v %v", keys, entries)
	}
	if keys, _, _ := s.List("nope", ""); len(keys) != 0 {
		t.Fatalf("unknown namespace: %v", keys)
	}
}

// TestPersistenceAcrossReopen: committed state survives closing and
// reopening the same file — the durability guarantee.
func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("billing", "last-invoice", map[string]any{"id": "inv-9"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Incr("billing", "runs", 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	v, found, err := s2.Get("billing", "last-invoice")
	if err != nil || !found {
		t.Fatalf("reopen: found=%v err=%v", found, err)
	}
	if v.(map[string]any)["id"] != "inv-9" {
		t.Fatalf("reopen value: %v", v)
	}
	if n, _ := s2.Incr("billing", "runs", 0); n != 3 {
		t.Fatalf("reopen counter: %d", n)
	}
}

// TestSetNX: created vs existing, and ttl only applies on create.
func TestSetNX(t *testing.T) {
	s := openTemp(t)
	v, created, err := s.SetNX("", "lock", "owner-1", 0)
	if err != nil || !created || v != "owner-1" {
		t.Fatalf("first setnx: %v %v %v", v, created, err)
	}
	v, created, err = s.SetNX("", "lock", "owner-2", 0)
	if err != nil || created || v != "owner-1" {
		t.Fatalf("second setnx must return the existing value: %v %v %v", v, created, err)
	}
	// An expired key counts as absent.
	_ = s.Set("", "gone", "x", 5*time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	if _, created, _ := s.SetNX("", "gone", "fresh", 0); !created {
		t.Fatal("setnx over an expired key must create")
	}
}

// TestMerge: create, merge-over (per-key override + keep), and the
// non-object type error naming key and type.
func TestMerge(t *testing.T) {
	s := openTemp(t)
	v, err := s.Merge("", "profile", map[string]any{"name": "amy", "age": float64(30)})
	if err != nil || v["name"] != "amy" {
		t.Fatalf("merge-create: %v %v", v, err)
	}
	v, err = s.Merge("", "profile", map[string]any{"age": float64(31), "city": "berlin"})
	if err != nil {
		t.Fatal(err)
	}
	if v["name"] != "amy" || v["age"] != float64(31) || v["city"] != "berlin" {
		t.Fatalf("merge-over: %v", v)
	}
	got, _, _ := s.Get("", "profile")
	if !reflect.DeepEqual(got, map[string]any{"name": "amy", "age": float64(31), "city": "berlin"}) {
		t.Fatalf("persisted merge: %v", got)
	}
	_ = s.Set("", "scalar", 7, 0)
	_, err = s.Merge("", "scalar", map[string]any{"a": 1})
	if err == nil || !contains(err.Error(), "scalar") || !contains(err.Error(), "number") {
		t.Fatalf("merge over a number must name key and type: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestAppendRemoveContains: single/array append, unique semantics, removal
// of all occurrences, absent no-ops, and the non-list type errors.
func TestAppendRemoveContains(t *testing.T) {
	s := openTemp(t)
	lst, err := s.Append("", "tags", []any{"a"}, false)
	if err != nil || len(lst) != 1 {
		t.Fatalf("append-create: %v %v", lst, err)
	}
	lst, _ = s.Append("", "tags", []any{"b", "a", "a"}, false)
	if len(lst) != 4 {
		t.Fatalf("append-many: %v", lst)
	}
	// unique skips already-present values (JSON equality, objects included).
	lst, _ = s.Append("", "tags", []any{"a", "c"}, true)
	if !reflect.DeepEqual(lst, []any{"a", "b", "a", "a", "c"}) {
		t.Fatalf("append-unique: %v", lst)
	}
	ok, err := s.Contains("", "tags", "b")
	if err != nil || !ok {
		t.Fatalf("contains b: %v %v", ok, err)
	}
	if ok, _ := s.Contains("", "tags", "zz"); ok {
		t.Fatal("contains zz")
	}
	if ok, _ := s.Contains("", "no-such-list", "x"); ok {
		t.Fatal("contains on absent key must be false")
	}
	// remove deletes ALL occurrences of each item.
	lst, err = s.Remove("", "tags", []any{"a"})
	if err != nil || !reflect.DeepEqual(lst, []any{"b", "c"}) {
		t.Fatalf("remove-all: %v %v", lst, err)
	}
	if lst, err := s.Remove("", "no-such-list", []any{"x"}); err != nil || len(lst) != 0 {
		t.Fatalf("remove on absent key is a no-op: %v %v", lst, err)
	}
	// Object membership uses canonical JSON equality.
	_, _ = s.Append("", "objs", []any{map[string]any{"a": float64(1), "b": float64(2)}}, false)
	if ok, _ := s.Contains("", "objs", map[string]any{"b": float64(2), "a": float64(1)}); !ok {
		t.Fatal("object equality must be key-order independent")
	}
	// Type errors name the key and actual type.
	_ = s.Set("", "notalist", "str", 0)
	for _, op := range []func() error{
		func() error { _, err := s.Append("", "notalist", []any{1}, false); return err },
		func() error { _, err := s.Remove("", "notalist", []any{1}); return err },
		func() error { _, err := s.Contains("", "notalist", 1); return err },
	} {
		if err := op(); err == nil || !contains(err.Error(), "notalist") || !contains(err.Error(), "string") {
			t.Fatalf("non-list op must name key and type: %v", err)
		}
	}
}

// TestListAccessors: first/last/index (negatives, out of range), slice
// (defaults, negatives, clamping, empty), len.
func TestListAccessors(t *testing.T) {
	s := openTemp(t)
	_, _ = s.Append("", "q", []any{"a", "b", "c", "d"}, false)

	if v, found, _ := s.First("", "q"); !found || v != "a" {
		t.Fatalf("first: %v %v", v, found)
	}
	if v, found, _ := s.Last("", "q"); !found || v != "d" {
		t.Fatalf("last: %v %v", v, found)
	}
	if _, found, _ := s.First("", "empty"); found {
		t.Fatal("first on absent must be found=false")
	}
	if v, found, _ := s.Index("", "q", 2); !found || v != "c" {
		t.Fatalf("index 2: %v %v", v, found)
	}
	if v, found, _ := s.Index("", "q", -1); !found || v != "d" {
		t.Fatalf("index -1: %v %v", v, found)
	}
	if v, found, _ := s.Index("", "q", -4); !found || v != "a" {
		t.Fatalf("index -4: %v %v", v, found)
	}
	for _, idx := range []int{4, -5, 99} {
		if _, found, _ := s.Index("", "q", idx); found {
			t.Fatalf("index %d must be out of range", idx)
		}
	}
	slice := func(start, end int, endSet bool) []any {
		v, err := s.Slice("", "q", start, end, endSet)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := slice(0, 0, false); !reflect.DeepEqual(got, []any{"a", "b", "c", "d"}) {
		t.Fatalf("full slice: %v", got)
	}
	if got := slice(1, 3, true); !reflect.DeepEqual(got, []any{"b", "c"}) {
		t.Fatalf("[1:3): %v", got)
	}
	if got := slice(-2, 0, false); !reflect.DeepEqual(got, []any{"c", "d"}) {
		t.Fatalf("[-2:]: %v", got)
	}
	if got := slice(0, -1, true); !reflect.DeepEqual(got, []any{"a", "b", "c"}) {
		t.Fatalf("[:-1): %v", got)
	}
	if got := slice(-99, 99, true); len(got) != 4 {
		t.Fatalf("clamped: %v", got)
	}
	if got := slice(3, 1, true); len(got) != 0 {
		t.Fatalf("inverted range must be empty: %v", got)
	}
	if n, _ := s.Len("", "q"); n != 4 {
		t.Fatalf("len: %d", n)
	}
	if n, _ := s.Len("", "absent"); n != 0 {
		t.Fatalf("len absent: %d", n)
	}
	if _, err := s.Len("", "notalist2"); err != nil {
		t.Fatal(err) // absent, so still fine
	}
	_ = s.Set("", "notalist2", true, 0)
	if _, err := s.Len("", "notalist2"); err == nil || !contains(err.Error(), "bool") {
		t.Fatalf("len on bool: %v", err)
	}
}

// TestPop: back/front pops return and shrink; empty and absent are quiet
// no-ops.
func TestPop(t *testing.T) {
	s := openTemp(t)
	_, _ = s.Append("", "stack", []any{"a", "b", "c"}, false)
	v, found, n, err := s.Pop("", "stack", false)
	if err != nil || !found || v != "c" || n != 2 {
		t.Fatalf("pop back: %v %v %d %v", v, found, n, err)
	}
	v, found, n, err = s.Pop("", "stack", true)
	if err != nil || !found || v != "a" || n != 1 {
		t.Fatalf("pop front: %v %v %d %v", v, found, n, err)
	}
	rest, _, _ := s.Get("", "stack")
	if !reflect.DeepEqual(rest, []any{"b"}) {
		t.Fatalf("remaining: %v", rest)
	}
	_, _, _, _ = s.Pop("", "stack", false)
	if _, found, n, err := s.Pop("", "stack", false); found || n != 0 || err != nil {
		t.Fatalf("pop empty: %v %d %v", found, n, err)
	}
	if _, found, _, err := s.Pop("", "never-existed", true); found || err != nil {
		t.Fatalf("pop absent: %v %v", found, err)
	}
}

// TestListMutationAtomicity: parallel unique appends and parallel removes
// land deterministically, and parallel pops from both ends hand out every
// element exactly once.
func TestListMutationAtomicity(t *testing.T) {
	s := openTemp(t)

	// Parallel unique appends of an overlapping value set → the value SET,
	// no duplicates.
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := s.Append("atomic", "set", []any{fmt.Sprintf("v%d", i)}, true); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	got, _, _ := s.Get("atomic", "set")
	if len(got.([]any)) != 10 {
		t.Fatalf("unique append under contention: %d values (want 10): %v", len(got.([]any)), got)
	}

	// Parallel removes of disjoint halves → exactly the untouched values.
	for w := 0; w < 2; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < 10; i += 2 {
				if i%4 == w%4 { // remove v0,v4,v8 and v1,v5,v9
					if _, err := s.Remove("atomic", "set", []any{fmt.Sprintf("v%d", i)}); err != nil {
						t.Error(err)
					}
				}
			}
		}()
	}
	wg.Wait()
	// Removed: v0,v4,v8 (worker 0) and v1,v5,v9 (worker 1) → v2,v3,v6,v7 stay.
	got, _, _ = s.Get("atomic", "set")
	if !reflect.DeepEqual(got, []any{"v2", "v3", "v6", "v7"}) {
		t.Fatalf("parallel remove: %v", got)
	}

	// Parallel pops from both ends: every element exactly once.
	items := make([]any, 40)
	for i := range items {
		items[i] = fmt.Sprintf("it%02d", i)
	}
	_, _ = s.Append("atomic", "queue", items, false)
	var mu sync.Mutex
	seen := map[string]int{}
	for w := 0; w < 8; w++ {
		front := w%2 == 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				v, found, _, err := s.Pop("atomic", "queue", front)
				if err != nil {
					t.Error(err)
					return
				}
				if !found {
					return
				}
				mu.Lock()
				seen[v.(string)]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Fatalf("parallel pop: %d distinct items (want 40)", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("item %s popped %d times", k, n)
		}
	}
	if n, _ := s.Len("atomic", "queue"); n != 0 {
		t.Fatalf("queue not drained: %d", n)
	}
}

// TestDefaultStoreWiring: SetDefaultPath + lazy Default(), including the
// re-point-and-reopen path tests rely on.
func TestDefaultStoreWiring(t *testing.T) {
	SetDefaultPath("")
	if _, err := Default(); err == nil {
		t.Fatal("Default without a path must error")
	}
	p1 := filepath.Join(t.TempDir(), "kv.db")
	SetDefaultPath(p1)
	s1, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Set("", "k", "v1", 0); err != nil {
		t.Fatal(err)
	}
	again, _ := Default()
	if again != s1 {
		t.Fatal("Default must return the same open store")
	}
	SetDefaultPath(p1) // same path: no-op, still open
	if s, _ := Default(); s != s1 {
		t.Fatal("same-path SetDefaultPath must keep the store")
	}
	p2 := filepath.Join(t.TempDir(), "kv.db")
	SetDefaultPath(p2)
	s2, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s2.Get("", "k"); found {
		t.Fatal("new path must be a fresh store")
	}
	SetDefaultPath("")
}

// TestErrorBranches: unserializable values, required keys, and the type
// names in mismatch errors.
func TestErrorBranches(t *testing.T) {
	s := openTemp(t)
	if err := s.Set("", "bad", make(chan int), 0); err == nil || !contains(err.Error(), "JSON-serializable") {
		t.Fatalf("unserializable set: %v", err)
	}
	if _, _, err := s.SetNX("", "", 1, 0); err == nil {
		t.Fatal("setnx empty key")
	}
	for _, err := range []error{
		func() error { return s.Set("", "", 1, 0) }(),
		func() error { _, _, e := s.Get("", ""); return e }(),
		func() error { return s.Delete("", "") }(),
		func() error { _, e := s.Incr("", "", 1); return e }(),
		func() error { _, e := s.Merge("", "", nil); return e }(),
		func() error { _, e := s.Append("", "", nil, false); return e }(),
		func() error { _, e := s.Remove("", "", nil); return e }(),
		func() error { _, e := s.Contains("", "", 1); return e }(),
		func() error { _, e := s.Len("", ""); return e }(),
		func() error { _, _, _, e := s.Pop("", "", false); return e }(),
	} {
		if err == nil || !contains(err.Error(), "key is required") {
			t.Fatalf("empty key must error: %v", err)
		}
	}
	// typeName in mismatches: a list refuses merge; an object refuses append;
	// null appends refuse too.
	_, _ = s.Append("", "alist", []any{1}, false)
	if _, err := s.Merge("", "alist", map[string]any{"a": 1}); err == nil || !contains(err.Error(), "list") {
		t.Fatalf("merge over list: %v", err)
	}
	_, _ = s.Merge("", "anobj", map[string]any{"a": 1})
	if _, err := s.Append("", "anobj", []any{1}, false); err == nil || !contains(err.Error(), "object") {
		t.Fatalf("append over object: %v", err)
	}
	_ = s.Set("", "anull", nil, 0)
	if _, err := s.Len("", "anull"); err == nil || !contains(err.Error(), "null") {
		t.Fatalf("len over null: %v", err)
	}
}
