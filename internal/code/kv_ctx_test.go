package code

import (
	"reflect"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

// The ctx.store SURFACE, exercised through the one dispatcher every engine
// reaches it by. These used to be four near-identical tests — one per
// in-process engine face (js/go-embed/risor/lua) — asserting the same ops
// through four bindings. The bindings are gone with the engines; the
// dispatcher they all called is not, so the op contract is tested where it
// actually lives, once. Both surviving callers land here: the `cli` engine's
// socket and a plugin engine's host.kv both go through CtxHandler.Invoke.

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

// kvCall runs one kv op through the data plane and fails the test if it did
// not succeed — the common case, so the assertions below read as values.
func kvCall(t *testing.T, h CtxHandler, op string, args ...any) any {
	t.Helper()
	res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: op, Resource: "s", Args: args})
	if !res.OK {
		t.Fatalf("kv.%s%v: %s", op, args, res.Error)
	}
	return res.Value
}

// TestCtxKVOpSurface: the whole ctx.store method set, and the writes landing
// in the shared store rather than being echoed back.
func TestCtxKVOpSurface(t *testing.T) {
	st := tempKV(t)
	if _, err := st.Append("q", "jobs", []any{"a", "b", "c", "d"}, false); err != nil {
		t.Fatal(err)
	}
	h := CtxHandler{}

	// An absent read folds "not found" into a null value, so dynamic code can
	// write `if (!v) …` without a second return value.
	if v := kvCall(t, h, "get", "ns", "nope"); v != nil {
		t.Errorf("absent get = %#v, want nil", v)
	}

	kvCall(t, h, "set", "ns", "obj", map[string]any{"deep": []any{float64(1), "two"}})
	nx, _ := kvCall(t, h, "setnx", "ns", "obj", "loser").(map[string]any)
	if nx["created"] != false {
		t.Errorf("setnx over an existing key: %#v", nx)
	}
	merged, _ := kvCall(t, h, "merge", "ns", "obj", map[string]any{"extra": true}).(map[string]any)
	if merged["extra"] != true {
		t.Errorf("merge = %#v", merged)
	}
	if n := kvCall(t, h, "incr", "ns", "count", 5); n != int64(5) {
		t.Errorf("incr = %#v", n)
	}
	kvCall(t, h, "append", "ns", "tags", []any{"x", "y", "x"}, true)
	kvCall(t, h, "remove", "ns", "tags", "y")
	if has := kvCall(t, h, "contains", "ns", "tags", "x"); has != true {
		t.Errorf("contains = %#v", has)
	}

	// The list ops, against the seeded queue.
	for _, c := range []struct {
		op   string
		args []any
		want any
	}{
		{"first", []any{"q", "jobs"}, "a"},
		{"last", []any{"q", "jobs"}, "d"},
		{"index", []any{"q", "jobs", -2}, "c"},
		{"len", []any{"q", "jobs"}, 4},
		{"pop", []any{"q", "jobs", "front"}, "a"},
	} {
		if got := kvCall(t, h, c.op, c.args...); got != c.want {
			t.Errorf("%s = %#v, want %#v", c.op, got, c.want)
		}
	}
	if mid, ok := kvCall(t, h, "slice", "q", "jobs", 0, 2).([]any); !ok ||
		!reflect.DeepEqual(mid, []any{"b", "c"}) {
		t.Errorf("slice after pop = %#v", mid)
	}
	listed, _ := kvCall(t, h, "list", "ns").(map[string]any)
	if keys, ok := listed["keys"].([]any); !ok || len(keys) != 3 { // obj, count, tags
		t.Errorf("list keys = %#v", listed["keys"])
	}

	// The writes are really in the shared store, with their types intact.
	v, _, _ := st.Get("ns", "obj")
	obj, _ := v.(map[string]any)
	if obj["extra"] != true || !reflect.DeepEqual(obj["deep"], []any{float64(1), "two"}) {
		t.Fatalf("store after the run: %#v", v)
	}
	if tags, _, _ := st.Get("ns", "tags"); !reflect.DeepEqual(tags, []any{"x"}) {
		t.Fatalf("tags: %#v", tags)
	}
	if n, _ := st.Len("q", "jobs"); n != 3 {
		t.Fatalf("pop did not persist: %d", n)
	}
}

// A type error from the store is an ERROR, not a policy refusal — the
// distinction a client branches on.
func TestCtxKVTypeErrorIsNotARefusal(t *testing.T) {
	tempKV(t)
	h := CtxHandler{}
	kvCall(t, h, "incr", "ns", "count", 1)
	res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "merge", Resource: "s",
		Args: []any{"ns", "count", map[string]any{"a": 1}}})
	if res.OK || res.Refused || !strings.Contains(res.Error, "not an object") {
		t.Fatalf("merge onto a number: %#v", res)
	}
}

// TestKVInvokeArity: the dispatcher's own argument contract — an op called
// with too few args says so instead of indexing off the end.
func TestKVInvokeArity(t *testing.T) {
	tempKV(t)
	for _, c := range []struct {
		op   string
		args []any
	}{
		{"get", []any{"ns"}},
		{"set", []any{"ns", "k"}},
		{"merge", []any{"ns", "k"}},
		{"append", []any{"ns", "k"}},
		{"index", []any{"ns", "k"}},
	} {
		if _, err := kvInvoke(nil, "s", c.op, c.args); err == nil ||
			!strings.Contains(err.Error(), "want") {
			t.Errorf("%s %v: want an arity error, got %v", c.op, c.args, err)
		}
	}
	if _, err := kvInvoke(nil, "s", "nosuch", nil); err == nil ||
		!strings.Contains(err.Error(), `no operation "nosuch"`) {
		t.Errorf("unknown op: %v", err)
	}
}
