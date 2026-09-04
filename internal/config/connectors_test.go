package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeTestConfig writes body to a fresh temp directory and returns the path,
// mirroring config_test.go's inline os.WriteFile/t.TempDir pattern.
func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// 1. Dual-schema load
// ---------------------------------------------------------------------------

func TestLoadNewSchemaMinimal(t *testing.T) {
	path := writeTestConfig(t, `
connectors:
  gh:
    type: github
agents:
  fixer:
    provider: claude
triggers:
  - on: gh.new_comment
    steps:
      - id: respond
        type: agent
        agent: fixer
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.HasConnectors() {
		t.Fatal("a config carrying connectors/triggers should report HasConnectors() = true")
	}
}

func TestLoadLegacyOnlyStillLoads(t *testing.T) {
	path := writeTestConfig(t, `
integrations:
  - type: github
    name: acme
agents:
  fixer:
    provider: claude
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HasConnectors() {
		t.Fatal("a legacy-only config should report HasConnectors() = false")
	}
}

func TestLoadNeitherIntegrationsNorConnectorsErrors(t *testing.T) {
	path := writeTestConfig(t, "control: {}\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("a config with neither integrations nor connectors should fail to load")
	}
	if !strings.Contains(err.Error(), "no integrations or connectors") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "no integrations or connectors")
	}
}

// ---------------------------------------------------------------------------
// 2. Structural validation rejections — one case per error path in
// validateConnectors/validateSteps/validateStep/validateHooks.
//
// Note: an empty connector name ("" key under connectors:) is unreachable via
// normal YAML authoring in the same way the other cases are exercised here, so
// per instructions it is not covered as its own case.
// ---------------------------------------------------------------------------

func TestValidateConnectorsStructural(t *testing.T) {
	validCmdStep := Step{ID: "s1", Type: "command", Command: []string{"true"}}
	validTrigger := func(steps []Step, hooks []Hook) TriggerSpec {
		return TriggerSpec{On: "gh.event", Steps: steps, Hooks: hooks}
	}

	cases := []struct {
		name    string
		build   func() *Config
		wantErr string
	}{
		{
			name: "connector missing type",
			build: func() *Config {
				return &Config{ConnectorsMap: map[string]ConnectorRef{"gh": {}}}
			},
			wantErr: `connector "gh": missing type`,
		},
		{
			name: "runtime with both type and agent",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Runtimes:      map[string]RuntimeConfig{"r1": {Type: "paseo", Agent: "gemini"}},
				}
			},
			wantErr: `runtime "r1": set exactly one of`,
		},
		{
			name: "runtime with neither type nor agent",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Runtimes:      map[string]RuntimeConfig{"r1": {}},
				}
			},
			wantErr: `runtime "r1": set exactly one of`,
		},
		{
			name: "runtime unknown host",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Runtimes:      map[string]RuntimeConfig{"r1": {Type: "paseo", Host: "nope"}},
				}
			},
			wantErr: `runtime "r1": unknown host "nope"`,
		},
		{
			name: "more than one default across runtimes and controllers combined",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Runtimes:      map[string]RuntimeConfig{"r1": {Type: "paseo", Default: true}},
					Controllers:   map[string]ControllerConfig{"c1": {Type: "paseo", Default: true}},
				}
			},
			wantErr: "at most one runtime may set `default: true`",
		},
		{
			name: "same name in runtimes and controllers",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Runtimes:      map[string]RuntimeConfig{"dup": {Type: "paseo"}},
					Controllers:   map[string]ControllerConfig{"dup": {Type: "paseo"}},
				}
			},
			wantErr: `"dup" is defined under both runtimes: and controllers:`,
		},
		{
			name: "host missing address",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Hosts:         map[string]HostConfig{"h1": {}},
				}
			},
			wantErr: `host "h1": missing host address`,
		},
		{
			name: "trigger missing on",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{{}},
				}
			},
			wantErr: "missing `on:`",
		},
		{
			name: "on without dot",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{{On: "nodot"}},
				}
			},
			wantErr: "must be <connector>.<event>",
		},
		{
			name: "on references unknown connector",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{{On: "unknown.event", Steps: []Step{validCmdStep}}},
				}
			},
			wantErr: `unknown connector "unknown"`,
		},
		{
			name: "trigger with no steps",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{{On: "gh.event"}},
				}
			},
			wantErr: "no steps",
		},
		{
			name: "duplicate step ids",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{
						{ID: "a", Type: "command", Command: []string{"true"}},
						{ID: "a", Type: "command", Command: []string{"true"}},
					}, nil)},
				}
			},
			wantErr: `duplicate step id "a"`,
		},
		{
			name: "step with no form",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{validTrigger([]Step{{ID: "s1"}}, nil)},
				}
			},
			wantErr: "set one of `type: agent`",
		},
		{
			name: "step with two forms (uses + run)",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{validTrigger([]Step{{ID: "s1", Uses: "gh.verb", Run: "sh"}}, nil)},
				}
			},
			wantErr: "step forms are mutually exclusive",
		},
		{
			name: "uses without dot",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{validTrigger([]Step{{ID: "s1", Uses: "nodot"}}, nil)},
				}
			},
			wantErr: "must be <connector>.<verb>",
		},
		{
			name: "run without code",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{validTrigger([]Step{{ID: "s1", Run: "sh"}}, nil)},
				}
			},
			wantErr: "needs `code:`",
		},
		{
			name: "step with both host and ssh",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{{
						ID: "s1", Run: "sh", Code: "echo hi",
						Host: "h1", SSH: &HostConfig{Host: "1.2.3.4"},
					}}, nil)},
				}
			},
			wantErr: "not both",
		},
		{
			name: "step host references unknown hosts entry",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{{
						ID: "s1", Run: "sh", Code: "echo hi", Host: "nope",
					}}, nil)},
				}
			},
			wantErr: `unknown host "nope"`,
		},
		{
			name: "inline ssh missing host address",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{{
						ID: "s1", Run: "sh", Code: "echo hi", SSH: &HostConfig{},
					}}, nil)},
				}
			},
			wantErr: "inline ssh: needs `host:`",
		},
		{
			name: "run: js with host: is local-only",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Hosts:         map[string]HostConfig{"h1": {Host: "1.2.3.4"}},
					Triggers: []TriggerSpec{validTrigger([]Step{{
						ID: "s1", Run: "js", Code: "1+1", Host: "h1",
					}}, nil)},
				}
			},
			wantErr: "local-only",
		},
		{
			name: "run: go-embed with ssh: is local-only",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{{
						ID: "s1", Run: "go-embed", Code: "package main",
						SSH: &HostConfig{Host: "1.2.3.4"},
					}}, nil)},
				}
			},
			wantErr: "local-only",
		},
		{
			name: "use: references unknown workflow",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers:      []TriggerSpec{validTrigger([]Step{{ID: "s1", Use: "nope"}}, nil)},
				}
			},
			wantErr: `unknown workflow "nope"`,
		},
		{
			name: "hook with bad at:",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{validCmdStep}, []Hook{
						{At: "nope", Uses: "gh.verb"},
					})},
				}
			},
			wantErr: "`at:` must be start|done|fail",
		},
		{
			name: "hook without uses:",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{validCmdStep}, []Hook{
						{At: "start"},
					})},
				}
			},
			wantErr: "hooks are verb action units",
		},
		{
			name: "hook uses without dot",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{validCmdStep}, []Hook{
						{At: "start", Uses: "nodot"},
					})},
				}
			},
			wantErr: "must be <connector>.<verb>",
		},
		{
			name: "workflow with no steps",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Workflows:     map[string]WorkflowDef{"wf1": {}},
				}
			},
			wantErr: "workflow wf1: no steps",
		},
		{
			name: "workflow input with unknown type",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Workflows: map[string]WorkflowDef{"wf1": {
						Steps:  []Step{validCmdStep},
						Inputs: map[string]InputSpec{"x": {Type: "weird"}},
					}},
				}
			},
			wantErr: `input "x": unknown type "weird"`,
		},
		{
			name: "parallel branches combined with another form",
			build: func() *Config {
				return &Config{
					ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
					Triggers: []TriggerSpec{validTrigger([]Step{{
						ID: "s1", Uses: "gh.verb",
						Parallel: &ParallelSpec{Branches: [][]Step{{
							{ID: "a", Type: "command", Command: []string{"true"}},
						}}},
					}}, nil)},
				}
			},
			wantErr: "parallel branches cannot be combined with another step form",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.build()
			err := cfg.validateConnectors()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Step.Form()
// ---------------------------------------------------------------------------

func TestStepForm(t *testing.T) {
	cases := []struct {
		name string
		step Step
		want string
	}{
		{"verb", Step{Uses: "gh.comment"}, "verb"},
		{"workflow", Step{Use: "wf1"}, "workflow"},
		{"code", Step{Run: "sh", Code: "echo hi"}, "code"},
		{"agent explicit type", Step{Type: "agent"}, "agent"},
		{"agent inferred from agent:", Step{Agent: "fixer"}, "agent"},
		{"command explicit type", Step{Type: "command"}, "command"},
		{"command inferred from command:", Step{Command: []string{"true"}}, "command"},
		{"parallel", Step{Parallel: &ParallelSpec{Branches: [][]Step{{{ID: "a"}}}}}, "parallel"},
		{"indeterminate", Step{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.step.Form(); got != tc.want {
				t.Fatalf("Form() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. ParallelSpec YAML (unmarshal + marshal round trip)
// ---------------------------------------------------------------------------

func TestParallelSpecUnmarshalConcurrent(t *testing.T) {
	var wrap struct {
		Parallel ParallelSpec `yaml:"parallel"`
	}
	if err := yaml.Unmarshal([]byte("parallel: true\n"), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !wrap.Parallel.Concurrent {
		t.Fatal("parallel: true should set Concurrent = true")
	}
	if len(wrap.Parallel.Branches) != 0 {
		t.Fatalf("parallel: true should leave Branches empty, got %+v", wrap.Parallel.Branches)
	}
}

func TestParallelSpecUnmarshalBranches(t *testing.T) {
	var wrap struct {
		Parallel ParallelSpec `yaml:"parallel"`
	}
	y := "parallel: [[{id: a, uses: c.v, options: {}}]]\n"
	if err := yaml.Unmarshal([]byte(y), &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wrap.Parallel.Branches) != 1 {
		t.Fatalf("want 1 branch, got %d: %+v", len(wrap.Parallel.Branches), wrap.Parallel.Branches)
	}
	branch := wrap.Parallel.Branches[0]
	if len(branch) != 1 {
		t.Fatalf("want 1 step in the branch, got %d: %+v", len(branch), branch)
	}
	if branch[0].ID != "a" || branch[0].Uses != "c.v" {
		t.Fatalf("unexpected step: %+v", branch[0])
	}
}

func TestParallelSpecUnmarshalBadScalarErrors(t *testing.T) {
	var wrap struct {
		Parallel ParallelSpec `yaml:"parallel"`
	}
	if err := yaml.Unmarshal([]byte("parallel: nope\n"), &wrap); err == nil {
		t.Fatal("a non-bool scalar for parallel: should error")
	}
}

func TestParallelSpecMarshalRoundTrip(t *testing.T) {
	t.Run("concurrent", func(t *testing.T) {
		in := struct {
			Parallel ParallelSpec `yaml:"parallel"`
		}{Parallel: ParallelSpec{Concurrent: true}}
		out, err := yaml.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back struct {
			Parallel ParallelSpec `yaml:"parallel"`
		}
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal round trip: %v (yaml: %s)", err, out)
		}
		if !back.Parallel.Concurrent {
			t.Fatalf("round trip lost Concurrent: %+v (yaml: %s)", back.Parallel, out)
		}
	})

	t.Run("branches", func(t *testing.T) {
		in := struct {
			Parallel ParallelSpec `yaml:"parallel"`
		}{Parallel: ParallelSpec{Branches: [][]Step{{{ID: "a", Uses: "c.v"}}}}}
		out, err := yaml.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back struct {
			Parallel ParallelSpec `yaml:"parallel"`
		}
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal round trip: %v (yaml: %s)", err, out)
		}
		if len(back.Parallel.Branches) != 1 || len(back.Parallel.Branches[0]) != 1 {
			t.Fatalf("round trip lost branches: %+v (yaml: %s)", back.Parallel, out)
		}
		if got := back.Parallel.Branches[0][0]; got.ID != "a" || got.Uses != "c.v" {
			t.Fatalf("round trip lost step fields: %+v (yaml: %s)", got, out)
		}
	})
}

// ---------------------------------------------------------------------------
// 5. RetrySpec
// ---------------------------------------------------------------------------

func TestRetrySpecMaxBackoffOnlyHasNoStepRetry(t *testing.T) {
	var r RetrySpec
	if err := yaml.Unmarshal([]byte("max: 3\nbackoff: 5s\n"), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.StepRetry() != nil {
		t.Fatalf("max/backoff-only retry should have no StepRetry, got %+v", r.StepRetry())
	}
}

func TestRetrySpecWhileOutputMatchesHasStepRetry(t *testing.T) {
	var r RetrySpec
	y := "while_output_matches: retry\ninterval: 30s\ntimeout: 5m\n"
	if err := yaml.Unmarshal([]byte(y), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sr := r.StepRetry()
	if sr == nil {
		t.Fatal("while_output_matches should produce a non-nil StepRetry")
	}
	if sr.WhileOutputMatches != "retry" {
		t.Fatalf("WhileOutputMatches = %q", sr.WhileOutputMatches)
	}
	if sr.Interval.D() != 30*time.Second {
		t.Fatalf("Interval = %v, want 30s", sr.Interval.D())
	}
	if sr.Timeout.D() != 5*time.Minute {
		t.Fatalf("Timeout = %v, want 5m", sr.Timeout.D())
	}
}

// ---------------------------------------------------------------------------
// 6. MergePolicy precedence
// ---------------------------------------------------------------------------

func TestMergePolicyPrecedence(t *testing.T) {
	global := &Policy{
		QuietHours:  &QuietHours{TZ: "America/New_York", From: "22:00", To: "07:00", Hold: boolPtr(true)},
		Concurrency: &Concurrency{MaxAgents: intPtr(8)},
		PauseLabel:  strPtr("a"),
	}
	connector := &Policy{
		Ignore:     &Ignore{Users: []string{"x"}},
		PauseLabel: strPtr("b"),
	}
	trigger := &Policy{
		QuietHours:  &QuietHours{Hold: boolPtr(false)},
		Concurrency: &Concurrency{MaxAgents: intPtr(2)},
	}

	out := MergePolicy(global, connector, trigger)

	if out.QuietHours == nil {
		t.Fatal("QuietHours missing from merged policy")
	}
	if out.QuietHours.TZ != "America/New_York" || out.QuietHours.From != "22:00" || out.QuietHours.To != "07:00" {
		t.Fatalf("tz/from/to should carry over from global, got %+v", out.QuietHours)
	}
	if out.QuietHours.Hold == nil || *out.QuietHours.Hold != false {
		t.Fatalf("trigger's hold:false should win, got %+v", out.QuietHours.Hold)
	}
	if out.Concurrency == nil || out.Concurrency.MaxAgents == nil || *out.Concurrency.MaxAgents != 2 {
		t.Fatalf("trigger's max_agents should win, got %+v", out.Concurrency)
	}
	if out.PauseLabel == nil || *out.PauseLabel != "b" {
		t.Fatalf("connector's pause_label should win over global (trigger sets none), got %v", out.PauseLabel)
	}
	if out.Ignore == nil || len(out.Ignore.Users) != 1 || out.Ignore.Users[0] != "x" {
		t.Fatalf("connector's ignore.users should carry through, got %+v", out.Ignore)
	}
}

func TestMergePolicyAllNilsReturnsZeroPolicy(t *testing.T) {
	out := MergePolicy(nil, nil, nil)
	if out != (Policy{}) {
		t.Fatalf("MergePolicy(nil, nil, nil) = %+v, want zero Policy", out)
	}
}

func TestMergePolicyQuietHoursFieldLevelOverlay(t *testing.T) {
	global := &Policy{QuietHours: &QuietHours{TZ: "UTC", From: "22:00", To: "07:00", Hold: boolPtr(true)}}
	trigger := &Policy{QuietHours: &QuietHours{From: "23:00"}}

	out := MergePolicy(global, trigger)

	if out.QuietHours.From != "23:00" {
		t.Fatalf("trigger's From should win, got %q", out.QuietHours.From)
	}
	if out.QuietHours.TZ != "UTC" || out.QuietHours.To != "07:00" {
		t.Fatalf("unset trigger fields should keep global's, got %+v", out.QuietHours)
	}
	if out.QuietHours.Hold == nil || *out.QuietHours.Hold != true {
		t.Fatalf("unset trigger Hold should keep global's, got %+v", out.QuietHours.Hold)
	}
}

// ---------------------------------------------------------------------------
// 7. TriggerSpec helpers
// ---------------------------------------------------------------------------

func TestTriggerSpecConnectorAndEvent(t *testing.T) {
	tr := TriggerSpec{On: "gh.new_comment"}
	if got := tr.Connector(); got != "gh" {
		t.Fatalf("Connector() = %q, want %q", got, "gh")
	}
	if got := tr.Event(); got != "new_comment" {
		t.Fatalf("Event() = %q, want %q", got, "new_comment")
	}

	nodot := TriggerSpec{On: "nodot"}
	if got := nodot.Connector(); got != "nodot" {
		t.Fatalf("Connector() on a no-dot string = %q, want %q", got, "nodot")
	}
	if got := nodot.Event(); got != "" {
		t.Fatalf("Event() on a no-dot string = %q, want empty", got)
	}
}

func TestTriggerSpecIsEnabled(t *testing.T) {
	if !(TriggerSpec{}).IsEnabled() {
		t.Fatal("a trigger with no enabled: field should default to enabled")
	}
	f := false
	if (TriggerSpec{Enabled: &f}).IsEnabled() {
		t.Fatal("enabled: false should be honored")
	}
}

// ---------------------------------------------------------------------------
// 8. GroupSpec parse
// ---------------------------------------------------------------------------

func TestGroupSpecDurations(t *testing.T) {
	var g GroupSpec
	if err := yaml.Unmarshal([]byte("window: 15s\nmax_wait: 60s\n"), &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if g.Window.D() != 15*time.Second {
		t.Fatalf("Window = %v, want 15s", g.Window.D())
	}
	if g.MaxWait.D() != 60*time.Second {
		t.Fatalf("MaxWait = %v, want 60s", g.MaxWait.D())
	}
}

// ---------------------------------------------------------------------------
// 9. AgentProfile.RuntimeName()
// ---------------------------------------------------------------------------

func TestAgentProfileRuntimeName(t *testing.T) {
	if got := (AgentProfile{Runtime: "r1", Controller: "c1"}).RuntimeName(); got != "r1" {
		t.Fatalf("Runtime should win over Controller, got %q", got)
	}
	if got := (AgentProfile{Controller: "c1"}).RuntimeName(); got != "c1" {
		t.Fatalf("should fall back to Controller when Runtime is unset, got %q", got)
	}
	if got := (AgentProfile{}).RuntimeName(); got != "" {
		t.Fatalf("an empty profile should return empty, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// 10. Agent validation (runtime/host/legacy-controller references)
// ---------------------------------------------------------------------------

func connBaseCfg() *Config {
	return &Config{ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}}}
}

func TestAgentRuntimeReferenceValid(t *testing.T) {
	c := connBaseCfg()
	c.Runtimes = map[string]RuntimeConfig{"paseo1": {Type: "paseo"}}
	c.Agents = map[string]AgentProfile{"fixer": {Runtime: "paseo1"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("an agent referencing a defined runtimes: entry should pass, got %v", err)
	}
}

func TestAgentRuntimeReferenceUnknown(t *testing.T) {
	c := connBaseCfg()
	c.Agents = map[string]AgentProfile{"fixer": {Runtime: "nope"}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown runtime") {
		t.Fatalf("an agent referencing nothing should fail with 'unknown runtime', got %v", err)
	}
}

func TestAgentHostReferenceUnknown(t *testing.T) {
	c := connBaseCfg()
	c.Agents = map[string]AgentProfile{"fixer": {Host: "nope"}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `unknown host "nope"`) {
		t.Fatalf("an agent referencing an unknown host should fail, got %v", err)
	}
}

func TestAgentLegacyControllerReferenceStillPasses(t *testing.T) {
	c := connBaseCfg()
	c.Controllers = map[string]ControllerConfig{"pae": {Type: "paseo"}}
	c.Agents = map[string]AgentProfile{"fixer": {Runtime: "pae"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("an agent referencing a legacy controllers: entry should still pass, got %v", err)
	}
}

// TestConfigAgentCapPrecedence proves the effective concurrency accessors
// prefer the global policy.concurrency values and fall back to the legacy
// control fields.
func TestConfigAgentCapPrecedence(t *testing.T) {
	// Nothing set: the legacy default (3) applies.
	c := &Config{}
	if got := c.AgentCap(); got != 3 {
		t.Fatalf("default AgentCap = %d, want 3", got)
	}
	if got := c.AgentsPerHour(); got != 0 {
		t.Fatalf("default AgentsPerHour = %d, want 0 (unlimited)", got)
	}

	// Only legacy set: it applies.
	c.Control.MaxConcurrentAgents = intPtr(5)
	c.Control.MaxAgentsPerHour = 7
	if got := c.AgentCap(); got != 5 {
		t.Fatalf("legacy AgentCap = %d, want 5", got)
	}
	if got := c.AgentsPerHour(); got != 7 {
		t.Fatalf("legacy AgentsPerHour = %d, want 7", got)
	}

	// policy.concurrency set: it wins over legacy.
	c.Policy = &Policy{Concurrency: &Concurrency{MaxAgents: intPtr(2), MaxAgentsPerHour: intPtr(9)}}
	if got := c.AgentCap(); got != 2 {
		t.Fatalf("policy AgentCap = %d, want 2", got)
	}
	if got := c.AgentsPerHour(); got != 9 {
		t.Fatalf("policy AgentsPerHour = %d, want 9", got)
	}

	// A policy block without concurrency still falls back to legacy.
	c.Policy = &Policy{}
	if got := c.AgentCap(); got != 5 {
		t.Fatalf("fallback AgentCap = %d, want 5", got)
	}
}

// TestMergePolicySameKeyThreeScopes (E4): one key set at ALL three scopes —
// the trigger (most specific) wins; drop the trigger and the connector wins.
func TestMergePolicySameKeyThreeScopes(t *testing.T) {
	global := &Policy{
		PauseLabel:  strPtr("global:hold"),
		Concurrency: &Concurrency{MaxAgents: intPtr(8)},
		Backoff:     &Backoff{Base: Duration(10 * time.Minute)},
	}
	connector := &Policy{
		PauseLabel:  strPtr("conn:hold"),
		Concurrency: &Concurrency{MaxAgents: intPtr(4)},
		Backoff:     &Backoff{Base: Duration(20 * time.Minute)},
	}
	trigger := &Policy{
		PauseLabel:  strPtr("trig:hold"),
		Concurrency: &Concurrency{MaxAgents: intPtr(1)},
		Backoff:     &Backoff{Base: Duration(30 * time.Minute)},
	}

	out := MergePolicy(global, connector, trigger)
	if *out.PauseLabel != "trig:hold" || *out.Concurrency.MaxAgents != 1 || out.Backoff.Base.D() != 30*time.Minute {
		t.Fatalf("trigger scope should win on every conflicting key, got %+v", out)
	}

	out = MergePolicy(global, connector, nil)
	if *out.PauseLabel != "conn:hold" || *out.Concurrency.MaxAgents != 4 || out.Backoff.Base.D() != 20*time.Minute {
		t.Fatalf("connector scope should win over global, got %+v", out)
	}

	out = MergePolicy(global, nil, nil)
	if *out.PauseLabel != "global:hold" || *out.Concurrency.MaxAgents != 8 {
		t.Fatalf("global applies when nothing overrides, got %+v", out)
	}
}
