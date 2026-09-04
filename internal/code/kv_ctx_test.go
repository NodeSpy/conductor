package code

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

// tempKV registers one boltdb store named "s" for a test and returns it.
func tempKV(t *testing.T) kv.KVBackend {
	t.Helper()
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	st, err := kv.OpenBoltStore("s", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := kv.Register("s", st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestCtxKVJS: the full ctx.kv surface from run: js — writes are visible in
// the shared store afterwards, absent reads are null, errors throw.
func TestCtxKVJS(t *testing.T) {
	st := tempKV(t)
	_, _ = st.Append("q", "jobs", []any{"a", "b", "c", "d"}, false)

	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "js", Code: `
const kv = ctx.store("s");
const missing = kv.get("ns", "nope");
kv.set("ns", "obj", { deep: [1, "two"] });
const nx = kv.setnx("ns", "obj", "loser");
const merged = kv.merge("ns", "obj", { extra: true });
const n = kv.incr("ns", "count", 5);
kv.append("ns", "tags", ["x", "y", "x"], true);
kv.remove("ns", "tags", "y");
return {
  missing: missing,
  created: nx.created,
  merged: merged.extra,
  n: n,
  has: kv.contains("ns", "tags", "x"),
  first: kv.first("q", "jobs"),
  last: kv.last("q", "jobs"),
  at: kv.index("q", "jobs", -2),
  mid: kv.slice("q", "jobs", 1, 3),
  len: kv.len("q", "jobs"),
  popped: kv.pop("q", "jobs", "front"),
  keys: kv.list("ns").keys,
};`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"missing": nil, "created": false, "merged": true, "n": float64(5),
		"has": true, "first": "a", "last": "d", "at": "c",
		"len": float64(4), "popped": "a",
	}
	for k, w := range want {
		if !reflect.DeepEqual(out[k], w) {
			t.Errorf("%s = %#v, want %#v", k, out[k], w)
		}
	}
	if mid, ok := out["mid"].([]any); !ok || !reflect.DeepEqual(mid, []any{"b", "c"}) {
		t.Errorf("mid = %#v", out["mid"])
	}
	if keys, ok := out["keys"].([]any); !ok || len(keys) != 3 { // obj, count, tags
		t.Errorf("keys = %#v", out["keys"])
	}
	// The writes landed in the shared store.
	v, _, _ := st.Get("ns", "obj")
	obj := v.(map[string]any)
	if obj["extra"] != true || !reflect.DeepEqual(obj["deep"], []any{float64(1), "two"}) {
		t.Fatalf("js writes: %v", v)
	}
	tags, _, _ := st.Get("ns", "tags")
	if !reflect.DeepEqual(tags, []any{"x"}) {
		t.Fatalf("tags: %v", tags)
	}
	if n, _ := st.Len("q", "jobs"); n != 3 {
		t.Fatalf("pop persisted: %d", n)
	}
	// A kv type error surfaces as a thrown JS error.
	_, err = e.Exec(context.Background(), Spec{Run: "js", Code: `ctx.store("s").merge("ns", "count", {a: 1}); return 1`}, nil)
	if err == nil || !strings.Contains(err.Error(), "not an object") {
		t.Fatalf("js kv error: %v", err)
	}
}

// TestCtxKVGoEmbed: the `import "conductor/store"` virtual package in
// run: go-embed — store.Use resolves a defined store to a typed handle.
func TestCtxKVGoEmbed(t *testing.T) {
	st := tempKV(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: `
import "conductor/store"

func run(ctx map[string]any) (any, error) {
	kv, err := store.Use("s")
	if err != nil {
		return nil, err
	}
	if err := kv.Set("ns", "who", "embed"); err != nil {
		return nil, err
	}
	n, err := kv.Incr("ns", "count", 2)
	if err != nil {
		return nil, err
	}
	if _, err := kv.Append("ns", "l", []any{"a", "b"}, false); err != nil {
		return nil, err
	}
	last, err := kv.Last("ns", "l")
	if err != nil {
		return nil, err
	}
	popped, err := kv.Pop("ns", "l", "back")
	if err != nil {
		return nil, err
	}
	ok, err := kv.Contains("ns", "l", "a")
	if err != nil {
		return nil, err
	}
	v, err := kv.Get("ns", "who")
	if err != nil {
		return nil, err
	}
	return map[string]any{"v": v, "n": n, "last": last, "popped": popped, "ok": ok}, nil
}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["v"] != "embed" || out["n"] != int64(2) || out["last"] != "b" || out["popped"] != "b" || out["ok"] != true {
		t.Fatalf("go-embed kv: %#v", out)
	}
	if v, _, _ := st.Get("ns", "who"); v != "embed" {
		t.Fatalf("store: %v", v)
	}
}

// TestCtxKVRisor: the top-level kv module in run: risor.
func TestCtxKVRisor(t *testing.T) {
	st := tempKV(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "risor", Code: `
s := store("s")
s.set("ns", "who", "risor")
s.append("ns", "l", ["x", "y"])
{
  "v": s.get("ns", "who"),
  "n": s.incr("ns", "count", 3),
  "first": s.first("ns", "l"),
  "len": s.len("ns", "l"),
  "missing": s.get("ns", "nope"),
}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["v"] != "risor" || out["n"] != int64(3) || out["first"] != "x" || out["len"] != int64(2) || out["missing"] != nil {
		t.Fatalf("risor kv: %#v", out)
	}
	if v, _, _ := st.Get("ns", "who"); v != "risor" {
		t.Fatalf("store: %v", v)
	}
}

// TestCtxKVLua: the ctx.kv table in run: lua, including an error raise.
func TestCtxKVLua(t *testing.T) {
	st := tempKV(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `
local kv = ctx.store("s")
kv.set("ns", "who", "lua")
kv.append("ns", "l", {"a", "b", "c"})
return {
  v = kv.get("ns", "who"),
  n = kv.incr("ns", "count", 4),
  popped = kv.pop("ns", "l", "front"),
  len = kv.len("ns", "l"),
  missing = kv.get("ns", "nope") == nil,
}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["v"] != "lua" || out["n"] != int64(4) || out["popped"] != "a" || out["len"] != int64(2) || out["missing"] != true {
		t.Fatalf("lua kv: %#v", out)
	}
	if v, _, _ := st.Get("ns", "who"); v != "lua" {
		t.Fatalf("store: %v", v)
	}
	_, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `ctx.store("s").incr("ns", "who")`}, nil)
	if err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Fatalf("lua kv error: %v", err)
	}
}
