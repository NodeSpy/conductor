package config

import "testing"

// A code-step engine plugin with no `engines:` config is UNTRUSTED by default:
// PluginRefs synthesizes a best-effort namespace sandbox with the network denied.
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
	if !ref.IsolationDefaulted {
		t.Fatal("an engine with no config must be sandboxed-by-default (IsolationDefaulted)")
	}
	if ref.TrustFull {
		t.Fatal("default engine must not be TrustFull")
	}
	if ref.Isolation == nil || ref.Isolation.Mode != "namespace" {
		t.Fatalf("default sandbox should be mode namespace, got %+v", ref.Isolation)
	}
	if ref.Isolation.Network == nil || !ref.Isolation.Network.Deny {
		t.Fatalf("default sandbox should deny network, got %+v", ref.Isolation.Network)
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

// An explicit `isolation:` block is used verbatim and is NOT best-effort: it
// keeps the fail-closed posture (IsolationDefaulted stays false).
func TestEngineExplicitIsolationIsFailClosed(t *testing.T) {
	c, err := loadYAML(t, `engines:
  wasmtime:
    isolation: { mode: namespace }
`+engineWF+`      - { id: x, use: wasmtime, code: "x" }
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ref := c.PluginRefs()["engines/wasmtime"]
	if ref.Isolation == nil || ref.Isolation.Mode != "namespace" {
		t.Fatalf("explicit isolation should be used, got %+v", ref.Isolation)
	}
	if ref.IsolationDefaulted {
		t.Fatal("an operator-written isolation block must NOT be marked defaulted (it fails closed)")
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
