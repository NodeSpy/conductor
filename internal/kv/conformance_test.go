package kv

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// runConformance drives the full KVBackend contract against one backend —
// the same suite runs against boltdb, redis (miniredis), and the http shim,
// so every backend proves identical semantics.
func runConformance(t *testing.T, b KVBackend) {
	t.Helper()

	t.Run("scalar round-trip and found", func(t *testing.T) {
		values := map[string]any{
			"str": "hello", "num": float64(42.5), "bool": true,
			"obj": map[string]any{"a": float64(1), "b": []any{"x"}},
			"arr": []any{float64(1), "two", false},
		}
		for k, v := range values {
			if err := b.Set("conf", k, v, 0); err != nil {
				t.Fatalf("set %s: %v", k, err)
			}
		}
		for k, want := range values {
			got, found, err := b.Get("conf", k)
			if err != nil || !found || !reflect.DeepEqual(got, want) {
				t.Fatalf("get %s: %#v %v %v (want %#v)", k, got, found, err, want)
			}
		}
		if _, found, err := b.Get("conf", "missing"); found || err != nil {
			t.Fatalf("absent get: %v %v", found, err)
		}
		if err := b.Delete("conf", "str"); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := b.Get("conf", "str"); found {
			t.Fatal("deleted key still found")
		}
		if err := b.Delete("conf", "str"); err != nil {
			t.Fatalf("absent delete must be a no-op: %v", err)
		}
	})

	t.Run("namespace isolation", func(t *testing.T) {
		_ = b.Set("nsa", "key", "A", 0)
		_ = b.Set("nsb", "key", "B", 0)
		if v, _, _ := b.Get("nsa", "key"); v != "A" {
			t.Fatalf("nsa: %v", v)
		}
		_ = b.Delete("nsa", "key")
		if v, found, _ := b.Get("nsb", "key"); !found || v != "B" {
			t.Fatal("delete leaked across namespaces")
		}
	})

	t.Run("incr", func(t *testing.T) {
		if v, err := b.Incr("conf", "count", 1); err != nil || v != 1 {
			t.Fatalf("incr from absent: %d %v", v, err)
		}
		if v, _ := b.Incr("conf", "count", 4); v != 5 {
			t.Fatalf("incr by: %d", v)
		}
		if v, _ := b.Incr("conf", "count", -5); v != 0 {
			t.Fatalf("decrement: %d", v)
		}
		_ = b.Set("conf", "word", "abc", 0)
		if _, err := b.Incr("conf", "word", 1); err == nil {
			t.Fatal("incr on a string must error")
		}
	})

	t.Run("setnx", func(t *testing.T) {
		v, created, err := b.SetNX("conf", "lock", "one", 0)
		if err != nil || !created || v != "one" {
			t.Fatalf("create: %v %v %v", v, created, err)
		}
		v, created, err = b.SetNX("conf", "lock", "two", 0)
		if err != nil || created || v != "one" {
			t.Fatalf("existing: %v %v %v", v, created, err)
		}
	})

	t.Run("merge", func(t *testing.T) {
		v, err := b.Merge("conf", "prof", map[string]any{"name": "amy", "age": float64(30)})
		if err != nil || v["name"] != "amy" {
			t.Fatalf("create: %v %v", v, err)
		}
		v, err = b.Merge("conf", "prof", map[string]any{"age": float64(31)})
		if err != nil || v["age"] != float64(31) || v["name"] != "amy" {
			t.Fatalf("merge-over: %v %v", v, err)
		}
		_ = b.Set("conf", "scalar", float64(7), 0)
		if _, err := b.Merge("conf", "scalar", map[string]any{"a": float64(1)}); err == nil ||
			!strContains(err.Error(), "scalar") || !strContains(err.Error(), "number") {
			t.Fatalf("merge over number must name key+type: %v", err)
		}
	})

	t.Run("lists", func(t *testing.T) {
		lst, err := b.Append("conf", "tags", []any{"a"}, false)
		if err != nil || len(lst) != 1 {
			t.Fatalf("append-create: %v %v", lst, err)
		}
		lst, _ = b.Append("conf", "tags", []any{"b", "a"}, false)
		if !reflect.DeepEqual(lst, []any{"a", "b", "a"}) {
			t.Fatalf("append-many: %v", lst)
		}
		lst, _ = b.Append("conf", "tags", []any{"a", "c"}, true)
		if !reflect.DeepEqual(lst, []any{"a", "b", "a", "c"}) {
			t.Fatalf("append-unique: %v", lst)
		}
		if ok, _ := b.Contains("conf", "tags", "b"); !ok {
			t.Fatal("contains b")
		}
		if ok, _ := b.Contains("conf", "tags", "zz"); ok {
			t.Fatal("contains zz")
		}
		if ok, err := b.Contains("conf", "no-list", "x"); ok || err != nil {
			t.Fatalf("contains absent: %v %v", ok, err)
		}
		// Object membership is key-order independent.
		_, _ = b.Append("conf", "objs", []any{map[string]any{"a": float64(1), "b": float64(2)}}, false)
		if ok, _ := b.Contains("conf", "objs", map[string]any{"b": float64(2), "a": float64(1)}); !ok {
			t.Fatal("canonical object equality")
		}
		lst, err = b.Remove("conf", "tags", []any{"a"})
		if err != nil || !reflect.DeepEqual(lst, []any{"b", "c"}) {
			t.Fatalf("remove-all: %v %v", lst, err)
		}
		if lst, err := b.Remove("conf", "no-list", []any{"x"}); err != nil || len(lst) != 0 {
			t.Fatalf("remove absent: %v %v", lst, err)
		}
		// Type errors name key and type.
		_ = b.Set("conf", "notalist", "str", 0)
		if _, err := b.Append("conf", "notalist", []any{float64(1)}, false); err == nil ||
			!strContains(err.Error(), "notalist") || !strContains(err.Error(), "string") {
			t.Fatalf("append over string: %v", err)
		}
	})

	t.Run("list accessors", func(t *testing.T) {
		_, _ = b.Append("conf", "q", []any{"a", "b", "c", "d"}, false)
		if v, found, _ := b.First("conf", "q"); !found || v != "a" {
			t.Fatalf("first: %v %v", v, found)
		}
		if v, _, _ := b.Last("conf", "q"); v != "d" {
			t.Fatalf("last: %v", v)
		}
		if _, found, _ := b.First("conf", "empty-q"); found {
			t.Fatal("first absent")
		}
		if v, found, _ := b.Index("conf", "q", -1); !found || v != "d" {
			t.Fatalf("index -1: %v", v)
		}
		if _, found, _ := b.Index("conf", "q", 9); found {
			t.Fatal("index out of range")
		}
		if got, _ := b.Slice("conf", "q", 1, 3, true); !reflect.DeepEqual(got, []any{"b", "c"}) {
			t.Fatalf("[1:3): %v", got)
		}
		if got, _ := b.Slice("conf", "q", -2, 0, false); !reflect.DeepEqual(got, []any{"c", "d"}) {
			t.Fatalf("[-2:]: %v", got)
		}
		if got, _ := b.Slice("conf", "q", -99, 99, true); len(got) != 4 {
			t.Fatalf("clamp: %v", got)
		}
		if got, _ := b.Slice("conf", "q", 3, 1, true); len(got) != 0 {
			t.Fatalf("inverted: %v", got)
		}
		if n, _ := b.Len("conf", "q"); n != 4 {
			t.Fatalf("len: %d", n)
		}
		if n, err := b.Len("conf", "absent-q"); n != 0 || err != nil {
			t.Fatalf("len absent: %d %v", n, err)
		}
	})

	t.Run("pop", func(t *testing.T) {
		_, _ = b.Append("conf", "stack", []any{"x", "y", "z"}, false)
		v, found, n, err := b.Pop("conf", "stack", false)
		if err != nil || !found || v != "z" || n != 2 {
			t.Fatalf("pop back: %v %v %d %v", v, found, n, err)
		}
		v, found, _, _ = b.Pop("conf", "stack", true)
		if !found || v != "x" {
			t.Fatalf("pop front: %v", v)
		}
		_, _, _, _ = b.Pop("conf", "stack", false)
		if _, found, n, err := b.Pop("conf", "stack", false); found || n != 0 || err != nil {
			t.Fatalf("pop empty: %v %d %v", found, n, err)
		}
		if _, found, _, err := b.Pop("conf", "never", true); found || err != nil {
			t.Fatalf("pop absent: %v %v", found, err)
		}
	})

	t.Run("ttl", func(t *testing.T) {
		if err := b.Set("conf", "blip", "x", 30*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := b.Get("conf", "blip"); !found {
			t.Fatal("fresh ttl key readable")
		}
		time.Sleep(60 * time.Millisecond)
		if _, found, _ := b.Get("conf", "blip"); found {
			t.Fatal("expired key must read absent")
		}
		keys, _, _ := b.List("conf", "blip")
		if len(keys) != 0 {
			t.Fatalf("expired key in list: %v", keys)
		}
	})

	t.Run("list keys and prefix", func(t *testing.T) {
		_ = b.Set("scan", "p-1", float64(1), 0)
		_ = b.Set("scan", "p-2", float64(2), 0)
		_ = b.Set("scan", "other", float64(3), 0)
		keys, entries, err := b.List("scan", "p-")
		if err != nil || !reflect.DeepEqual(keys, []string{"p-1", "p-2"}) {
			t.Fatalf("prefix list: %v %v", keys, err)
		}
		if entries["p-2"] != float64(2) {
			t.Fatalf("entries: %v", entries)
		}
		keys, _, _ = b.List("scan", "")
		if len(keys) != 3 {
			t.Fatalf("full list: %v", keys)
		}
		if keys, _, _ := b.List("no-such-ns", ""); len(keys) != 0 {
			t.Fatalf("unknown namespace: %v", keys)
		}
	})

	t.Run("rmw atomicity", func(t *testing.T) {
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 10; i++ {
					if _, err := b.Incr("atomic", "hits", 1); err != nil {
						t.Error(err)
						return
					}
					if _, err := b.Append("atomic", "uniq", []any{fmt.Sprintf("v%d", i)}, true); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		if v, _ := b.Incr("atomic", "hits", 0); v != 80 {
			t.Fatalf("concurrent incr: %d", v)
		}
		if n, _ := b.Len("atomic", "uniq"); n != 10 {
			lst, _, _ := b.Get("atomic", "uniq")
			t.Fatalf("concurrent unique append: %d %v", n, lst)
		}
		// Parallel pops from both ends: every element exactly once.
		items := make([]any, 30)
		for i := range items {
			items[i] = fmt.Sprintf("it%02d", i)
		}
		_, _ = b.Append("atomic", "queue", items, false)
		var mu sync.Mutex
		seen := map[string]int{}
		for w := 0; w < 6; w++ {
			front := w%2 == 0
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					v, found, _, err := b.Pop("atomic", "queue", front)
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
		if len(seen) != 30 {
			t.Fatalf("parallel pop: %d distinct (want 30)", len(seen))
		}
		for k, n := range seen {
			if n != 1 {
				t.Fatalf("%s popped %d times", k, n)
			}
		}
	})
}

// strContains avoids importing strings into every conformance assertion.
func strContains(s, sub string) bool { return indexOf(s, sub) >= 0 }

// TestConformanceBoltdb runs the shared suite against the boltdb backend.
func TestConformanceBoltdb(t *testing.T) {
	runConformance(t, openTemp(t))
}
