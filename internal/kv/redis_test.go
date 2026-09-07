package kv

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// miniStore spins an in-memory redis and returns the backend + the server
// (for clock control).
func miniStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	st := NewRedisStore(redis.NewClient(&redis.Options{Addr: mini.Addr()}))
	t.Cleanup(func() { _ = st.Close() })
	return st, mini
}

// TestConformanceRedis: the shared contract suite against the redis backend
// (miniredis; FastForward drives ttl expiry).
func TestConformanceRedis(t *testing.T) {
	st, mini := miniStore(t)
	runConformanceAdv(t, st, func(d time.Duration) { mini.FastForward(d) })
}

// TestRedisAtomicityHeavy: the redis-specific concurrency pass — parallel
// incr, unique appends, and both-end pops each land exactly once (Lua
// scripts are the transaction).
func TestRedisAtomicityHeavy(t *testing.T) {
	st, _ := miniStore(t)
	var wg sync.WaitGroup
	for w := 0; w < 12; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := st.Incr("hot", "n", 1); err != nil {
					t.Error(err)
					return
				}
				if _, err := st.Append("hot", "set", []any{fmt.Sprintf("v%d", i%7)}, true); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if v, _ := st.Incr("hot", "n", 0); v != 240 {
		t.Fatalf("incr under contention: %d", v)
	}
	if n, _ := st.Len("hot", "set"); n != 7 {
		lst, _, _ := st.Get("hot", "set")
		t.Fatalf("unique append under contention: %d %v", n, lst)
	}

	items := make([]any, 60)
	for i := range items {
		items[i] = i
	}
	_, _ = st.Append("hot", "q", items, false)
	var mu sync.Mutex
	seen := map[float64]int{}
	for w := 0; w < 8; w++ {
		front := w%2 == 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				v, found, _, err := st.Pop("hot", "q", front)
				if err != nil {
					t.Error(err)
					return
				}
				if !found {
					return
				}
				mu.Lock()
				seen[v.(float64)]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 60 {
		t.Fatalf("parallel pop: %d distinct (want 60)", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("item %v popped %d times", k, n)
		}
	}
}

// TestRedisNamespaceScan: prefix listing escapes glob metacharacters and
// stays namespace-scoped.
func TestRedisNamespaceScan(t *testing.T) {
	st, _ := miniStore(t)
	_ = st.Set("a", "p-1", 1.0, 0)
	_ = st.Set("a", "p-2", 2.0, 0)
	_ = st.Set("a", "q-1", 3.0, 0)
	_ = st.Set("b", "p-1", 4.0, 0)
	_ = st.Set("a", "we[i]rd*", 5.0, 0)
	keys, entries, err := st.List("a", "p-")
	if err != nil || len(keys) != 2 || entries["p-1"] != 1.0 {
		t.Fatalf("prefix: %v %v %v", keys, entries, err)
	}
	keys, _, _ = st.List("a", "we[i]rd")
	if len(keys) != 1 || keys[0] != "we[i]rd*" {
		t.Fatalf("glob escape: %v", keys)
	}
	keys, _, _ = st.List("b", "")
	if len(keys) != 1 {
		t.Fatalf("namespace scope: %v", keys)
	}
}

// TestRedisSeparatorInNameRefused (#57 M7): the redis backend joins namespace
// and key with a U+001F unit separator, so a name that itself contains that
// byte could straddle the boundary — e.g. namespace "a", key "\x1fb" would map
// to the same physical key as namespace "a", key "b" under a crafted namespace
// "a\x1f". Every verb must refuse a namespace or key carrying the separator
// rather than let it collide with, or read across, another namespace.
func TestRedisSeparatorInNameRefused(t *testing.T) {
	st, _ := miniStore(t)

	// A legitimate value lives at namespace "tenant-a", key "secret".
	if err := st.Set("tenant-a", "secret", "owned-by-a", 0); err != nil {
		t.Fatal(err)
	}

	const sep = "\x1f"
	wantRefused := func(op string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: separator in name must be refused", op)
		}
		if !strings.Contains(err.Error(), "U+001F") {
			t.Fatalf("%s: error must name the separator, got %v", op, err)
		}
	}

	// A crafted key carrying the separator (attempting to straddle into another
	// namespace's keyspace) is refused on every operation.
	wantRefused("set/key", st.Set("tenant-b", sep+"tenant-a"+sep+"secret", "pwned", 0))
	{
		_, _, err := st.Get("tenant-b", sep+"tenant-a"+sep+"secret")
		wantRefused("get/key", err)
	}
	wantRefused("delete/key", st.Delete("tenant-b", sep+"x"))
	{
		_, err := st.Incr("tenant-b", sep+"n", 1)
		wantRefused("incr/key", err)
	}
	{
		_, err := st.Append("tenant-b", sep+"l", []any{1}, false)
		wantRefused("append/key", err)
	}

	// A crafted namespace is refused too.
	{
		_, _, err := st.Get("tenant-a"+sep, "secret")
		wantRefused("get/namespace", err)
	}
	{
		_, _, err := st.List("tenant-a"+sep, "")
		wantRefused("list/namespace", err)
	}

	// The refusals never touched the legitimate value.
	v, found, err := st.Get("tenant-a", "secret")
	if err != nil || !found || v != "owned-by-a" {
		t.Fatalf("legit value must be intact: %v %v %v", v, found, err)
	}

	// CheckName accepts ordinary names.
	if err := CheckName("tenant-a", "secret"); err != nil {
		t.Fatalf("ordinary name must pass: %v", err)
	}
}
