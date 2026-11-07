package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// A recorded run names its trigger by FlowRef "<index>:<on>", the trigger's
// position in the flattened trigger list when it ran. These tests record a
// run against one config, then change that config the ways a live one
// drifts (another pack added before it, other triggers added and removed,
// the run's own pack upgraded) and retry it.

// pingPackV1 is a synthetic pack: one trigger, two steps, the second reading
// the first's output.
const pingPackV1 = `
pack:
  name: pinger
  version: 1.0.0
  requires:
    connectors: [enginegate]
triggers:
  - name: on_ping
    on: enginegate.ping
    steps:
      - { id: first,  uses: enginegate.post, options: { text: "one" } }
      - { id: second, uses: enginegate.post, options: { text: "got {{.first.token}}" } }
`

// pingPackV2 is pingPackV1 upgraded: a new step between the two, and the
// failing step's text changed.
const pingPackV2 = `
pack:
  name: pinger
  version: 2.0.0
  requires:
    connectors: [enginegate]
triggers:
  - name: on_ping
    on: enginegate.ping
    steps:
      - { id: first,  uses: enginegate.post, options: { text: "one" } }
      - { id: second, uses: enginegate.post, options: { text: "v2 got {{.first.token}}" } }
      - { id: third,  uses: enginegate.post, options: { text: "v2 third" } }
`

// pingPackV4 drops the step BEFORE the one the recorded run failed at, so
// that step now sits at another index.
const pingPackV4 = `
pack:
  name: pinger
  version: 4.0.0
  requires:
    connectors: [enginegate]
triggers:
  - name: on_ping
    on: enginegate.ping
    steps:
      - { id: second, uses: enginegate.post, options: { text: "v4 second" } }
      - { id: third,  uses: enginegate.post, options: { text: "v4 third" } }
`

// pingPackV3Hooked is pingPackV3 with a workflow start hook.
const pingPackV3Hooked = `
pack:
  name: pinger
  version: 3.0.1
  requires:
    connectors: [enginegate]
triggers:
  - name: on_ping
    on: enginegate.ping
    hooks:
      - { at: start, uses: enginegate.post, options: { text: "v3 start" } }
    steps:
      - { id: first,   uses: enginegate.post, options: { text: "one" } }
      - { id: renamed, uses: enginegate.post, options: { text: "v3 renamed" } }
`

// pingPackV3 drops the step the recorded run failed at.
const pingPackV3 = `
pack:
  name: pinger
  version: 3.0.0
  requires:
    connectors: [enginegate]
triggers:
  - name: on_ping
    on: enginegate.ping
    steps:
      - { id: first,   uses: enginegate.post, options: { text: "one" } }
      - { id: renamed, uses: enginegate.post, options: { text: "v3 renamed" } }
`

// otherPack is an unrelated pack whose instance sorts before "ping", with a
// trigger on the same event.
const otherPack = `
pack:
  name: other
  version: 1.0.0
  requires:
    connectors: [enginegate]
triggers:
  - name: also_ping
    on: enginegate.ping
    steps:
      - { id: x, uses: enginegate.post, options: { text: "other pack" } }
`

// driftConfig writes a consumer config (user triggers + pack instances) and
// loads it through the real pack loader, as the daemon boots.
func driftConfig(t *testing.T, userTriggers string, packs map[string]string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("connectors:\n  eg: { use: enginegate }\n")
	if userTriggers != "" {
		b.WriteString("triggers:\n" + userTriggers)
	}
	b.WriteString("packs:\n")
	for inst, manifest := range packs {
		writeDriftPack(t, dir, inst, manifest)
		b.WriteString("  " + inst + ":\n    source: ./src/" + inst + "\n    connectors: { enginegate: eg }\n    triggers:\n      \"*\": { enabled: true }\n")
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolvePacks(path); err != nil {
		t.Fatalf("resolve packs: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func writeDriftPack(t *testing.T, dir, inst, manifest string) {
	t.Helper()
	d := filepath.Join(dir, "src", inst)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, config.PackManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// engineOver builds an engine + real flow runner over a loaded config.
func engineOver(t *testing.T, cfg *config.Config) (*Engine, *flowGateStore) {
	t.Helper()
	registerGateConn()
	reg, err := connector.Build(cfg, connector.Deps{Secrets: secrets.New(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.Validate(cfg, reg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	st := newFlowGateStore()
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	return New(Options{Config: cfg, Store: st, Dispatch: fakeFlowDispatcher{},
		Notifier: notif, Flow: runner, Connectors: reg}), st
}

// refOf is the FlowRef the lowering stamps on a trigger's events in cfg.
func refOf(t *testing.T, cfg *config.Config, name string) (string, config.TriggerSpec) {
	t.Helper()
	for i, s := range cfg.Triggers {
		if s.Name == name {
			return connector.CompiledTrigger{Index: i, Spec: s}.Ref(), s
		}
	}
	t.Fatalf("no trigger %q in %+v", name, cfg.Triggers)
	return "", config.TriggerSpec{}
}

// recordAgainst builds the history record of a run of the pack's on_ping
// under cfg — as the lowering and the flow runner would have written it:
// step first succeeded with an output, step second failed.
func recordAgainst(t *testing.T, cfg *config.Config, id string) store.RunHistory {
	t.Helper()
	ref, spec := refOf(t, cfg, "ping/on_ping")
	trig := flowTrigger("d1")
	trig.Action = nil
	trigRaw, err := json.Marshal(trig)
	if err != nil {
		t.Fatal(err)
	}
	actRaw, err := json.Marshal(config.Action{Name: spec.Name, FlowRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	return store.RunHistory{
		ID: id, RunID: "ping:acme/x#1", Kind: "ping", Variant: spec.Name, On: spec.On,
		Def:  config.StepsFingerprint(spec.Steps),
		Repo: "acme/x", Number: 1, Started: time.Now().Add(-24 * time.Hour),
		Status: "failed", Error: `step "second": boom`, FailedStep: "second",
		Trigger: trigRaw, Action: actRaw,
		Steps: []store.StepRecord{
			{ID: "first", Index: 0, Status: "ok", Outputs: map[string]any{"token": "pinned-42"}},
			{ID: "second", Index: 1, Status: "failed", Error: "boom"},
		},
	}
}

func gateTexts(calls []map[string]any) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		s, _ := c["text"].(string)
		out = append(out, s)
	}
	return out
}

// waitRetryDone waits for the retry's own history record to end.
func waitRetryDone(t *testing.T, st *flowGateStore, of string) store.RunHistory {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		for id, r := range st.history {
			if id != of && r.RetryOf == of && r.Status != "running" {
				st.mu.Unlock()
				return r
			}
		}
		st.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("retry of %s never finished", of)
	return store.RunHistory{}
}

// The live symptom: the run's trigger is still configured, unchanged, but
// other triggers now sit before it — a pack added, user triggers added —
// so its recorded index points elsewhere. The retry still resolves it.
func TestRetryResolvesTriggerAfterUnrelatedConfigChanges(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "rdrift")
	if ref, _ := refOf(t, before, "ping/on_ping"); ref != "0:eg.ping" {
		t.Fatalf("fixture: the recorded trigger must start at index 0, got %s", ref)
	}

	after := driftConfig(t, `  - name: mine
    on: eg.ping
    steps: [ { id: m, uses: eg.post, options: { text: "mine" } } ]
  - on: eg.ping
    steps: [ { id: u, uses: eg.post, options: { text: "unnamed" } } ]
`, map[string]string{"ping": pingPackV1, "aaa": otherPack})
	if ref, _ := refOf(t, after, "ping/on_ping"); ref == "0:eg.ping" {
		t.Fatalf("fixture: the config change must move the trigger, still at %s", ref)
	}
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)

	msg, err := eng.RetryRunByID(context.Background(), "rdrift", "", false)
	if err != nil {
		t.Fatalf("retry after unrelated config changes: %v", err)
	}
	if !strings.Contains(msg, "step second") || strings.Contains(msg, "changed") {
		t.Fatalf("ack: %q", msg)
	}
	// The run's OWN trigger ran: only its failed step, against the pinned
	// output — not whatever trigger now sits at the recorded index.
	if got := gateTexts(waitGateCalls(t, 1)); len(got) != 1 || got[0] != "got pinned-42" {
		t.Fatalf("retried calls: %q", got)
	}
	r := waitRetryDone(t, st, "rdrift")
	if r.Status != "ok" || r.Variant != "ping/on_ping" {
		t.Fatalf("retry record: status %s variant %q", r.Status, r.Variant)
	}
	// The persisted run carries the trigger's CURRENT ref, so a resume of
	// the retry (and its run caps) key off where the trigger is now.
	var act config.Action
	if err := json.Unmarshal(r.Action, &act); err != nil {
		t.Fatal(err)
	}
	if want, _ := refOf(t, after, "ping/on_ping"); act.FlowRef != want {
		t.Fatalf("retry action ref = %q, want the current %q", act.FlowRef, want)
	}
	// The retry's own record fingerprints the definition it ran, for the
	// next retry to compare against.
	if _, spec := refOf(t, after, "ping/on_ping"); r.Def != config.StepsFingerprint(spec.Steps) {
		t.Fatalf("retry record def = %q", r.Def)
	}
}

// Removing OTHER triggers moves the recorded trigger the other way.
func TestRetryResolvesTriggerAfterOtherTriggersRemoved(t *testing.T) {
	before := driftConfig(t, `  - name: mine
    on: eg.ping
    steps: [ { id: m, uses: eg.post, options: { text: "mine" } } ]
`, map[string]string{"ping": pingPackV1, "aaa": otherPack})
	rec := recordAgainst(t, before, "rshrink")
	after := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)
	if _, err := eng.RetryRunByID(context.Background(), "rshrink", "", false); err != nil {
		t.Fatalf("retry after other triggers were removed: %v", err)
	}
	if got := gateTexts(waitGateCalls(t, 1)); got[0] != "got pinned-42" {
		t.Fatalf("retried calls: %q", got)
	}
}

// The trigger itself gone: only then does the retry say so.
func TestRetryRefusesWhenTheTriggerIsReallyGone(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "rgone")
	// Another trigger on the same event now sits at the recorded index —
	// the old positional check took it for the run's own.
	after := driftConfig(t, "", map[string]string{"aaa": otherPack})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)
	_, err := eng.RetryRunByID(context.Background(), "rgone", "", false)
	if err == nil || !strings.Contains(err.Error(), "no longer in the config") || !strings.Contains(err.Error(), "ping/on_ping") {
		t.Fatalf("a removed trigger must be reported gone, by name: %v", err)
	}
	if gateCalls() != 0 {
		t.Fatal("nothing may run for a trigger that is gone")
	}
}

// The run's own pack upgraded (and moved): the retry runs the CURRENT
// definition from the recorded step, and says the definition changed.
func TestRetryAfterPackUpgradeRunsCurrentDefinition(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "rupg")
	after := driftConfig(t, "", map[string]string{"ping": pingPackV2, "aaa": otherPack})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)

	msg, err := eng.RetryRunByID(context.Background(), "rupg", "", false)
	if err != nil {
		t.Fatalf("retry after a pack upgrade: %v", err)
	}
	if !strings.Contains(msg, "step second") || !strings.Contains(msg, "steps changed") || !strings.Contains(msg, "current definition") {
		t.Fatalf("ack must say the definition changed: %q", msg)
	}
	got := gateTexts(waitGateCalls(t, 2))
	if len(got) != 2 || got[0] != "v2 got pinned-42" || got[1] != "v2 third" {
		t.Fatalf("the current definition must run from the recorded step: %q", got)
	}
}

// An older record carries no definition fingerprint: a step that moved is
// still read as a changed definition.
func TestRetryDetectsMovedStepsWithoutFingerprint(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "rold")
	rec.Def = ""
	moved := strings.Replace(pingPackV2, "      - { id: first,", "      - { id: zero, uses: enginegate.post, options: { text: \"zero\" } }\n      - { id: first,", 1)
	after := driftConfig(t, "", map[string]string{"ping": moved})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)
	// "zero" never ran: starting at "second" would skip it, so the retry
	// falls back to the top — which replays "first" and needs consent.
	if _, err := eng.RetryRunByID(context.Background(), "rold", "", false); err == nil ||
		!strings.Contains(err.Error(), "--force-replay") || !strings.Contains(err.Error(), "first") {
		t.Fatalf("a changed run-up must not skip a never-run step silently: %v", err)
	}
	if gateCalls() != 0 {
		t.Fatal("a refused retry ran something")
	}
	// Naming the step explicitly does not skip "zero" either.
	if _, err := eng.RetryRunByID(context.Background(), "rold", "second", true); err == nil ||
		!strings.Contains(err.Error(), "skip steps it never ran") {
		t.Fatalf("an explicit step past a never-run one: %v", err)
	}
	msg, err := eng.RetryRunByID(context.Background(), "rold", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "the beginning") || !strings.Contains(msg, "steps changed") {
		t.Fatalf("ack: %q", msg)
	}
	if got := gateTexts(waitGateCalls(t, 4)); got[0] != "zero" || got[1] != "one" {
		t.Fatalf("from the top on the current definition: %q", got)
	}
}

// The recorded failed step no longer exists after the upgrade: the retry
// falls back to the top (with consent, as it replays succeeded steps).
func TestRetryFallsBackToTopWhenRecordedStepIsGone(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "rrm")
	after := driftConfig(t, "", map[string]string{"ping": pingPackV3})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)

	_, err := eng.RetryRunByID(context.Background(), "rrm", "", false)
	if err == nil || !strings.Contains(err.Error(), `step "second" is no longer in the trigger`) || !strings.Contains(err.Error(), "--force-replay") {
		t.Fatalf("falling back over a succeeded step needs consent: %v", err)
	}
	msg, err := eng.RetryRunByID(context.Background(), "rrm", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "from the beginning") || !strings.Contains(msg, `"second"`) {
		t.Fatalf("ack must say why it starts over: %q", msg)
	}
	if got := gateTexts(waitGateCalls(t, 2)); got[0] != "one" || got[1] != "v3 renamed" {
		t.Fatalf("the current definition from the top: %q", got)
	}

	// An explicit step the current definition lacks is an error, listing
	// the steps it has now.
	if _, err := eng.RetryRunByID(context.Background(), "rrm", "second", true); err == nil ||
		!strings.Contains(err.Error(), `no step "second"`) || !strings.Contains(err.Error(), "renamed") {
		t.Fatalf("explicit missing step: %v", err)
	}
}

// A run with nothing succeeded falls back to the top without consent.
func TestRetryFallbackWithNothingSucceededNeedsNoForce(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "rnone")
	rec.Steps = rec.Steps[1:]
	rec.Steps[0].Index = 0
	rec.FailedStep = "second"
	after := driftConfig(t, "", map[string]string{"ping": pingPackV3})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)
	if _, err := eng.RetryRunByID(context.Background(), "rnone", "", false); err != nil {
		t.Fatal(err)
	}
	waitGateCalls(t, 2)
}

// Resume after a restart is the same lookup: a run in flight when the
// daemon stopped resumes onto its own trigger after the config moved it.
func TestResumeFindsMovedTrigger(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	ref, spec := refOf(t, before, "ping/on_ping")
	after := driftConfig(t, "", map[string]string{"ping": pingPackV1, "aaa": otherPack})
	eng, st := engineOver(t, after)
	resetGateCalls()

	trig := flowTrigger("d1")
	act := config.Action{Name: spec.Name, FlowRef: ref}
	trig.Action = act
	run := flowRunRecord(trig)
	run.StepIndex = 1
	run.Outputs = map[string]map[string]any{"first": {"token": "pinned-7"}}
	run.StartHooksFired = true
	trig.Action = nil
	eng.resumeFlowRun(context.Background(), run, trig, act)

	if got := gateTexts(waitGateCalls(t, 1)); len(got) != 1 || got[0] != "got pinned-7" {
		t.Fatalf("resume must continue its own trigger from its checkpoint: %q", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, a := range st.audits {
		if a["event"] == "resume_restarted" {
			t.Fatalf("an unchanged definition must resume in place: %+v", a)
		}
	}
}

// A resume whose checkpoint no longer fits the trigger's steps (the pack
// upgraded while the daemon was down) starts over on the current definition
// instead of skipping steps or running past the end.
func TestResumeRestartsWhenCheckpointNoLongerFits(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	ref, spec := refOf(t, before, "ping/on_ping")
	after := driftConfig(t, "", map[string]string{"ping": pingPackV3Hooked, "aaa": otherPack})
	eng, st := engineOver(t, after)
	resetGateCalls()

	trig := flowTrigger("d1")
	act := config.Action{Name: spec.Name, FlowRef: ref}
	trig.Action = act
	run := flowRunRecord(trig)
	// v1 checkpointed past "first" and "second" (both with outputs); v3 has
	// no "second".
	run.StepIndex = 2
	run.Outputs = map[string]map[string]any{"first": {"token": "a"}, "second": {"token": "b"}}
	run.StartHooksFired = true
	trig.Action = nil
	eng.resumeFlowRun(context.Background(), run, trig, act)

	// A fresh attempt: its start hook fires again, then every current step.
	if got := gateTexts(waitGateCalls(t, 3)); got[0] != "v3 start" || got[1] != "one" || got[2] != "v3 renamed" {
		t.Fatalf("a misfit checkpoint restarts on the current steps: %q", got)
	}
	// The resumed run's record carries the trigger's current ref.
	var rec store.RunHistory
	deadline := time.Now().Add(3 * time.Second)
	for rec.ID == "" && time.Now().Before(deadline) {
		st.mu.Lock()
		for _, h := range st.history {
			if h.Status != "running" {
				rec = h
			}
		}
		st.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	var got config.Action
	_ = json.Unmarshal(rec.Action, &got)
	if want, _ := refOf(t, after, "ping/on_ping"); got.FlowRef != want {
		t.Fatalf("resumed run's action ref = %q, want the current %q", got.FlowRef, want)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, a := range st.audits {
		if a["event"] == "resume_restarted" {
			return
		}
	}
	t.Fatal("a restarted resume must be audited")
}

func TestCheckpointFits(t *testing.T) {
	steps := []config.Step{{ID: "a"}, {ID: "b"}, {}}
	for _, c := range []struct {
		name string
		run  store.WorkflowRun
		want bool
	}{
		{"fresh", store.WorkflowRun{}, true},
		{"mid", store.WorkflowRun{StepIndex: 1, Outputs: map[string]map[string]any{"a": {}}}, true},
		{"all done", store.WorkflowRun{StepIndex: 3, Outputs: map[string]map[string]any{"a": {}, "step3": {}}}, true},
		{"past the end", store.WorkflowRun{StepIndex: 4}, false},
		{"negative", store.WorkflowRun{StepIndex: -1}, false},
		{"output of a removed step", store.WorkflowRun{StepIndex: 1, Outputs: map[string]map[string]any{"gone": {}}}, false},
		{"output of a step now after the index", store.WorkflowRun{StepIndex: 1, Outputs: map[string]map[string]any{"b": {}}}, false},
		{"output at the index", store.WorkflowRun{StepIndex: 2, Outputs: map[string]map[string]any{"step3": {}}}, false},
	} {
		if got := checkpointFits(c.run, steps); got != c.want {
			t.Errorf("%s: checkpointFits = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPrefixDoneAndDefChanged(t *testing.T) {
	steps := []config.Step{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	rec := store.RunHistory{Steps: []store.StepRecord{
		{ID: "a", Index: 0, Status: "skipped"},
		{ID: "b", Index: 1, Status: "failed", ContinuedOnError: true},
		{ID: "c", Index: 2, Status: "failed"},
	}}
	// The run-up to i is done while every step before i is ok, skipped or
	// continued past; "c" failed.
	for i, want := range []bool{true, true, true, false} {
		if got := prefixDone(rec, steps, i); got != want {
			t.Errorf("prefixDone(%d) = %v, want %v", i, got, want)
		}
	}
	if prefixDone(rec, []config.Step{{ID: "new"}, {ID: "c"}}, 1) {
		t.Error("a step the record never ran is not done")
	}
	if defChanged(rec, steps) {
		t.Error("same ids at the same positions, no fingerprint: unchanged")
	}
	if !defChanged(rec, []config.Step{{ID: "b"}, {ID: "a"}, {ID: "c"}}) {
		t.Error("reordered steps: changed")
	}
	if !defChanged(rec, steps[:2]) {
		t.Error("a recorded step removed: changed")
	}
	rec.Def = config.StepsFingerprint(steps)
	if defChanged(rec, steps) {
		t.Error("matching fingerprint: unchanged")
	}
	edited := append([]config.Step{}, steps...)
	edited[1].Uses = "x.y"
	if !defChanged(rec, edited) {
		t.Error("same ids, edited step: the fingerprint says changed")
	}
}

// A run queued for a slot when the daemon stopped re-queues onto its own
// trigger after the config moved it, re-keyed under the current ref.
func TestRequeueFindsMovedTrigger(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	ref, spec := refOf(t, before, "ping/on_ping")
	after := driftConfig(t, "", map[string]string{"ping": pingPackV1, "aaa": otherPack})
	eng, st := engineOver(t, after)
	resetGateCalls()

	trig := flowTrigger("d1")
	act := config.Action{Name: spec.Name, FlowRef: ref}
	trig.Action = act
	run := flowRunRecord(trig)
	run.ID = queuedRunID(trig)
	_ = st.PutRun(run)
	trig.Action = nil
	eng.requeueFlowRun(context.Background(), run, trig, act)

	if got := gateTexts(waitGateCalls(t, 2)); got[0] != "one" || !strings.HasPrefix(got[1], "got ") {
		t.Fatalf("the re-queued run must be its own trigger's: %q", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.runs[run.ID] {
		t.Fatalf("the record under the stale ref %s must be dropped (re-keyed)", run.ID)
	}
}

// The upgrade removed a step before the failed one: the failed step now
// sits at another index, and the retry starts at its CURRENT position.
func TestRetryStartsAtTheStepsCurrentPosition(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "rpos")
	after := driftConfig(t, "", map[string]string{"ping": pingPackV4})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)
	msg, err := eng.RetryRunByID(context.Background(), "rpos", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "step second") {
		t.Fatalf("ack: %q", msg)
	}
	if got := gateTexts(waitGateCalls(t, 2)); got[0] != "v4 second" || got[1] != "v4 third" {
		t.Fatalf("from the failed step's current position: %q", got)
	}
}

// A step the upgrade added after everything the run did is a valid explicit
// start: nothing before it is skipped.
func TestRetryFromAStepAddedSince(t *testing.T) {
	before := driftConfig(t, "", map[string]string{"ping": pingPackV1})
	rec := recordAgainst(t, before, "radd")
	rec.Steps[1].Status, rec.Steps[1].Error = "ok", ""
	rec.FailedStep, rec.Error = "", "a done hook failed"
	after := driftConfig(t, "", map[string]string{"ping": pingPackV2})
	eng, st := engineOver(t, after)
	resetGateCalls()
	_ = st.PutHistory(rec)
	if _, err := eng.RetryRunByID(context.Background(), "radd", "third", false); err != nil {
		t.Fatalf("a step added after what ran: %v", err)
	}
	if got := gateTexts(waitGateCalls(t, 1)); len(got) != 1 || got[0] != "v2 third" {
		t.Fatalf("only the added step runs: %q", got)
	}
}
