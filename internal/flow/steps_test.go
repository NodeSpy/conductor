package flow

import (
	"testing"
)

// TestStepsScopingAndChaining covers item 1: a verb step reads trigger
// context via {{.msg}}; a second verb step reads the first's output via
// {{.a.id}} (and via the legacy {{.steps.a.outputs.id}} spelling).
func TestStepsScopingAndChaining(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    type: fake
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: a
    uses: svc.post
    options:
      text: "hello {{.msg}}"
  - id: b
    uses: svc.post
    options:
      text: "id-was-{{.a.id}}"
  - id: c
    uses: svc.post
    options:
      text: "legacy-{{.steps.a.outputs.id}}"
`)

	st.outputs["post"] = map[string]any{"id": 42}

	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "world"})

	runTrigger(rig, trig, spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}

	calls := st.snapshot()
	if len(calls) != 3 {
		t.Fatalf("expected 3 invocations, got %d", len(calls))
	}
	if got := calls[0].Opts["text"]; got != "hello world" {
		t.Errorf("step a text = %v, want %q", got, "hello world")
	}
	if got := calls[1].Opts["text"]; got != "id-was-42" {
		t.Errorf("step b text = %v, want %q", got, "id-was-42")
	}
	if got := calls[2].Opts["text"]; got != "legacy-42" {
		t.Errorf("step c text = %v, want %q", got, "legacy-42")
	}

	// step outputs are recorded in the audit log for the "step" event.
	stepAudits := rig.Store.auditsWithEvent("step")
	if len(stepAudits) != 3 {
		t.Fatalf("expected 3 step audit entries, got %d: %+v", len(stepAudits), stepAudits)
	}
}

// TestOptionsMerge covers item 2: connector DefaultOptions merge under
// step options (step wins on conflicts), including nested-map merging.
func TestOptionsMerge(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    type: fake
    options:
      channel: C
      as: me
      meta:
        region: us
        level: 1
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: a
    uses: svc.post
    options:
      text: T
      as: override-me
      meta:
        level: 2
`)

	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "x"})

	runTrigger(rig, trig, spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}

	calls := st.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(calls))
	}
	opts := calls[0].Opts
	if opts["channel"] != "C" {
		t.Errorf("channel = %v, want default C to pass through", opts["channel"])
	}
	if opts["text"] != "T" {
		t.Errorf("text = %v, want T", opts["text"])
	}
	if opts["as"] != "override-me" {
		t.Errorf("as = %v, want step override %q", opts["as"], "override-me")
	}
	meta, ok := opts["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta not a map: %#v", opts["meta"])
	}
	if meta["region"] != "us" {
		t.Errorf("meta.region = %v, want default 'us' preserved through nested merge", meta["region"])
	}
	if meta["level"] != 2 {
		t.Errorf("meta.level = %v, want step override 2", meta["level"])
	}
}
