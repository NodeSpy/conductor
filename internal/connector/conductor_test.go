package connector

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// startConductorSource builds a registry from YAML, lowers the conductor
// triggers, starts the source with a capturing emit, and returns the capture
// plus a stop func.
func startConductorSource(t *testing.T, y string) (func() []core.Trigger, func()) {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	reg, err := Build(&cfg, Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	in, ok := reg.Get("conductor")
	if !ok {
		t.Fatal("conductor instance missing")
	}
	var trigs []CompiledTrigger
	for i, spec := range cfg.Triggers {
		if spec.Connector() == "conductor" {
			trigs = append(trigs, CompiledTrigger{Index: i, Spec: spec})
		}
	}
	src, err := in.Impl.Source(trigs)
	if err != nil || src == nil {
		t.Fatalf("source: %v %v", src, err)
	}
	if src.Name() != "conductor" || src.Validate() != nil {
		t.Fatal("source identity")
	}

	var mu sync.Mutex
	var got []core.Trigger
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = src.Start(ctx, func(_ context.Context, tr core.Trigger) {
			mu.Lock()
			got = append(got, tr)
			mu.Unlock()
		})
	}()
	// Start registers before blocking; wait until the emit is wired.
	deadline := time.Now().Add(2 * time.Second)
	for {
		lifecycleMu.Lock()
		ready := lifecycleEmit != nil
		lifecycleMu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source did not register")
		}
		time.Sleep(time.Millisecond)
	}
	capture := func() []core.Trigger {
		mu.Lock()
		defer mu.Unlock()
		return append([]core.Trigger(nil), got...)
	}
	return capture, func() { cancel(); <-done }
}

const conductorTriggerYAML = `
connectors:
  box: { type: command }
triggers:
  - name: alert
    on: [ conductor.escalate, conductor.needs_input ]
    steps: [ { uses: box.run, options: { command: "true" } } ]
  - name: on-update
    on: conductor.update_available
    steps: [ { uses: box.run, options: { command: "true" } } ]
`

// TestEmitLifecycleRoutesToTriggers: an event reaches every trigger listening
// for it (fan-in lists included), with the composed line and target context;
// other events don't fire it.
func TestEmitLifecycleRoutesToTriggers(t *testing.T) {
	capture, stop := startConductorSource(t, conductorTriggerYAML)
	defer stop()

	orig := core.Trigger{
		Source: "github", Instance: "gh", Kind: "merge_conflict",
		Target: core.Target{Repo: "o/r", Number: 7}, Title: "fix conflict",
	}
	EmitLifecycle(context.Background(), "escalate", orig, "[escalate] o/r#7 merge_conflict gave up", nil)
	got := capture()
	if len(got) != 1 {
		t.Fatalf("emitted %d triggers, want 1: %+v", len(got), got)
	}
	tr := got[0]
	if tr.Source != "conductor" || tr.Kind != "escalate" || tr.Variant != "alert" || !tr.Force {
		t.Fatalf("trigger identity: %+v", tr)
	}
	if tr.Target.Repo != "o/r" || tr.Target.Number != 7 {
		t.Fatalf("target carried: %+v", tr.Target)
	}
	if tr.Context["message"] != "[escalate] o/r#7 merge_conflict gave up" ||
		tr.Context["ref"] != "o/r#7" || tr.Context["origin_kind"] != "merge_conflict" {
		t.Fatalf("context: %+v", tr.Context)
	}
	act, isAct := tr.Action.(config.Action)
	if !isAct || act.FlowRef == "" || !strings.Contains(act.FlowRef, "conductor.escalate") {
		t.Fatalf("flow ref: %+v", tr.Action)
	}

	// The fan-in list's other event fires the same trigger.
	EmitLifecycle(context.Background(), "needs_input", orig, "line", nil)
	if got := capture(); len(got) != 2 || got[1].Kind != "needs_input" {
		t.Fatalf("needs_input: %+v", got)
	}

	// An event nothing listens for is a no-op.
	EmitLifecycle(context.Background(), "complete", orig, "line", nil)
	if got := capture(); len(got) != 2 {
		t.Fatalf("complete must not fire: %+v", got)
	}

	// Extra context (version) rides along; a target-less origin synthesizes.
	EmitLifecycle(context.Background(), "update_available", core.Trigger{Source: "updater", Kind: "update"},
		"release v1.2.3 available", map[string]any{"version": "v1.2.3"})
	got = capture()
	if len(got) != 3 {
		t.Fatalf("update_available: %+v", got)
	}
	if got[2].Context["version"] != "v1.2.3" || got[2].Variant != "on-update" {
		t.Fatalf("update context: %+v", got[2].Context)
	}
	if got[2].Target.Repo == "" {
		t.Fatalf("synthetic target: %+v", got[2].Target)
	}
}

// TestEmitLifecycleLoopGuard: events originating from a conductor.* run are
// NEVER re-fed — a notification workflow's own dispatch/complete/failed
// cannot storm.
func TestEmitLifecycleLoopGuard(t *testing.T) {
	capture, stop := startConductorSource(t, conductorTriggerYAML)
	defer stop()

	meta := core.Trigger{
		Source: "conductor", Instance: "conductor", Kind: "escalate",
		Target: core.Target{Repo: "o/r", Number: 7},
	}
	for _, ev := range []string{"dispatch", "escalate", "complete", "failed", "needs_input"} {
		EmitLifecycle(context.Background(), ev, meta, "meta-run event", nil)
	}
	if got := capture(); len(got) != 0 {
		t.Fatalf("loop guard breached: %+v", got)
	}
}

// TestEmitLifecycleNoSource: with no conductor triggers running, emission is
// a safe no-op.
func TestEmitLifecycleNoSource(t *testing.T) {
	lifecycleMu.Lock()
	lifecycleEmit, lifecycleTriggers = nil, nil
	lifecycleMu.Unlock()
	EmitLifecycle(context.Background(), "escalate", core.Trigger{Source: "github"}, "line", nil)
}

// TestConductorReservedAndSourceless: the connector name is reserved; a
// config with no conductor triggers lowers no source.
func TestConductorReservedAndSourceless(t *testing.T) {
	var cfg config.Config
	if err := yaml.Unmarshal([]byte("connectors:\n  conductor: { type: command }\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved name: %v", err)
	}
	src, err := (conductorImpl{}).Source(nil)
	if err != nil || src != nil {
		t.Fatalf("sourceless: %v %v", src, err)
	}
}

// TestConductorVerbsWithoutDaemon: outside the daemon (no ops wired) every
// verb errors clearly instead of pretending.
func TestConductorVerbsWithoutDaemon(t *testing.T) {
	SetConductorOps(nil)
	impl := conductorImpl{}
	for _, verb := range []string{"update", "pause", "resume", "restart", "reload", "run"} {
		_, err := impl.Invoke(context.Background(), verb, map[string]any{"name": "x"})
		if err == nil || !strings.Contains(err.Error(), "not available in this context") {
			t.Errorf("%s: %v", verb, err)
		}
	}
	if _, err := runSweepHook(context.Background()); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("sweep hook: %v", err)
	}
}
