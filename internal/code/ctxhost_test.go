package code

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

// CtxHandler is the data plane an out-of-process engine gets. The property
// under test throughout is that it is the SAME authorization it would have
// got in-process: the step's DataGuard, then the store's own gates, then the
// op — never the op first.

// guardDenyingStore mimics the shape flow installs for an agent-authored
// step: the resource allowlist (only `allowed` is reachable) plus the
// no_secret_egress write barrier (a value write carrying `secret` is
// refused). See flow.(*Runner).planDataGuard.
func guardDenyingStore(allowed, secret string) DataGuard {
	return func(kind, op, resource string, args []any) error {
		if (kind == "kv" || kind == "sql") && resource != allowed {
			return fmt.Errorf("agent_authored allowlist: code step touches store %q", resource)
		}
		if kvValueWrites[op] || op == "exec" || op == "remember" {
			if strings.Contains(fmt.Sprint(args...), secret) {
				return fmt.Errorf("no_secret_egress: refusing to write secret material into %s.%s", kind, op)
			}
		}
		return nil
	}
}

// An allowed get/set round-trips, and the value that comes back out of the
// handler is the value that went into the store.
func TestCtxHandlerRoundTrip(t *testing.T) {
	st := tempKV(t)
	h := CtxHandler{Guard: guardDenyingStore("s", "hunter2")}

	res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "set", Resource: "s",
		Args: []any{"ns", "k", map[string]any{"deep": []any{float64(1), "two"}}}})
	if !res.OK {
		t.Fatalf("set: %#v", res)
	}
	res = h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "get", Resource: "s", Args: []any{"ns", "k"}})
	if !res.OK {
		t.Fatalf("get: %#v", res)
	}
	got, ok := res.Value.(map[string]any)
	if !ok || fmt.Sprint(got["deep"]) != "[1 two]" {
		t.Fatalf("value = %#v", res.Value)
	}
	// …and it really is in the shared store, not just echoed back.
	if v, found, _ := st.Get("ns", "k"); !found || fmt.Sprint(v) != fmt.Sprint(res.Value) {
		t.Fatalf("store = %#v (found %v)", v, found)
	}
	// An absent read is a null value, not an error — the same fold every
	// in-process binding does (kvbind.go nullable).
	res = h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "get", Resource: "s", Args: []any{"ns", "nope"}})
	if !res.OK || res.Value != nil {
		t.Fatalf("absent read: %#v", res)
	}
	// An op the dispatcher doesn't have is an error, not a refusal.
	res = h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "truncate", Resource: "s", Args: []any{"ns"}})
	if res.OK || res.Refused || !strings.Contains(res.Error, "no operation") {
		t.Fatalf("unknown op: %#v", res)
	}
}

// openSecondStore registers another boltdb store alongside tempKV's "s", so
// a test can prove the allowlist keeps a code step out of a store that is
// perfectly well defined and open.
func openSecondStore(t *testing.T, name string) (kv.KVBackend, error) {
	t.Helper()
	st, err := kv.OpenBoltStore(name, "")
	if err != nil {
		return nil, err
	}
	return st, kv.Register(name, st)
}

// The no_secret_egress half of the guard: a durable value write carrying
// tracked secret material is refused, and refused BEFORE the store is
// touched.
func TestCtxHandlerRefusesSecretWrite(t *testing.T) {
	st := tempKV(t)
	h := CtxHandler{Guard: guardDenyingStore("s", "hunter2")}

	res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "set", Resource: "s",
		Args: []any{"ns", "parked", "token=hunter2"}})
	if res.OK {
		t.Fatal("secret write was allowed")
	}
	if !res.Refused {
		t.Errorf("a policy denial must be typed as refused: %#v", res)
	}
	if !strings.Contains(res.Error, "no_secret_egress") {
		t.Errorf("error = %q", res.Error)
	}
	if _, found, _ := st.Get("ns", "parked"); found {
		t.Fatal("the refused write reached the store anyway")
	}
	// The same guard lets a non-secret write through, so the refusal is the
	// barrier working rather than writes being broken.
	if res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "set", Resource: "s",
		Args: []any{"ns", "fine", "ordinary"}}); !res.OK {
		t.Fatalf("clean write refused: %#v", res)
	}
}

// The allowlist half: a store outside `verbs.code.store` is unreachable, no
// matter that it is defined and open.
func TestCtxHandlerRefusesOutOfAllowlistStore(t *testing.T) {
	tempKV(t) // registers "s"
	other, err := openSecondStore(t, "other")
	if err != nil {
		t.Fatal(err)
	}
	h := CtxHandler{Guard: guardDenyingStore("s", "hunter2")}

	res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "set", Resource: "other",
		Args: []any{"ns", "k", "v"}})
	if res.OK || !res.Refused || !strings.Contains(res.Error, "allowlist") {
		t.Fatalf("out-of-allowlist store: %#v", res)
	}
	if _, found, _ := other.Get("ns", "k"); found {
		t.Fatal("the refused write reached the out-of-allowlist store")
	}
	// Reads are policed too — the allowlist is about touching the store at
	// all, not only about writing to it.
	if res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "get", Resource: "other",
		Args: []any{"ns", "k"}}); res.OK || !res.Refused {
		t.Fatalf("out-of-allowlist read: %#v", res)
	}
}

// sql and memory go through the same single core: the guard sees them with
// their own kind, and a refusal is typed the same way.
func TestCtxHandlerGuardsSQLAndMemory(t *testing.T) {
	tempSQL(t)
	tempMem(t)
	h := CtxHandler{Guard: guardDenyingStore("db", "hunter2")}

	if res := h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: "exec", Resource: "db",
		Args: []any{"INSERT INTO events (body) VALUES (?)", []any{"ok"}}}); !res.OK {
		t.Fatalf("sql exec: %#v", res)
	}
	if res := h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: "exec", Resource: "db",
		Args: []any{"INSERT INTO events (body) VALUES (?)", []any{"hunter2"}}}); res.OK || !res.Refused {
		t.Fatalf("sql secret write: %#v", res)
	}
	if res := h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: "query", Resource: "nope",
		Args: []any{"SELECT 1"}}); res.OK || !res.Refused {
		t.Fatalf("sql out-of-allowlist store: %#v", res)
	}
	if res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: "remember",
		Args: []any{"a note", []any{"ci"}, "repo:o/r"}}); !res.OK {
		t.Fatalf("memory remember: %#v", res)
	}
	if res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: "remember",
		Args: []any{"token=hunter2", nil, "repo:o/r"}}); res.OK || !res.Refused {
		t.Fatalf("memory secret write: %#v", res)
	}
}

// A nil guard is the config-authored case: the store's own gates still
// apply, and only they.
func TestCtxHandlerNilGuardStillHitsStoreGates(t *testing.T) {
	tempSQL(t)
	h := CtxHandler{}
	if res := h.Invoke(CtxRequest{Kind: CtxKindKV, Op: "get", Resource: "undefined-store",
		Args: []any{"ns", "k"}}); res.OK || res.Refused {
		// Not a policy refusal: the store simply is not defined. That is a
		// property of the config, and reads for itself.
		t.Fatalf("undefined store: %#v", res)
	}
	if res := h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: "query", Resource: "db",
		Args: []any{"SELECT 1 AS n"}}); !res.OK {
		t.Fatalf("nil guard blocked a legal query: %#v", res)
	}
}

// Malformed requests are answered, not crashed on.
func TestCtxHandlerMalformed(t *testing.T) {
	h := CtxHandler{}
	for _, tc := range []struct {
		name string
		req  CtxRequest
		want string
	}{
		{"no kind", CtxRequest{Op: "get"}, "no kind"},
		{"unknown kind", CtxRequest{Kind: "files", Op: "read"}, "no kind"},
		{"no op", CtxRequest{Kind: CtxKindKV}, "no op"},
	} {
		res := h.Invoke(tc.req)
		if res.OK || !strings.Contains(res.Error, tc.want) {
			t.Errorf("%s: %#v", tc.name, res)
		}
	}
}
