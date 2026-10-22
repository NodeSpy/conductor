package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func headlessDoc(runtimes string) string {
	return `
connectors:
  timer: { use: cron, schedules: { tick: { every: 1h } } }
runtimes:
` + runtimes + `
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`
}

// headless_mode loads as a mode id, "none", or a per-provider map, and
// reaches the primary dispatcher; unset is the zero value (the defaults).
func TestHeadlessModeLoads(t *testing.T) {
	for rt, want := range map[string]HeadlessMode{
		"  paseo: { use: paseo }":                                               {},
		"  paseo: { use: paseo, headless_mode: auto }":                          {All: "auto"},
		"  paseo: { use: paseo, headless_mode: none }":                          {All: "none"},
		"  paseo: { use: paseo, headless_mode: { claude: auto, codex: none } }": {Providers: map[string]string{"claude": "auto", "codex": "none"}},
	} {
		cfg, err := loadDoc(t, headlessDoc(rt))
		if err != nil {
			t.Errorf("%s: %v", rt, err)
			continue
		}
		if got := cfg.PrimaryHeadlessMode(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: primary headless mode = %+v, want %+v", rt, got, want)
		}
	}
}

// A malformed or misplaced headless_mode is a load error naming the runtime
// and what to write instead.
func TestHeadlessModeRejects(t *testing.T) {
	for rt, wantErr := range map[string]string{
		`  r: { use: cli, tool: claude-code, headless_mode: auto }`: "paseo runtime only",
		`  r: { use: acp, agent: gemini, headless_mode: none }`:     "paseo runtime only",
		`  r: { use: paseo, headless_mode: "" }`:                    "not a mode id",
		`  r: { use: paseo, headless_mode: "two words" }`:           "not a mode id",
		`  r: { use: paseo, headless_mode: --dangerously }`:         "not a mode id",
		`  r: { use: paseo, headless_mode: {} }`:                    "empty map",
		`  r: { use: paseo, headless_mode: { claude: "" } }`:        "headless_mode.claude",
		`  r: { use: paseo, headless_mode: { Claude: auto } }`:      "lowercase",
		`  r: { use: paseo, headless_mode: [auto] }`:                "headless_mode",
		`  r: { use: paseo, headless_mode: { claude: [auto] } }`:    "headless_mode",
	} {
		if _, err := loadDoc(t, headlessDoc(rt)); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: want error containing %q, got %v", rt, wantErr, err)
		}
	}
}

// Runtimes on the primary dispatcher (local, same bin) must agree — one
// dispatcher launches all their agents. A runtime with its own bin: or a
// host: has its own dispatcher and may differ.
func TestHeadlessModeSharedDispatcherMustAgree(t *testing.T) {
	if _, err := loadDoc(t, headlessDoc(`  a: { use: paseo, default: true, headless_mode: auto }
  b: { use: paseo }`)); err == nil || !strings.Contains(err.Error(), "share conductor's primary paseo dispatcher") {
		t.Fatalf("want the disagreement error, got %v", err)
	}
	if _, err := loadDoc(t, headlessDoc(`  a: { use: paseo, default: true, headless_mode: auto }
  b: { use: paseo, headless_mode: auto }`)); err != nil {
		t.Fatalf("agreeing runtimes must load: %v", err)
	}
	// (A runtime with no bin: shares whichever bin the primary resolves to,
	// so the default runtime names its own here.)
	cfg, err := loadDoc(t, headlessDoc(`  a: { use: paseo, bin: /usr/bin/paseo, default: true, headless_mode: auto }
  b: { use: paseo, bin: /opt/other/paseo, headless_mode: none }`))
	if err != nil {
		t.Fatalf("a runtime with its own bin: has its own dispatcher: %v", err)
	}
	if got := cfg.PrimaryHeadlessMode(); got.All != "auto" {
		t.Fatalf("primary = %+v, want the shared runtimes' auto", got)
	}
}

// Both shapes survive a marshal/unmarshal round trip.
func TestHeadlessModeRoundTrips(t *testing.T) {
	for _, h := range []HeadlessMode{{All: "auto"}, {All: "none"}, {Providers: map[string]string{"claude": "auto", "codex": "none"}}} {
		b, err := yaml.Marshal(RuntimeConfig{Use: "paseo", HeadlessMode: &h})
		if err != nil {
			t.Fatal(err)
		}
		var back RuntimeConfig
		if err := yaml.Unmarshal(b, &back); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if back.HeadlessMode == nil || !reflect.DeepEqual(*back.HeadlessMode, h) {
			t.Fatalf("round trip of %+v gave %+v (%s)", h, back.HeadlessMode, b)
		}
	}
}
