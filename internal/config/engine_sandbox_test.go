package config

import (
	"strings"
	"testing"
)

// A code-step engine plugin with no `engines:` config is UNTRUSTED by
// default — but the declared-capability jail replaced the old synthesized
// best-effort namespace sandbox: PluginRefs no longer invents an isolation:
// block for it at all (that whole mechanism, and defaultEngineIsolation with
// it, is gone). Instead the engine plugin itself is jailed to exactly what
// IT declares (internal/plugin's buildJailed), the same as every other
// plugin: Isolation stays nil, IsolationDefaulted stays false, and TrustFull
// stays false unless the operator opts out with `trust: full`.
func TestEngineDefaultSandboxSynthesized(t *testing.T) {
	c, err := loadYAML(t, engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ref, ok := c.PluginRefs()["engines/wasmtime"]
	if !ok {
		t.Fatalf("no engine ref; got %v", keysOf(c.PluginRefs()))
	}
	if ref.TrustFull {
		t.Fatal("an engine with no config must not be TrustFull — it is jailed by default")
	}
	if ref.Isolation != nil {
		t.Fatalf("an engine's jail is its own declaration now, not a synthesized isolation: block: %+v", ref.Isolation)
	}
	if ref.IsolationDefaulted {
		t.Fatal("there is no more synthesized-default isolation for an engine (IsolationDefaulted must stay false)")
	}
}

// `engines: { <name>: { trust: full } }` opts the engine out of sandboxing: no
// synthesized isolation, and TrustFull marks it so the "no sandbox" warning stays
// quiet.
func TestEngineTrustFullOptsOut(t *testing.T) {
	c, err := loadYAML(t, `engines:
  wasmtime:
    trust: full
`+engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ref := c.PluginRefs()["engines/wasmtime"]
	if !ref.TrustFull {
		t.Fatal("trust: full must set TrustFull")
	}
	if ref.Isolation != nil {
		t.Fatalf("trust: full must not synthesize a sandbox, got %+v", ref.Isolation)
	}
	if ref.IsolationDefaulted {
		t.Fatal("trust: full is not a defaulted sandbox")
	}
}

// An explicit `isolation:` block on an engine is now a LOAD ERROR: the
// declared-capability jail replaced isolation: for every plugin (connector,
// runtime, or engine) — an engine's jail is what it declares, narrowed by
// network:/allow_env, or opted out of entirely with trust: full. There is no
// more "explicit block wins, synthesized default is best-effort" distinction
// to keep fail-closed; checkPluginJail refuses the key outright, naming it.
func TestEngineExplicitIsolationIsFailClosed(t *testing.T) {
	_, err := loadYAML(t, `engines:
  wasmtime:
    isolation: { mode: namespace }
`+engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err == nil {
		t.Fatal("isolation: on an engine must be refused at load — it no longer applies to a plugin")
	}
	if !strings.Contains(err.Error(), "engines.wasmtime") || !strings.Contains(err.Error(), "isolation") {
		t.Fatalf("error must name the engine and isolation:, got: %v", err)
	}
}

func TestEngineTrustInvalidRejected(t *testing.T) {
	_, err := loadYAML(t, `engines:
  wasmtime:
    trust: sorta
`+engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err == nil {
		t.Fatal("trust other than full/empty must be rejected")
	}
}

func TestEngineTrustAndIsolationConflict(t *testing.T) {
	_, err := loadYAML(t, `engines:
  wasmtime:
    trust: full
    isolation: { mode: namespace }
`+engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err == nil {
		t.Fatal("setting both trust: full and isolation must be rejected")
	}
}
