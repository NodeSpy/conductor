package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

// tempKV points the shared store at a temp file for one test.
func tempKV(t *testing.T) kv.KVBackend {
	t.Helper()
	kv.SetDataDir(t.TempDir())
	t.Cleanup(func() { kv.SetDataDir("") })
	st, err := kv.Default()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestKVTemplateFuncs: {{ kv }} in its 1-arg (default namespace) and 2-arg
// forms, composing with default on a missing key, and the read-only
// kvContains — the template surface never mutates.
func TestKVTemplateFuncs(t *testing.T) {
	st := tempKV(t)
	if err := st.Set("", "greeting", "hello", 0); err != nil {
		t.Fatal(err)
	}
	_ = st.Set("runs", "42", float64(7), 0)
	_, _ = st.Append("pd", "seen", []any{"i-1", "i-2"}, false)

	cases := []struct{ tmpl, want string }{
		{`{{ kv "greeting" }}`, "hello"},                        // 1-arg, default ns
		{`{{ kv "runs" "42" }}`, "7"},                           // 2-arg
		{`{{ kv "runs" (print .pr) }}`, "7"},                    // composed key
		{`{{ kv "missing" | default "fb" }}`, "fb"},             // absent → default
		{`{{ kv "runs" "nope" | default 0 }}`, "0"},             // absent in ns
		{`{{ kvContains "pd" "seen" "i-1" }}`, "true"},          // 3-arg
		{`{{ kvContains "pd" "seen" "i-9" }}`, "false"},         // not present
		{`{{ kvContains "absent-list" "x" }}`, "false"},         // 2-arg, absent key
		{`{{ if kvContains "pd" "seen" .id }}dup{{ end }}`, ""}, // composes in if
	}
	data := map[string]any{"pr": 42, "id": "i-9"}
	for _, c := range cases {
		got, err := render(c.tmpl, data)
		if err != nil {
			t.Fatalf("%s: %v", c.tmpl, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.tmpl, got, c.want)
		}
	}

	// Wrong arity is a render error, not a panic.
	if _, err := render(`{{ kv }}`, nil); err == nil {
		t.Fatal("kv with no args must error")
	}
	if _, err := render(`{{ kvContains "a" }}`, nil); err == nil {
		t.Fatal("kvContains with one arg must error")
	}
}

// TestKVStepsAndCodeShareOneStore: a run: js step writes through ctx.kv, a
// kv.get verb step reads the same key, and the value flows on into another
// verb — the three access paths hit ONE durable store.
func TestKVStepsAndCodeShareOneStore(t *testing.T) {
	st := tempKV(t)
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
`)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: write
    run: js
    code: |
      const prev = ctx.kv.get("billing", "last-invoice");
      ctx.kv.set("billing", "last-invoice", ctx.msg);
      const n = ctx.kv.incr("billing", "writes");
      ctx.kv.append("billing", "history", ctx.msg);
      return { first_time: !prev, writes: n };
  - id: gate
    uses: kv.get
    options: { namespace: billing, key: last-invoice }
  - id: post
    uses: svc.post
    options: { text: "saw {{.gate.value}} (first={{.write.first_time}}, n={{.write.writes}})" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "inv-77"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "saw inv-77 (first=true, n=1)" {
		t.Fatalf("calls: %+v", calls)
	}
	// The js write is durably in the shared store.
	v, found, err := st.Get("billing", "last-invoice")
	if err != nil || !found || v != "inv-77" {
		t.Fatalf("store after run: %v %v %v", v, found, err)
	}
	if ok, _ := st.Contains("billing", "history", "inv-77"); !ok {
		t.Fatal("ctx.kv.append not visible in the store")
	}
}

// TestKVVerbValidation: uses: kv.* steps validate like any connector verb —
// unknown options and missing required keys fail at load.
func TestKVVerbValidation(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { key: k } } ]
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("kv verb must validate: %v", err)
	}

	cfg = loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { key: k, bogus: 1 } } ]
`)
	reg = buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err == nil || !strings.Contains(err.Error(), `"bogus"`) {
		t.Fatalf("unknown kv option: %v", err)
	}

	cfg = loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - on: svc.ping
    steps: [ { uses: kv.set, options: { key: k } } ]
`)
	reg = buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("missing required kv option: %v", err)
	}
}
