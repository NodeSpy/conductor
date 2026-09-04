package connector

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/kv"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// kvInstance builds a registry whose config defines one boltdb store (s1)
// and returns the always-on kv instance.
func kvInstance(t *testing.T) *Instance {
	t.Helper()
	kv.SetDataDir(t.TempDir())
	t.Cleanup(func() { kv.SetDataDir(""); kv.ResetStores() })
	reg := buildAPIRegistry(t, "connectors: {}\nstores:\n  s1: { type: boltdb }\n", secrets.New())
	in, ok := reg.Get("kv")
	if !ok || in.DisabledReason != "" || !in.Enabled {
		t.Fatalf("kv instance: ok=%v %+v", ok, in)
	}
	return in
}

// TestKVAlwaysRegistered: the instance exists with no config, publishes the
// full verb set, and has no source events.
func TestKVAlwaysRegistered(t *testing.T) {
	in := kvInstance(t)
	want := []string{"append", "contains", "delete", "first", "get", "incr",
		"index", "last", "len", "list", "merge", "pop", "remove", "set", "setnx", "slice"}
	if got := in.Decl.VerbNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("verbs: %v", got)
	}
	if evs := in.Decl.EventNames(); len(evs) != 0 {
		t.Fatalf("kv has no events: %v", evs)
	}
	if _, err := in.Impl.Source([]CompiledTrigger{{}}); err == nil {
		t.Fatal("kv must reject triggers")
	}
	// Every verb requires a store: selector naming a defined store.
	if _, err := in.Invoke(context.Background(), "get", map[string]any{"key": "k"}); err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("missing store: %v", err)
	}
	if _, err := in.Invoke(context.Background(), "get", map[string]any{"store": "nope", "key": "k"}); err == nil || !strings.Contains(err.Error(), `no store named "nope"`) {
		t.Fatalf("unknown store: %v", err)
	}
}

// TestKVVerbSurface: each verb's options/outputs contract through Invoke,
// including defaults, item|items, ttl parsing, and the error shapes.
func TestKVVerbSurface(t *testing.T) {
	in := kvInstance(t)
	ctx := context.Background()
	call := func(verb string, opts map[string]any) map[string]any {
		t.Helper()
		out, err := in.Invoke(ctx, verb, opts)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		return out
	}

	// get on absent: found=false, default honored (else null).
	out := call("get", map[string]any{"store": "s1", "key": "missing"})
	if out["found"] != false || out["value"] != nil {
		t.Fatalf("absent get: %v", out)
	}
	out = call("get", map[string]any{"store": "s1", "key": "missing", "default": "fb"})
	if out["value"] != "fb" || out["found"] != false {
		t.Fatalf("default: %v", out)
	}

	// set + get round-trip with namespace + structured value.
	call("set", map[string]any{"store": "s1", "namespace": "pd", "key": "last", "value": map[string]any{"id": "i-1"}})
	out = call("get", map[string]any{"store": "s1", "namespace": "pd", "key": "last"})
	if out["found"] != true || out["value"].(map[string]any)["id"] != "i-1" {
		t.Fatalf("round-trip: %v", out)
	}

	// setnx created vs existing.
	out = call("setnx", map[string]any{"store": "s1", "key": "lock", "value": "a"})
	if out["created"] != true || out["value"] != "a" {
		t.Fatalf("setnx create: %v", out)
	}
	out = call("setnx", map[string]any{"store": "s1", "key": "lock", "value": "b"})
	if out["created"] != false || out["value"] != "a" {
		t.Fatalf("setnx existing: %v", out)
	}

	// merge upsert + type check.
	out = call("merge", map[string]any{"store": "s1", "key": "obj", "value": map[string]any{"a": 1}})
	if out["value"].(map[string]any)["a"] != 1 {
		t.Fatalf("merge: %v", out)
	}
	if _, err := in.Invoke(ctx, "merge", map[string]any{"store": "s1", "key": "obj", "value": "nope"}); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("merge non-object option: %v", err)
	}

	// incr default by=1 and explicit by.
	if out := call("incr", map[string]any{"store": "s1", "key": "n"}); out["value"] != int64(1) {
		t.Fatalf("incr: %v", out)
	}
	if out := call("incr", map[string]any{"store": "s1", "key": "n", "by": 4}); out["value"] != int64(5) {
		t.Fatalf("incr by: %v", out)
	}

	// append item vs items, unique; len output.
	out = call("append", map[string]any{"store": "s1", "key": "l", "item": "x"})
	if out["len"] != 1 {
		t.Fatalf("append item: %v", out)
	}
	out = call("append", map[string]any{"store": "s1", "key": "l", "items": []any{"y", "x"}, "unique": true})
	if out["len"] != 2 {
		t.Fatalf("append unique: %v", out)
	}
	if _, err := in.Invoke(ctx, "append", map[string]any{"store": "s1", "key": "l"}); err == nil || !strings.Contains(err.Error(), "item") {
		t.Fatalf("append needs item|items: %v", err)
	}

	// contains / remove / list accessors.
	if out := call("contains", map[string]any{"store": "s1", "key": "l", "item": "y"}); out["contains"] != true {
		t.Fatalf("contains: %v", out)
	}
	out = call("remove", map[string]any{"store": "s1", "key": "l", "item": "x"})
	if out["len"] != 1 || !reflect.DeepEqual(out["value"], []any{"y"}) {
		t.Fatalf("remove: %v", out)
	}
	call("append", map[string]any{"store": "s1", "key": "l", "items": []any{"z", "w"}})
	if out := call("first", map[string]any{"store": "s1", "key": "l"}); out["value"] != "y" || out["found"] != true {
		t.Fatalf("first: %v", out)
	}
	if out := call("last", map[string]any{"store": "s1", "key": "l"}); out["value"] != "w" {
		t.Fatalf("last: %v", out)
	}
	if out := call("index", map[string]any{"store": "s1", "key": "l", "index": -2}); out["value"] != "z" {
		t.Fatalf("index: %v", out)
	}
	if out := call("index", map[string]any{"store": "s1", "key": "l", "index": 9}); out["found"] != false || out["value"] != nil {
		t.Fatalf("index out of range: %v", out)
	}
	out = call("slice", map[string]any{"store": "s1", "key": "l", "start": 1, "end": -1})
	if !reflect.DeepEqual(out["value"], []any{"z"}) || out["len"] != 1 {
		t.Fatalf("slice: %v", out)
	}
	if out := call("len", map[string]any{"store": "s1", "key": "l"}); out["len"] != 3 {
		t.Fatalf("len: %v", out)
	}
	if out := call("pop", map[string]any{"store": "s1", "key": "l", "from": "front"}); out["value"] != "y" || out["len"] != 2 {
		t.Fatalf("pop front: %v", out)
	}
	if out := call("pop", map[string]any{"store": "s1", "key": "l"}); out["value"] != "w" {
		t.Fatalf("pop back default: %v", out)
	}
	if out := call("pop", map[string]any{"store": "s1", "key": "drained"}); out["found"] != false {
		t.Fatalf("pop absent: %v", out)
	}

	// delete + list with prefix.
	call("set", map[string]any{"store": "s1", "key": "seen-1", "value": 1})
	call("set", map[string]any{"store": "s1", "key": "seen-2", "value": 2})
	call("delete", map[string]any{"store": "s1", "key": "seen-1"})
	out = call("list", map[string]any{"store": "s1", "prefix": "seen-"})
	if !reflect.DeepEqual(out["keys"], []any{"seen-2"}) {
		t.Fatalf("list: %v", out)
	}

	// ttl: parses, expires.
	call("set", map[string]any{"store": "s1", "key": "blip", "value": 1, "ttl": "15ms"})
	time.Sleep(25 * time.Millisecond)
	if out := call("get", map[string]any{"store": "s1", "key": "blip"}); out["found"] != false {
		t.Fatalf("ttl expiry: %v", out)
	}

	// Error shapes.
	for _, bad := range []struct {
		verb string
		opts map[string]any
		want string
	}{
		{"set", map[string]any{"store": "s1", "key": "k", "value": 1, "ttl": "bogus"}, "bad ttl"},
		{"setnx", map[string]any{"store": "s1", "key": "k", "value": 1, "ttl": "bogus"}, "bad ttl"},
		{"pop", map[string]any{"store": "s1", "key": "l", "from": "middle"}, "front or back"},
		{"index", map[string]any{"store": "s1", "key": "l", "index": "one"}, "must be an integer"},
		{"slice", map[string]any{"store": "s1", "key": "l", "start": "one"}, "must be an integer"},
		{"nosuch", map[string]any{"store": "s1"}, `no verb "nosuch"`},
	} {
		if _, err := in.Invoke(ctx, bad.verb, bad.opts); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: want %q, got %v", bad.verb, bad.want, err)
		}
	}
}

// TestKVConfiguredNameRejected: a user-defined connector named kv is a load
// error (the name is reserved for the built-in).
func TestKVConfiguredNameRejected(t *testing.T) {
	var cfg config.Config
	if err := yaml.Unmarshal([]byte("connectors:\n  kv: { type: command }\ntriggers:\n  - { on: kv.tick, steps: [ { uses: kv.get, options: { key: k } } ] }\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved name: %v", err)
	}
}
