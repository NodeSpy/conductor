package config

import (
	"strings"
	"testing"
)

// checkPluginJail is the declared-capability jail's config-time gate
// (plugins.go): a trust: value other than "full" or empty is refused, and a
// shared process whose non-isolated instances disagree on trust: full is
// refused — isolate: true on one of them resolves it by giving it its own
// process. What a config written before the jail says loads with a warning.

// A config written before the jail loads: isolation: on a plugin
// connector, runtime or engine warns, naming the key and what replaces it,
// and never refuses (an upgrade never breaks a working setup).
func TestIsolationOnPluginWarnsNeverRefuses(t *testing.T) {
	iso := &IsolationConfig{Mode: "namespace"}
	for where, c := range map[string]*Config{
		"connectors.a":     {ConnectorsMap: map[string]ConnectorRef{"a": {Use: "acme/p/jira", Isolation: iso}}},
		"runtimes.modal":   {Runtimes: map[string]RuntimeConfig{"modal": {Use: "acme/p/modal", Isolation: iso}}},
		"engines.wasmtime": {Engines: map[string]EngineConfig{"wasmtime": {Isolation: iso}}},
	} {
		if err := c.validatePluginRefs(); err != nil {
			t.Errorf("%s: isolation: refused: %v", where, err)
		}
		w := strings.Join(c.pluginJailWarnings(), "\n")
		for _, want := range []string{where + ".isolation", "no longer applies", "trust: full", "Upgrading"} {
			if !strings.Contains(w, want) {
				t.Errorf("%s: warning lacks %q: %s", where, want, w)
			}
		}
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

// Instances sharing one process that narrow it differently load (the
// process gets the union, as before the jail) with a warning naming both;
// isolate: true on one, or the same narrowing in any order, warns nothing.
func TestDifferingNarrowingOnSharedProcessWarns(t *testing.T) {
	for name, pair := range map[string][2]ConnectorRef{
		"network":         {{Use: "acme/p/jira", Network: []string{"a.example:443"}}, {Use: "acme/p/jira"}},
		"allow_env":       {{Use: "acme/p/jira", AllowEnv: []string{"X"}}, {Use: "acme/p/jira", AllowEnv: []string{"Y"}}},
		"empty vs unset":  {{Use: "acme/p/jira", AllowEnv: []string{}}, {Use: "acme/p/jira"}},
		"network ordered": {{Use: "acme/p/jira", Network: []string{"a.example:443", "b.example:443"}}, {Use: "acme/p/jira", Network: []string{"c.example:443"}}},
	} {
		c := &Config{ConnectorsMap: map[string]ConnectorRef{"a": pair[0], "b": pair[1]}}
		if err := c.validatePluginRefs(); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if w := strings.Join(c.pluginJailWarnings(), "\n"); !strings.Contains(w, "narrow it differently") || !strings.Contains(w, "a and b") {
			t.Errorf("%s: no warning naming both: %q", name, w)
		}
		pair[1].Isolate = true
		c = &Config{ConnectorsMap: map[string]ConnectorRef{"a": pair[0], "b": pair[1]}}
		if w := c.pluginJailWarnings(); len(w) != 0 {
			t.Errorf("%s with isolate: true on one: %v", name, w)
		}
	}
	same := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Network: []string{"b.example:443", "a.example:443"}},
		"b": {Use: "acme/p/jira", Network: []string{"a.example:443", "b.example:443"}},
	}}
	if w := same.pluginJailWarnings(); len(w) != 0 {
		t.Fatalf("the same narrowing in another order: %v", w)
	}
	// The union is what the shared process gets.
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Network: []string{"a.example:443"}},
		"b": {Use: "acme/p/jira", Network: []string{"b.example:443"}},
	}}
	if got := c.PluginRefs()["connectors/jira"].Network; len(got) != 2 {
		t.Fatalf("shared process network = %v, want both", got)
	}
}

// `allow_env: []` survives load as set-but-empty (passes nothing, as it
// always did); `network: []` loads as unset (every declared destination, as
// before the jail); absent stays unset.
func TestEmptyNarrowingSurvivesLoad(t *testing.T) {
	c := decodeConfig(t, `
connectors:
  a:
    use: acme/p/jira
    allow_env: []
    network: []
  b:
    use: acme/p/other
`)
	refs := c.PluginRefs()
	a := refs["connectors/jira"]
	if a.AllowEnv == nil || a.Instances["a"].AllowEnv == nil {
		t.Fatalf("allow_env: [] lost: %+v", a)
	}
	if a.Network != nil || a.Instances["a"].Network != nil {
		t.Fatalf("network: [] narrows: %+v", a)
	}
	b := refs["connectors/other"]
	if b.AllowEnv != nil || b.Network != nil || b.Instances["b"].AllowEnv != nil {
		t.Fatalf("absent became set: %+v", b)
	}
}
