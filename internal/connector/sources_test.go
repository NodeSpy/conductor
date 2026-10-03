package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
	"gopkg.in/yaml.v3"
	"time"
)

// specOn builds a minimal TriggerSpec for `on: <conn>.<event>`.
func specOn(t *testing.T, y string) config.TriggerSpec {
	t.Helper()
	var s config.TriggerSpec
	if err := yaml.Unmarshal([]byte(y), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// ---------------------------------------------------------------------------
// cron
// ---------------------------------------------------------------------------

func TestCronConnector(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  timer:
    use: cron
    schedules:
      tick:    { every: 1h }
      nightly: { cron: "0 4 * * *", run_on_start: true }
`)
	in, ok := reg.Get("timer")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("timer should build enabled, got %+v", in)
	}
	if err := in.Impl.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// cron speaks the contract in process: its schedule names are dynamic
	// events it validates itself (plugin.validate), like any plugin's.
	if got := in.Impl.DeclaredEvents(); got != nil {
		t.Fatalf("DeclaredEvents = %v, want nil (validated by the plugin)", got)
	}

	// Source: one trigger per schedule lowers cleanly.
	src, err := in.Impl.Source([]CompiledTrigger{
		{Index: 0, Spec: specOn(t, "on: timer.tick\nsteps: [{id: s, type: command, command: [x]}]")},
		{Index: 1, Spec: specOn(t, "on: timer.nightly\nsteps: [{id: s, type: command, command: [x]}]")},
	})
	if err != nil || src == nil {
		t.Fatalf("Source: %v (src=%v)", err, src)
	}
	if err := src.Validate(); err != nil {
		t.Fatalf("lowered cron integration invalid: %v", err)
	}

	// No triggers → no source.
	if src, err := in.Impl.Source(nil); err != nil || src != nil {
		t.Fatalf("empty Source should be nil, got %v, %v", src, err)
	}
	// Unknown schedule names the declared set (the plugin's own checks).
	bad, _ := in.Impl.Source([]CompiledTrigger{{Spec: specOn(t, "on: timer.hourly")}})
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), `unknown cron schedule "hourly"`) || !strings.Contains(err.Error(), "nightly, tick") {
		t.Fatalf("unknown schedule: %v", err)
	}
	// A second trigger on one schedule is rejected.
	dup, _ := in.Impl.Source([]CompiledTrigger{
		{Spec: specOn(t, "on: timer.tick")}, {Index: 1, Spec: specOn(t, "on: timer.tick")},
	})
	if err := dup.Validate(); err == nil || !strings.Contains(err.Error(), "one trigger per cron schedule") {
		t.Fatalf("duplicate schedule: %v", err)
	}
	// No verbs.
	if _, err := in.Impl.Invoke(context.Background(), "run", nil); err == nil || !strings.Contains(err.Error(), "no verbs") {
		t.Fatalf("Invoke should refuse: %v", err)
	}
}

// ---------------------------------------------------------------------------
// rss
// ---------------------------------------------------------------------------

func TestRSSConnector(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  news:
    use: rss
    feeds:
      rel:  { url: "https://example.com/releases.atom", interval: 30m }
      blog: { url: "https://example.com/blog.rss" }
`)
	in, ok := reg.Get("news")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("news should build enabled, got %+v", in)
	}
	if err := in.Impl.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// rss speaks the contract in process: its feed names are dynamic events
	// it validates itself.
	if got := in.Impl.DeclaredEvents(); got != nil {
		t.Fatalf("DeclaredEvents = %v, want nil (validated by the plugin)", got)
	}

	// Two triggers on one feed coexist (variants), plus one on another feed.
	src, err := in.Impl.Source([]CompiledTrigger{
		{Index: 0, Spec: specOn(t, "on: news.rel")},
		{Index: 1, Spec: specOn(t, "on: news.rel\nname: second")},
		{Index: 2, Spec: specOn(t, "on: news.blog")},
	})
	if err != nil || src == nil {
		t.Fatalf("Source: %v (src=%v)", err, src)
	}
	if err := src.Validate(); err != nil {
		t.Fatalf("lowered rss integration invalid: %v", err)
	}
	bad, _ := in.Impl.Source([]CompiledTrigger{{Spec: specOn(t, "on: news.nope")}})
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), `unknown rss feed "nope"`) {
		t.Fatalf("unknown feed: %v", err)
	}
	if _, err := in.Impl.Invoke(context.Background(), "post", nil); err == nil || !strings.Contains(err.Error(), "no verbs") {
		t.Fatalf("Invoke should refuse: %v", err)
	}
}

// cron fires through the contract like any plugin source: run_on_start emits
// at once, routed to its trigger, with the facts and title it always had.
func TestCronFiresOverTheContract(t *testing.T) {
	reg := buildSinkRegistry(t, `
connectors:
  timer:
    use: cron
    schedules:
      nightly: { cron: "0 4 * * *", run_on_start: true }
`)
	in, _ := reg.Get("timer")
	src, err := in.Impl.Source([]CompiledTrigger{{Index: 0, Spec: specOn(t, "on: timer.nightly\nname: n\nsteps: [{id: s, type: command, command: [x]}]")}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan core.Trigger, 1)
	go func() { _ = src.Start(ctx, func(_ context.Context, tr core.Trigger) { got <- tr }) }()
	select {
	case tr := <-got:
		if tr.Source != "cron" || tr.Instance != "timer" || tr.Kind != "nightly" || tr.Title != "cron: timer/nightly" ||
			tr.Context["schedule"] != "nightly" || tr.Variant != "n" || tr.TargetTrusted {
			t.Fatalf("cron trigger = %+v", tr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run_on_start never fired")
	}
}

// TestOptionHooksSkipsUnassignedTarget is the defensive proof for finding
// 4(c): OptionHook Args render over the trigger's own facts, exactly like
// target_args' option-defaulting — and like target_args, that is honored
// only for a target the PLATFORM assigned (t.TargetTrusted). For an event
// whose target was not platform-assigned (sender-controlled facts),
// OptionHooks must lower nothing at all, not merely skip the Args
// rendering — the same observable effect as the plugin never having
// declared option_hooks.
func TestOptionHooksSkipsUnassignedTarget(t *testing.T) {
	sem := &sdk.EventSemantics{
		OptionHooks: []sdk.OptionHook{{
			Option: "ack", At: "start", Verb: "react",
			Args: map[string]string{"channel": "{{.channel}}"},
		}},
	}
	opts := map[string]any{"ack": map[string]any{"emoji": "eyes"}}

	untrusted := core.Trigger{Instance: "chat", Kind: "message", Sem: sem,
		Context: map[string]any{"channel": "C1"}, TargetTrusted: false}
	if hooks := OptionHooks(untrusted, opts); hooks != nil {
		t.Fatalf("an unassigned target must lower NO option hooks, got %+v", hooks)
	}

	trusted := untrusted
	trusted.TargetTrusted = true
	hooks := OptionHooks(trusted, opts)
	if len(hooks) != 1 {
		t.Fatalf("an assigned target must lower the declared option hook, got %+v", hooks)
	}
	if hooks[0].Options["emoji"] != "eyes" || hooks[0].Options["channel"] != "C1" {
		t.Fatalf("lowered hook options = %+v", hooks[0].Options)
	}
}
