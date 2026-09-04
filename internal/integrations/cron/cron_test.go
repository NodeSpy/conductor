package cron

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func build(t *testing.T, cfg Config) *Integration {
	t.Helper()
	ig, err := newIntegration("chores", func(v any) error { *(v.(*Config)) = cfg; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return ig.(*Integration)
}

func TestValidate(t *testing.T) {
	ok := build(t, Config{Schedules: []Schedule{
		{Name: "nightly", Cron: "0 2 * * *", Action: config.Action{Type: "command", Command: []string{"echo"}}},
		{Name: "hourly", Every: config.Duration(time.Hour), Action: config.Action{Type: "agent", Agent: "r"}},
	}})
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	bad := build(t, Config{Schedules: []Schedule{{Name: "x", Cron: "not a cron", Action: config.Action{Type: "command"}}}})
	if err := bad.Validate(); err == nil {
		t.Fatal("bad cron spec should fail validation")
	}

	noAction := build(t, Config{Schedules: []Schedule{{Name: "x", Every: config.Duration(time.Minute)}}})
	if err := noAction.Validate(); err == nil {
		t.Fatal("missing action.type should fail validation")
	}

	dup := build(t, Config{Schedules: []Schedule{
		{Name: "a", Every: config.Duration(time.Minute), Action: config.Action{Type: "command"}},
		{Name: "a", Every: config.Duration(time.Minute), Action: config.Action{Type: "command"}},
	}})
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate schedule name should fail validation")
	}
}

func TestSpecFromEvery(t *testing.T) {
	s := Schedule{Every: config.Duration(6 * time.Hour)}
	if s.spec() != "@every 6h0m0s" {
		t.Fatalf("every→spec wrong: %q", s.spec())
	}
	s2 := Schedule{Cron: "@daily"}
	if s2.spec() != "@daily" {
		t.Fatalf("cron passthrough wrong: %q", s2.spec())
	}
}

func TestTriggerCarriesAction(t *testing.T) {
	act := config.Action{Type: "command", Backend: "local", Command: []string{"gh", "api", "rate_limit"}}
	g := build(t, Config{Schedules: []Schedule{{Name: "ratelimit", Every: config.Duration(time.Hour), Action: act}}})
	tr := g.trigger(g.cfg.Schedules[0])
	if tr.Source != "cron" || tr.Instance != "chores" || tr.Kind != "ratelimit" {
		t.Fatalf("trigger fields wrong: %+v", tr)
	}
	if tr.Dedup != "" {
		t.Fatal("cron triggers must not dedup (empty signature)")
	}
	got, ok := tr.Action.(config.Action)
	if !ok || got.Type != "command" || got.Command[2] != "rate_limit" {
		t.Fatalf("action not attached: %+v", tr.Action)
	}
}

func TestRunOnStartEmits(t *testing.T) {
	g := build(t, Config{Schedules: []Schedule{
		{Name: "boot", Cron: "0 0 1 1 *", RunOnStart: true, // Jan 1st — won't fire during the test
			Action: config.Action{Type: "command", Command: []string{"echo"}}},
	}})
	var got []core.Trigger
	emit := func(_ context.Context, tr core.Trigger) { got = append(got, tr) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = g.Start(ctx, emit); close(done) }()

	// run_on_start fires synchronously before the scheduler loop; give it a beat.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if len(got) != 1 || got[0].Kind != "boot" {
		t.Fatalf("run_on_start should emit exactly the boot trigger, got %+v", got)
	}
}

func TestNameActionsAndDecodeError(t *testing.T) {
	g := build(t, Config{Schedules: []Schedule{{Name: "s", Every: config.Duration(time.Hour),
		Action: config.Action{Type: "command", Command: []string{"x"}}}}})
	if g.Name() != "chores" {
		t.Fatalf("Name = %q", g.Name())
	}
	refs := g.Actions()
	if len(refs) != 1 || !strings.Contains(refs[0].Where, `schedule "s"`) {
		t.Fatalf("Actions: %+v", refs)
	}
	if _, err := newIntegration("bad", func(any) error { return errors.New("boom") }); err == nil {
		t.Fatal("decode error must surface")
	}
}
