package config

import (
	"strings"
	"testing"
)

// checkPluginJail is the declared-capability jail's config-time gate
// (plugins.go): the old isolation: surface on a plugin connector, runtime or
// engine is refused outright at load (the plugin's own declaration is its
// jail now), a trust: value other than "full" or empty is refused, and a
// shared process whose non-isolated instances disagree on trust: full is
// refused — isolate: true on one of them resolves it by giving it its own
// process.

func TestCheckPluginJailRefusesIsolationOnConnector(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "namespace"}},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("isolation: on a plugin connector was accepted")
	}
	if !strings.Contains(err.Error(), "connectors.a") {
		t.Fatalf("error must name the key: %v", err)
	}
	if !strings.Contains(err.Error(), "isolation") {
		t.Fatalf("error must name isolation: as the problem: %v", err)
	}
}

func TestCheckPluginJailRefusesIsolationOnRuntime(t *testing.T) {
	c := &Config{Runtimes: map[string]RuntimeConfig{
		"modal": {Use: "acme/p/modal", Isolation: &IsolationConfig{Mode: "namespace"}},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("isolation: on a plugin runtime was accepted")
	}
	if !strings.Contains(err.Error(), "runtimes.modal") {
		t.Fatalf("error must name the key: %v", err)
	}
	if !strings.Contains(err.Error(), "isolation") {
		t.Fatalf("error must name isolation: as the problem: %v", err)
	}
}

func TestCheckPluginJailRefusesIsolationOnEngine(t *testing.T) {
	c := &Config{Engines: map[string]EngineConfig{
		"wasmtime": {Isolation: &IsolationConfig{Mode: "namespace"}},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("isolation: on a plugin engine was accepted")
	}
	if !strings.Contains(err.Error(), "engines.wasmtime") {
		t.Fatalf("error must name the key: %v", err)
	}
	if !strings.Contains(err.Error(), "isolation") {
		t.Fatalf("error must name isolation: as the problem: %v", err)
	}
}

func TestCheckPluginJailRefusesBadTrustValue(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Trust: "sorta"},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("a bad trust: value was accepted")
	}
	if !strings.Contains(err.Error(), "connectors.a") || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("error must name the key and trust: %v", err)
	}
	// Empty and "full" (any case) are the only valid values.
	for _, ok := range []string{"", "full", "FULL", "Full"} {
		c2 := &Config{ConnectorsMap: map[string]ConnectorRef{"a": {Use: "acme/p/jira", Trust: ok}}}
		if err := c2.validatePluginRefs(); err != nil {
			t.Fatalf("trust: %q should be accepted: %v", ok, err)
		}
	}
}

func TestCheckPluginJailRefusesBadTrustValueOnRuntime(t *testing.T) {
	c := &Config{Runtimes: map[string]RuntimeConfig{
		"modal": {Use: "acme/p/modal", Trust: "sorta"},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("a bad trust: value on a runtime was accepted")
	}
	if !strings.Contains(err.Error(), "runtimes.modal") || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("error must name the key and trust: %v", err)
	}
}

// Two non-isolated instances of one plugin share a single process by
// default: if they disagree on trust: full, that one process cannot be both
// jailed and unjailed, so this is a load error. isolate: true on either one
// removes the conflict by giving it its own process.
func TestCheckPluginJailRefusesMixedTrustOnSharedProcess(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Trust: "full"},
		"b": {Use: "acme/p/jira"},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("two non-isolated instances disagreeing on trust: full were accepted")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("error must name both connectors: %v", err)
	}

	// isolate: true on the non-full instance resolves it: it gets its own
	// process instead of sharing a's trust: full.
	c.ConnectorsMap["b"] = ConnectorRef{Use: "acme/p/jira", Isolate: true}
	if err := c.validatePluginRefs(); err != nil {
		t.Fatalf("isolate: true on the mismatched instance must resolve it: %v", err)
	}

	// Agreeing (both full, or both empty) shares fine.
	c.ConnectorsMap["b"] = ConnectorRef{Use: "acme/p/jira", Trust: "full"}
	if err := c.validatePluginRefs(); err != nil {
		t.Fatalf("two instances agreeing on trust: full must share a process: %v", err)
	}
}
