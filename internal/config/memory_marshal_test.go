package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A pack step shipped with `memory: false` stayed disabled only until an
// override touched the step: applyStepOverride marshals the step, merges, and
// decodes, and the selector marshaled as a plain map — which decodes as ON.
func TestMemoryFalseSurvivesAStepOverride(t *testing.T) {
	for _, tc := range []struct {
		name string
		sel  MemorySelector
	}{
		{"disabled", MemorySelector{}},
		{"enabled", MemorySelector{Enabled: true}},
		{"narrowed", MemorySelector{Enabled: true, Scopes: []string{"acme/api", "${repo}"}, Tags: []string{"ci"}, Limit: 4}},
		{"extra scopes", MemorySelector{Enabled: true, ExtraScopes: []string{"standard:${repo}", "emphasis:${repo}"}}},
		{"disabled with fields", MemorySelector{Scopes: []string{"x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel := tc.sel
			base := Step{Type: "agent", Name: "lens", Prompt: "p", Memory: &sel}
			out, err := applyStepOverride(base, map[string]any{"prompt": "overridden"})
			if err != nil {
				t.Fatal(err)
			}
			if out.Prompt != "overridden" {
				t.Fatalf("the override did not apply: %q", out.Prompt)
			}
			if out.Memory == nil {
				t.Fatal("memory selector lost")
			}
			got := *out.Memory
			if got.Enabled != sel.Enabled {
				t.Fatalf("Enabled flipped %v -> %v through an override round trip", sel.Enabled, got.Enabled)
			}
			if !sel.Enabled {
				return // disabled is just `false`: nothing else to keep
			}
			if !sameList(got.Scopes, sel.Scopes) || !sameList(got.ExtraScopes, sel.ExtraScopes) ||
				!sameList(got.Tags, sel.Tags) || got.Limit != sel.Limit {
				t.Fatalf("round trip changed the selector: %+v -> %+v", sel, got)
			}
		})
	}
	// The override itself can still turn memory off or on.
	on := MemorySelector{Enabled: true}
	out, err := applyStepOverride(Step{Type: "agent", Memory: &on}, map[string]any{"memory": false})
	if err != nil || out.Memory == nil || out.Memory.Enabled {
		t.Fatalf("override memory: false: %+v %v", out.Memory, err)
	}
	off := MemorySelector{}
	out, err = applyStepOverride(Step{Type: "agent", Memory: &off}, map[string]any{"memory": map[string]any{"extra_scopes": []any{"standard:${repo}"}}})
	if err != nil || out.Memory == nil || !out.Memory.Enabled || !sameList(out.Memory.ExtraScopes, []string{"standard:${repo}"}) {
		t.Fatalf("override memory map: %+v %v", out.Memory, err)
	}
}

func sameList(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// legacySelector is MemorySelector as it marshaled before MarshalYAML
// existed: its Go fields, untagged.
type legacySelector struct {
	Enabled bool
	Scopes  []string
	Tags    []string
	Limit   int
}

// An unnamed step's identity is a hash of its marshaled YAML, so the
// selector must marshal byte-for-byte as it always did — or every unnamed
// step with memory: would get a new session binding and a new `${step}`
// memory key on upgrade.
func TestMemorySelectorMarshalsAsItAlwaysDid(t *testing.T) {
	for _, s := range []MemorySelector{
		{Enabled: true},
		{Enabled: true, Scopes: []string{"", "acme/api"}, Tags: []string{"ci"}, Limit: 10},
		{Enabled: true, Scopes: []string{"${step}"}},
	} {
		got, err := yaml.Marshal(Step{Type: "agent", Prompt: "x", Memory: &s})
		if err != nil {
			t.Fatal(err)
		}
		type legacyStep struct {
			Type   string          `yaml:"type,omitempty"`
			Prompt string          `yaml:"prompt,omitempty"`
			Memory *legacySelector `yaml:"memory,omitempty"`
		}
		want, _ := yaml.Marshal(legacyStep{Type: "agent", Prompt: "x", Memory: &legacySelector{s.Enabled, s.Scopes, s.Tags, s.Limit}})
		if !strings.Contains(string(got), string(want[strings.Index(string(want), "memory:"):])) {
			t.Fatalf("selector %+v marshals differently from before:\n got:\n%s\nwant (memory part):\n%s", s, got, want)
		}
	}
	// And so the fingerprint of a step that uses memory is unchanged by the
	// round trip a pack override takes.
	sel := MemorySelector{Enabled: true, Tags: []string{"ci"}}
	st := Step{Type: "agent", Prompt: "x", Memory: &sel}
	out, err := applyStepOverride(st, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Fingerprint() != out.Fingerprint() {
		t.Fatal("a no-op override round trip moved the step's fingerprint")
	}
}

func TestMemorySelectorExtraScopes(t *testing.T) {
	var p Step
	if err := yaml.Unmarshal([]byte(`memory: { extra_scopes: ["standard:${repo}", "emphasis:${repo}"], tags: [lens] }`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Memory == nil || !p.Memory.Enabled || len(p.Memory.Scopes) != 0 ||
		!reflect.DeepEqual(p.Memory.ExtraScopes, []string{"standard:${repo}", "emphasis:${repo}"}) {
		t.Fatalf("extra_scopes: %+v", p.Memory)
	}
	if err := yaml.Unmarshal([]byte(`memory: { extra_scopes: [""] }`), &Step{}); err == nil {
		t.Fatal("a blank extra scope must be refused")
	}
	p = Step{}
	if err := yaml.Unmarshal([]byte(`memory: { enabled: false, tags: [x] }`), &p); err != nil || p.Memory.Enabled {
		t.Fatalf("enabled: false in the map form: %+v %v", p.Memory, err)
	}
}

// `${repo}` / `${workflow}` / `${step}` are the selector's placeholders, not
// environment variables: a config using them loads, and other undefined
// `${VAR}` references still fail.
func TestMemoryPlaceholdersAreNotEnvVars(t *testing.T) {
	dir := t.TempDir()
	body := `
connectors: { box: { use: command } }
triggers:
  - on: manual
    name: t
    steps:
      - type: agent
        prompt: hi
        memory: { scopes: ["${repo}", "${workflow}"], extra_scopes: ["standard:${repo}", "${step}"] }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("repo", "") // even a defined env var of that name is not read
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a config using the memory placeholders must load: %v", err)
	}
	m := cfg.Triggers[0].Steps[0].Memory
	if !reflect.DeepEqual(m.Scopes, []string{"${repo}", "${workflow}"}) || !reflect.DeepEqual(m.ExtraScopes, []string{"standard:${repo}", "${step}"}) {
		t.Fatalf("placeholders must reach the selector verbatim: %+v", m)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(body, "prompt: hi", "prompt: ${CONDUCTOR_TEST_UNDEFINED_VAR}", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "CONDUCTOR_TEST_UNDEFINED_VAR") {
		t.Fatalf("other undefined env refs must still fail: %v", err)
	}
}
