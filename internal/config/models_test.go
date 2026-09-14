package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// --- runtimes: scalar | list | map, key-implies-use ------------------------

func TestRuntimeSetShapes(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		want  map[string]RuntimeConfig
		error string
	}{
		{
			name: "scalar",
			yaml: `runtimes: paseo`,
			want: map[string]RuntimeConfig{"paseo": {}},
		},
		{
			name: "list of names",
			yaml: `runtimes: [paseo, claude]`,
			want: map[string]RuntimeConfig{"paseo": {}, "claude": {}},
		},
		{
			name: "list with an object item naming use:",
			yaml: "runtimes:\n  - paseo\n  - use: acme/plugins/modal\n",
			want: map[string]RuntimeConfig{
				"paseo": {},
				"modal": {Use: "acme/plugins/modal"},
			},
		},
		{
			name: "map with config",
			yaml: "runtimes:\n  paseo:\n    models:\n      prefer: [claude-opus-5]\n",
			want: map[string]RuntimeConfig{
				"paseo": {Models: &RuntimeModels{Prefer: []string{"claude-opus-5"}}},
			},
		},
		{
			name: "null is empty",
			yaml: `runtimes:`,
			want: nil,
		},
		{
			name:  "object list item without use:",
			yaml:  "runtimes:\n  - models: { prefer: [x] }\n",
			error: "needs `use:`",
		},
		{
			name:  "duplicate in list",
			yaml:  `runtimes: [paseo, paseo]`,
			error: "duplicate runtime",
		},
		{
			name:  "unknown key in a map entry is still strict",
			yaml:  "runtimes:\n  paseo:\n    binn: x\n",
			error: "field binn not found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			err := strictUnmarshal([]byte(tc.yaml), &c)
			if tc.error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("want error containing %q, got %v", tc.error, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(tc.want) == 0 {
				if len(c.Runtimes) != 0 {
					t.Fatalf("want empty runtimes, got %#v", c.Runtimes)
				}
				return
			}
			if len(c.Runtimes) != len(tc.want) {
				t.Fatalf("want %d runtimes, got %d (%#v)", len(tc.want), len(c.Runtimes), c.Runtimes)
			}
			for name, want := range tc.want {
				got, ok := c.Runtimes[name]
				if !ok {
					t.Fatalf("missing runtime %q (got %#v)", name, c.Runtimes)
				}
				if got.Use != want.Use {
					t.Errorf("runtime %q: use = %q, want %q", name, got.Use, want.Use)
				}
				if !reflect.DeepEqual(got.Models, want.Models) {
					t.Errorf("runtime %q: models = %#v, want %#v", name, got.Models, want.Models)
				}
			}
		})
	}
}

func TestRuntimeKeyImpliesUse(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte("runtimes: [paseo, claude]\n"), &c); err != nil {
		t.Fatal(err)
	}
	for _, rt := range c.Runtimes {
		if rt.Use != "" {
			t.Fatalf("expected use: unset before defaults, got %q", rt.Use)
		}
	}
	c.applyRuntimeUseDefaults()
	if got := c.Runtimes["paseo"].Use; got != "paseo" {
		t.Errorf("paseo use = %q, want paseo", got)
	}
	if got := c.Runtimes["claude"].Use; got != "claude" {
		t.Errorf("claude use = %q, want claude", got)
	}
}

func TestRuntimeKeyImpliesUseDoesNotOverrideExtends(t *testing.T) {
	// A child inheriting use: through extends: must keep the inherited value,
	// not fall back to its own key — so the implication runs after extends.
	c := &Config{Runtimes: RuntimeSet{
		"base":   {Use: "acme/plugins/modal"},
		"remote": {Extends: "base"},
	}}
	if err := c.resolveExtends(); err != nil {
		t.Fatal(err)
	}
	c.applyRuntimeUseDefaults()
	if got := c.Runtimes["remote"].Use; got != "acme/plugins/modal" {
		t.Errorf("inherited use = %q, want acme/plugins/modal", got)
	}
}

func TestRuntimeKeyImpliesUseLeavesLegacyTypeAlone(t *testing.T) {
	// A stale `type:` must still produce the migration-specific error rather
	// than being papered over by the key implication.
	var c Config
	if err := strictUnmarshal([]byte("runtimes:\n  rt:\n    type: paseo\n"), &c); err != nil {
		t.Fatal(err)
	}
	c.applyRuntimeUseDefaults()
	if got := c.Runtimes["rt"].Use; got != "" {
		t.Fatalf("use should stay empty while type: is present, got %q", got)
	}
	err := validateUseRef("runtime rt", c.Runtimes["rt"].Use, c.Runtimes["rt"].legacyType(), UseKindRuntime)
	if err == nil || !strings.Contains(err.Error(), "conductor config migrate") {
		t.Fatalf("want migration error, got %v", err)
	}
}

// --- models: block on a runtime -------------------------------------------

func TestRuntimeModelsBlock(t *testing.T) {
	src := `
runtimes:
  paseo:
    models:
      default: claude-opus-5
      prefer: [claude-opus-5, gpt-5.6-sol]
      allow: ["claude-opus-*", "gpt-5.6-*"]
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	m := c.Runtimes["paseo"].Models
	if m == nil {
		t.Fatal("models block missing")
	}
	if m.Default != "claude-opus-5" {
		t.Errorf("default = %q", m.Default)
	}
	if !reflect.DeepEqual(m.Prefer, []string{"claude-opus-5", "gpt-5.6-sol"}) {
		t.Errorf("prefer = %#v", m.Prefer)
	}
	if !reflect.DeepEqual(m.Allow, []string{"claude-opus-*", "gpt-5.6-*"}) {
		t.Errorf("allow = %#v", m.Allow)
	}
}

func TestValidateRuntimeModels(t *testing.T) {
	tests := []struct {
		name  string
		in    *RuntimeModels
		error string
	}{
		{name: "nil is fine"},
		{name: "full", in: &RuntimeModels{Default: "claude-opus-5", Allow: []string{"claude-*"}}},
		{
			name:  "default may not be a pattern",
			in:    &RuntimeModels{Default: "claude-*"},
			error: "must be a concrete model id",
		},
		{
			name:  "default excluded by allow",
			in:    &RuntimeModels{Default: "gpt-5.6-sol", Allow: []string{"claude-*"}},
			error: "excluded by models.allow",
		},
		{
			name:  "empty prefer entry",
			in:    &RuntimeModels{Prefer: []string{"a", " "}},
			error: "models.prefer[1] is empty",
		},
		{
			name:  "empty allow entry",
			in:    &RuntimeModels{Allow: []string{""}},
			error: "models.allow[0] is empty",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRuntimeModels("runtime r", tc.in)
			switch {
			case tc.error == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.error != "" && (err == nil || !strings.Contains(err.Error(), tc.error)):
				t.Fatalf("want error containing %q, got %v", tc.error, err)
			}
		})
	}
}

// --- fleets and the model: field ------------------------------------------

func TestModelSpecShapes(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		want  ModelSpec
		error string
	}{
		{name: "string", yaml: `model: reviewer`, want: ModelSpec{Ref: "reviewer", present: true}},
		{name: "exact pin", yaml: `model: gemini-2.5-flash`, want: ModelSpec{Ref: "gemini-2.5-flash", present: true}},
		{name: "full wildcard", yaml: `model: "*"`, want: ModelSpec{Ref: "*", present: true}},
		{
			name: "array",
			yaml: `model: ["claude-opus-*", "gpt-5.6-*"]`,
			want: ModelSpec{Any: []string{"claude-opus-*", "gpt-5.6-*"}, present: true},
		},
		{
			name: "object",
			yaml: `model: { any: ["claude-opus-*"], required: true }`,
			want: ModelSpec{Any: []string{"claude-opus-*"}, Required: true, present: true},
		},
		{name: "absent", yaml: `id: x`, want: ModelSpec{}},
		{name: "empty scalar", yaml: `model: ""`, error: "model: is empty"},
		// yaml.v3 leaves a null node at the zero value without calling the
		// custom unmarshaler, so `model:` with no value reads as absent — the
		// same as every other optional key in this schema.
		{name: "null reads as absent", yaml: `model:`, want: ModelSpec{}},
		{name: "unknown object key", yaml: `model: { any: [a], require: true }`, error: "field require not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s Step
			err := strictUnmarshal([]byte(tc.yaml), &s)
			if tc.error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("want error containing %q, got %v", tc.error, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(s.Model, tc.want) {
				t.Fatalf("model = %#v, want %#v", s.Model, tc.want)
			}
		})
	}
}

func TestModelSpecRoundTrip(t *testing.T) {
	for _, in := range []string{
		`reviewer`,
		`"*"`,
		`[claude-opus-*, gpt-5.6-*]`,
		`{any: [claude-opus-*], required: true}`,
	} {
		var got ModelSpec
		if err := yaml.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		out, err := yaml.Marshal(got)
		if err != nil {
			t.Fatalf("%s: marshal: %v", in, err)
		}
		var back ModelSpec
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: reparse %q: %v", in, out, err)
		}
		if !reflect.DeepEqual(got, back) {
			t.Errorf("%s: round trip %#v -> %q -> %#v", in, got, out, back)
		}
	}
}

func TestModelSpecOmittedOnMarshal(t *testing.T) {
	out, err := yaml.Marshal(Step{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "model") {
		t.Fatalf("unset model should not marshal: %s", out)
	}
}

func TestTopLevelFleets(t *testing.T) {
	src := `
models:
  reviewer:
    any: ["claude-opus-*", "gpt-5.6-*"]
    required: true
  summarizer: ["claude-haiku-*", "*-mini"]
  pinned: gemini-2.5-flash
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.Models["reviewer"]; !got.Required || len(got.Any) != 2 {
		t.Errorf("reviewer = %#v", got)
	}
	if got := c.Models["summarizer"]; got.Required || !reflect.DeepEqual(got.Any, []string{"claude-haiku-*", "*-mini"}) {
		t.Errorf("summarizer = %#v", got)
	}
	if got := c.Models["pinned"]; got.Ref != "gemini-2.5-flash" {
		t.Errorf("pinned = %#v", got)
	}
}

func TestValidateFleets(t *testing.T) {
	tests := []struct {
		name  string
		in    FleetSpec
		error string
	}{
		{name: "ok object", in: FleetOf(true, "claude-opus-*")},
		{name: "ok string", in: ModelSpecOf("claude-opus-5")},
		{name: "unset", in: FleetSpec{}, error: "empty fleet"},
		{name: "empty any", in: FleetOf(false), error: "needs at least one acceptable model"},
		{name: "blank entry", in: FleetOf(false, "a", " "), error: "any[1] is empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFleet("models.f", tc.in)
			switch {
			case tc.error == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.error != "" && (err == nil || !strings.Contains(err.Error(), tc.error)):
				t.Fatalf("want error containing %q, got %v", tc.error, err)
			}
		})
	}
}

func TestValidateModelsRejectsEmptyFleetName(t *testing.T) {
	c := &Config{Models: map[string]FleetSpec{"": FleetOf(false, "x")}}
	if err := c.validateModels(); err == nil || !strings.Contains(err.Error(), "empty fleet name") {
		t.Fatalf("want empty-fleet-name error, got %v", err)
	}
}

// --- wildcard matching and roster expansion --------------------------------

func TestMatchModelPattern(t *testing.T) {
	tests := []struct {
		pattern, model string
		want           bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"claude-opus-*", "claude-opus-5", true},
		{"claude-opus-*", "claude-opus-4-8", true},
		{"claude-opus-*", "claude-sonnet-5", false},
		{"*-mini", "gpt-5.6-mini", true},
		{"*-mini", "gpt-5.6", false},
		{"*pro*", "gemini-3.0-pro-preview", true},
		{"*pro*", "gemini-3.0-flash", false},
		{"gpt-5.6-sol", "gpt-5.6-sol", true},
		{"gpt-5.6-sol", "gpt-5.6-solo", false},
		{"gemini-?.5-flash", "gemini-2.5-flash", true},
		{"gemini-?.5-flash", "gemini-2.6-flash", false},
		// `*` crosses a "/" — a model id is not a path.
		{"openai/*", "openai/gpt-5.6-sol", true},
		{"*/gpt-*", "openai/gpt-5.6-sol", true},
	}
	for _, tc := range tests {
		if got := MatchModelPattern(tc.pattern, tc.model); got != tc.want {
			t.Errorf("match(%q, %q) = %v, want %v", tc.pattern, tc.model, got, tc.want)
		}
	}
}

func TestExpandModelPatterns(t *testing.T) {
	roster := []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "gpt-5.6-sol", "gpt-5.6-mini"}
	tests := []struct {
		name       string
		acceptable []string
		want       []string
	}{
		{
			name:       "literals keep listed priority",
			acceptable: []string{"gpt-5.6-sol", "claude-opus-5"},
			want:       []string{"gpt-5.6-sol", "claude-opus-5"},
		},
		{
			name:       "wildcard expands in place, roster order",
			acceptable: []string{"claude-opus-*", "gpt-5.6-*"},
			want:       []string{"claude-opus-5", "gpt-5.6-sol", "gpt-5.6-mini"},
		},
		{
			name:       "wildcard de-dupes against an earlier literal",
			acceptable: []string{"gpt-5.6-mini", "gpt-5.6-*"},
			want:       []string{"gpt-5.6-mini", "gpt-5.6-sol"},
		},
		{
			name:       "full wildcard is the whole roster",
			acceptable: []string{"*"},
			want:       roster,
		},
		{
			name:       "a literal absent from the roster contributes nothing",
			acceptable: []string{"claude-opus-9", "claude-sonnet-5"},
			want:       []string{"claude-sonnet-5"},
		},
		{
			name:       "a pattern matching nothing falls through",
			acceptable: []string{"llama-*", "claude-haiku-*"},
			want:       []string{"claude-haiku-4-5"},
		},
		{name: "nothing matches", acceptable: []string{"llama-*"}, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandModelPatterns(tc.acceptable, roster)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExpandModelPatternsEmptyRoster(t *testing.T) {
	if got := ExpandModelPatterns([]string{"*"}, nil); len(got) != 0 {
		t.Fatalf(`"*" against an empty roster must be empty, got %v`, got)
	}
}

func TestFilterRoster(t *testing.T) {
	roster := []string{"claude-opus-5", "gpt-5.6-sol", "gemini-3.0-pro"}
	if got := FilterRoster(roster, nil); !reflect.DeepEqual(got, roster) {
		t.Errorf("no allow list should not filter: %v", got)
	}
	got := FilterRoster(roster, []string{"claude-*", "gemini-*"})
	if !reflect.DeepEqual(got, []string{"claude-opus-5", "gemini-3.0-pro"}) {
		t.Errorf("filtered = %v", got)
	}
}

func TestRankByPrefer(t *testing.T) {
	cands := []string{"gpt-5.6-sol", "claude-opus-5", "gemini-3.0-pro"}
	got := RankByPrefer(cands, []string{"claude-opus-*", "gemini-*"})
	want := []string{"claude-opus-5", "gemini-3.0-pro", "gpt-5.6-sol"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Stable: unranked candidates keep their relative order.
	got = RankByPrefer(cands, []string{"nothing-*"})
	if !reflect.DeepEqual(got, cands) {
		t.Fatalf("got %v, want %v", got, cands)
	}
}
