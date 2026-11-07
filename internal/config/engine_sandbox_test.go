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

// An explicit `isolation:` block on an engine loads with a warning: the
// engine is jailed to what it declares, and the block applies only where the
// host cannot jail plugins and the engine runs as before the jail.
func TestEngineExplicitIsolationWarns(t *testing.T) {
	cfg, err := loadYAML(t, `engines:
  wasmtime:
    isolation: { mode: namespace }
`+engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err != nil {
		t.Fatalf("isolation: on an engine refused at load: %v", err)
	}
	w := strings.Join(cfg.RemovedFieldWarnings(), "\n")
	if !strings.Contains(w, "engines.wasmtime.isolation") {
		t.Fatalf("no warning naming the engine's isolation:, got: %s", w)
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
