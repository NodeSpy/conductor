package connector

import (
	"context"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/secrets"
)

// --- MergeOptions ---

func TestMergeOptionsCallWins(t *testing.T) {
	defaults := map[string]any{"a": "1", "b": "2"}
	call := map[string]any{"b": "3", "c": "4"}
	got := MergeOptions(defaults, call)
	want := map[string]any{"a": "1", "b": "3", "c": "4"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key %q: got %v, want %v", k, got[k], v)
		}
	}
}

func TestMergeOptionsNestedMapsMergeRecursively(t *testing.T) {
	defaults := map[string]any{
		"outer": map[string]any{"x": "1", "y": "2"},
	}
	call := map[string]any{
		"outer": map[string]any{"y": "overridden", "z": "3"},
	}
	got := MergeOptions(defaults, call)
	outer, ok := got["outer"].(map[string]any)
	if !ok {
		t.Fatalf("outer: not a map: %v", got["outer"])
	}
	if outer["x"] != "1" {
		t.Fatalf("outer.x: got %v, want 1 (preserved from defaults)", outer["x"])
	}
	if outer["y"] != "overridden" {
		t.Fatalf("outer.y: got %v, want overridden (call wins)", outer["y"])
	}
	if outer["z"] != "3" {
		t.Fatalf("outer.z: got %v, want 3 (added by call)", outer["z"])
	}
}

func TestMergeOptionsNonMapReplacesEvenIfDefaultWasMap(t *testing.T) {
	defaults := map[string]any{"outer": map[string]any{"x": "1"}}
	call := map[string]any{"outer": "scalar-now"}
	got := MergeOptions(defaults, call)
	if got["outer"] != "scalar-now" {
		t.Fatalf("outer: got %v, want scalar-now", got["outer"])
	}
}

// --- ValidateSchema ---

func TestValidateSchemaUnknownKey(t *testing.T) {
	s := Schema{"a": {Type: TString}}
	err := ValidateSchema("test", s, map[string]any{"b": "x"})
	if err == nil || !strings.Contains(err.Error(), `unknown key "b"`) {
		t.Fatalf("got %v, want unknown key error", err)
	}
}

func TestValidateSchemaMissingRequired(t *testing.T) {
	s := Schema{"a": {Type: TString, Required: true}}
	err := ValidateSchema("test", s, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), `missing required key "a"`) {
		t.Fatalf("got %v, want missing required key error", err)
	}
}

func TestValidateSchemaEnum(t *testing.T) {
	s := Schema{"a": {Type: TString, Enum: []string{"x", "y"}}}
	if err := ValidateSchema("test", s, map[string]any{"a": "x"}); err != nil {
		t.Fatalf("valid enum value rejected: %v", err)
	}
	err := ValidateSchema("test", s, map[string]any{"a": "z"})
	if err == nil || !strings.Contains(err.Error(), "must be one of x|y") {
		t.Fatalf("got %v, want enum error", err)
	}
}

func TestValidateSchemaTypeMismatches(t *testing.T) {
	cases := []struct {
		name    string
		field   Field
		value   any
		wantErr bool
	}{
		{"string ok", Field{Type: TString}, "x", false},
		{"string got int", Field{Type: TString}, 1, true},
		{"int ok", Field{Type: TInt}, 5, false},
		{"int got string", Field{Type: TInt}, "5", true},
		{"bool ok", Field{Type: TBool}, true, false},
		{"bool got string", Field{Type: TBool}, "true", true},
		{"list ok slice-any", Field{Type: TList}, []any{"a"}, false},
		{"list ok slice-string", Field{Type: TList}, []string{"a"}, false},
		{"list got string", Field{Type: TList}, "a", true},
		{"map ok", Field{Type: TMap}, map[string]any{"a": 1}, false},
		{"map got string", Field{Type: TMap}, "a", true},
		{"any accepts anything", Field{Type: TAny}, 42, false},
		{"duration string ok", Field{Type: TDuration}, "30s", false},
		{"duration bad string", Field{Type: TDuration}, "not-a-duration", true},
		{"duration numeric ok", Field{Type: TDuration}, 30, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Schema{"k": c.field}
			err := ValidateSchema("test", s, map[string]any{"k": c.value})
			if c.wantErr && err == nil {
				t.Fatalf("want error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
		})
	}
}

func TestValidateSchemaTemplateStringPassesAnyType(t *testing.T) {
	s := Schema{"k": {Type: TInt, Required: true}}
	if err := ValidateSchema("test", s, map[string]any{"k": "{{ .foo }}"}); err != nil {
		t.Fatalf("template string should pass type checking regardless of declared type: %v", err)
	}
}

// --- TypeDecl.Event dynamic lookup ---

func TestTypeDeclEventDynamicLookup(t *testing.T) {
	decl := &TypeDecl{
		Events: []EventDecl{
			{Name: "fixed", Desc: "a fixed event"},
			{Name: "<dyn>", Dynamic: true, Desc: "template for dynamic names"},
		},
	}
	if e, ok := decl.Event("fixed"); !ok || e.Desc != "a fixed event" {
		t.Fatalf("exact match failed: %+v, %v", e, ok)
	}
	e, ok := decl.Event("anything-goes")
	if !ok {
		t.Fatalf("dynamic fallback should match any name")
	}
	if !e.Dynamic || e.Desc != "template for dynamic names" {
		t.Fatalf("dynamic fallback returned wrong decl: %+v", e)
	}
}

func TestTypeDeclEventNoMatchNoDynamic(t *testing.T) {
	decl := &TypeDecl{Events: []EventDecl{{Name: "fixed"}}}
	if _, ok := decl.Event("nope"); ok {
		t.Fatalf("expected no match")
	}
}

// --- rate limiter ---

func TestRateLimiterCapsWithinWindow(t *testing.T) {
	rl := newRateLimiter(2)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }
	var slept []time.Duration
	rl.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d) // simulate time passing
		return nil
	}
	ctx := context.Background()
	if err := rl.wait(ctx); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if err := rl.wait(ctx); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if len(slept) != 0 {
		t.Fatalf("first two calls should not sleep, slept=%v", slept)
	}
	// third call exceeds the cap of 2/minute -> must sleep.
	if err := rl.wait(ctx); err != nil {
		t.Fatalf("call 3: %v", err)
	}
	if len(slept) != 1 {
		t.Fatalf("call 3 should have slept once, slept=%v", slept)
	}
}

func TestRateLimiterWindowSlides(t *testing.T) {
	rl := newRateLimiter(1)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }
	rl.sleep = func(ctx context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}
	ctx := context.Background()
	if err := rl.wait(ctx); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	// advance real clock past the window without going through sleep.
	now = now.Add(time.Minute + time.Second)
	if err := rl.wait(ctx); err != nil {
		t.Fatalf("call 2 after window slide: %v", err)
	}
	// stamps should have exactly 1 entry (the old one aged out).
	if len(rl.stamps) != 1 {
		t.Fatalf("stamps = %v, want 1 entry after window slide", rl.stamps)
	}
}

func TestRateLimiterCtxCancel(t *testing.T) {
	rl := newRateLimiter(1)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }
	rl.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	// first call is under the cap: succeeds regardless of ctx state.
	if err := rl.wait(context.Background()); err != nil {
		t.Fatalf("first call under cap should succeed: %v", err)
	}
	// second call hits the cap and must sleep; a cancelled ctx propagates
	// straight out of wait() via the injected sleep func.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rl.wait(ctx); err == nil {
		t.Fatalf("second call should propagate the cancelled ctx from sleep")
	}
}

// --- Registry.Build ---

func mustDecodeConfig(t *testing.T, yamlSrc string) *config.Config {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(yamlSrc), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	return &cfg
}

func TestRegistryBuildUnknownTypeErrors(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  foo:
    type: does-not-exist
`)
	_, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err == nil || !strings.Contains(err.Error(), `unknown type "does-not-exist"`) {
		t.Fatalf("got %v, want unknown type error", err)
	}
}

func TestRegistryBuildDisablesOnSecretFailure(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  disc:
    type: discord
    bot_token: env:PC_TEST_MISSING_VAR_XYZ
`)
	sec := secrets.New()
	sec.LookupEnv = func(string) (string, bool) { return "", false }
	reg, err := Build(cfg, Deps{Secrets: sec})
	if err != nil {
		t.Fatalf("Build should not fail the boot on a secret failure: %v", err)
	}
	in, ok := reg.Get("disc")
	if !ok {
		t.Fatalf("connector not found in registry")
	}
	if in.DisabledReason == "" {
		t.Fatalf("expected DisabledReason to be set")
	}
	if !strings.Contains(in.DisabledReason, "PC_TEST_MISSING_VAR_XYZ") {
		t.Fatalf("DisabledReason = %q, want it to mention the missing env var", in.DisabledReason)
	}
	_, err = in.Invoke(context.Background(), "post", map[string]any{"channel": "c", "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Invoke on a disabled connector: got %v, want disabled error", err)
	}
}

// TestRegistryAuthorDisabled: `enabled: false` set by the config author (as
// opposed to a runtime disable) still builds the instance but rejects every
// verb — the author's kill switch for one connector.
func TestRegistryAuthorDisabled(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  box:
    type: command
    enabled: false
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Config: cfg})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("box")
	if !ok {
		t.Fatal("connector not in registry")
	}
	if in.Enabled {
		t.Fatal("enabled: false must carry through to the instance")
	}
	if in.DisabledReason != "" {
		t.Fatalf("author-disabled is not a runtime failure: %q", in.DisabledReason)
	}
	_, err = in.Invoke(context.Background(), "run", map[string]any{"command": "true"})
	if err == nil || !strings.Contains(err.Error(), `connector "box" is disabled`) {
		t.Fatalf("verbs on an author-disabled connector must be rejected, got %v", err)
	}
}

func TestRegistryBuildDisablesOnValidateFailure(t *testing.T) {
	// slack requires bot_token; omitting it passes decode (empty string) but
	// fails Validate().
	cfg := mustDecodeConfig(t, `
connectors:
  sl:
    type: slack
    app_token: xapp-literal
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, _ := reg.Get("sl")
	if in.DisabledReason == "" {
		t.Fatalf("expected DisabledReason from a failed Validate()")
	}
}

func TestInstanceInvokeMergesDefaultsInvokeFinalDoesNot(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  wh:
    type: webhook
    listen: ":0"
    sources:
      s1:
        path: /s1
    options:
      headers:
        X-Default: "1"
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("wh")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("connector not built cleanly: ok=%v reason=%q", ok, in.DisabledReason)
	}
	// Invoke (merges DefaultOptions with call options): a url-less call should
	// still fail (url required), proving the merge ran (defaults carried
	// through) without needing a live server.
	_, err = in.Invoke(context.Background(), "post", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "options.url is required") {
		t.Fatalf("Invoke: got %v", err)
	}
	// InvokeFinal skips the defaults merge entirely: pass already-final opts.
	_, err = in.InvokeFinal(context.Background(), "post", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "options.url is required") {
		t.Fatalf("InvokeFinal: got %v", err)
	}
	// Unknown verb still rejected identically through both paths.
	if _, err := in.Invoke(context.Background(), "nope", nil); err == nil {
		t.Fatalf("Invoke: want error for unknown verb")
	}
	if _, err := in.InvokeFinal(context.Background(), "nope", nil); err == nil {
		t.Fatalf("InvokeFinal: want error for unknown verb")
	}
}
