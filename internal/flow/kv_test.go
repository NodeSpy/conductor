package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

// tempStore registers one boltdb store under name for a template-level test
// (no registry build involved).
func tempStore(t *testing.T, name string) kv.KVBackend {
	t.Helper()
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	b, err := kv.OpenBoltStore(name, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := kv.Register(name, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestKVTemplateFuncs: {{ kv "store" "ns" "key" }} composing with default on
// a missing key, and the read-only {{ kvContains "store" "ns" "key" item }}.
func TestKVTemplateFuncs(t *testing.T) {
	st := tempStore(t, "cache")
	if err := st.Set("runs", "42", float64(7), 0); err != nil {
		t.Fatal(err)
	}
	_, _ = st.Append("pd", "seen", []any{"i-1", "i-2"}, false)

	cases := []struct{ tmpl, want string }{
		{`{{ kv "cache" "runs" "42" }}`, "7"},
		{`{{ kv "cache" "runs" (print .pr) }}`, "7"},
		{`{{ kv "cache" "runs" "nope" | default 0 }}`, "0"},
		{`{{ kvContains "cache" "pd" "seen" "i-1" }}`, "true"},
		{`{{ kvContains "cache" "pd" "seen" "i-9" }}`, "false"},
		{`{{ kvContains "cache" "pd" "absent" "x" }}`, "false"},
		{`{{ if kvContains "cache" "pd" "seen" .id }}dup{{ end }}`, ""},
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

	// Wrong arity and an undefined store are render errors, not panics.
	if _, err := render(`{{ kv "cache" "k" }}`, nil); err == nil {
		t.Fatal("kv with two args must error (store, namespace, key)")
	}
	if _, err := render(`{{ kv "ghost" "ns" "k" }}`, nil); err == nil || !strings.Contains(err.Error(), `no store named "ghost"`) {
		t.Fatalf("undefined store: %v", err)
	}
	if _, err := render(`{{ kvContains "cache" "ns" "k" }}`, nil); err == nil {
		t.Fatal("kvContains with three args must error")
	}
}

// TestKVStepsAndCodeShareOneStore: a run: js step writes through
// ctx.store("main"), a kv.get verb step (store: main) reads the same key,
// and the value flows on into another verb — the access paths hit ONE
// defined store.
func TestKVStepsAndCodeShareOneStore(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
stores:
  main: { type: boltdb }
`)
	reg := buildRegistry(t, cfg) // buildStores registers "main"
	fake := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: write
    run: js
    code: |
      const kv = ctx.store("main");
      const prev = kv.get("billing", "last-invoice");
      kv.set("billing", "last-invoice", ctx.msg);
      const n = kv.incr("billing", "writes");
      kv.append("billing", "history", ctx.msg);
      return { first_time: !prev, writes: n };
  - id: gate
    uses: kv.get
    options: { store: main, namespace: billing, key: last-invoice }
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
	st, err := kv.Use("main")
	if err != nil {
		t.Fatal(err)
	}
	v, found, err := st.Get("billing", "last-invoice")
	if err != nil || !found || v != "inv-77" {
		t.Fatalf("store after run: %v %v %v", v, found, err)
	}
	if ok, _ := st.Contains("billing", "history", "inv-77"); !ok {
		t.Fatal("ctx.store append not visible in the store")
	}
}

// TestKVVerbValidation: uses: kv.* steps validate at LOAD — store: must be a
// literal defined store; options check like any verb; hooks too.
func TestKVVerbValidation(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	base := `
connectors:
  svc: { type: fake }
stores:
  main: { type: boltdb }
`
	valid := func(y string) error {
		cfg := loadConfig(t, base+y)
		reg := buildRegistry(t, cfg)
		return Validate(cfg, reg)
	}
	if err := valid(`
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { store: main, key: k } } ]
    hooks: [ { at: done, uses: kv.incr, options: { store: main, key: done-count } } ]
`); err != nil {
		t.Fatalf("kv verb + hook must validate: %v", err)
	}

	cases := []struct{ name, yaml, wantErr string }{
		{"missing store", `
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { key: k } } ]`, "require store:"},
		{"unknown store", `
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { store: ghost, key: k } } ]`, `unknown store "ghost"`},
		{"templated store", `
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { store: "{{.msg}}", key: k } } ]`, "literal store name"},
		{"hook missing store", `
triggers:
  - on: svc.ping
    steps: [ { uses: svc.post, options: { text: t } } ]
    hooks: [ { at: done, uses: kv.incr, options: { key: k } } ]`, "require store:"},
		{"unknown option still checked", `
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { store: main, key: k, bogus: 1 } } ]`, `"bogus"`},
		{"required option still checked", `
triggers:
  - on: svc.ping
    steps: [ { uses: kv.set, options: { store: main, key: k } } ]`, "value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := valid(tc.yaml); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}
