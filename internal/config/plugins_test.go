package config

import (
	"strings"
	"testing"
)

// PluginRefs is DERIVED, not authored: a connectors:/runtimes: entry whose
// `use:` resolves to a builtin contributes nothing, and everything else becomes
// exactly one plugin the daemon must run.
func TestPluginRefsDerivedFromUse(t *testing.T) {
	c := &Config{
		ConnectorsMap: map[string]ConnectorRef{
			"gh":      {Use: "github"},                           // builtin: not a plugin
			"tickets": {Use: "acme/plugins/jira"},                // explicit repo
			"alerts":  {Use: "sentry", Network: []string{"x:1"}}, // official repo
		},
		Runtimes: map[string]RuntimeConfig{
			"local": {Use: "paseo"},        // builtin: not a plugin
			"modal": {Use: "acme/p/modal"}, // plugin runtime
		},
	}
	refs := c.PluginRefs()
	if len(refs) != 3 {
		t.Fatalf("want 3 derived plugins, got %d: %v", len(refs), keysOf(refs))
	}
	jira, ok := refs["connectors/jira"]
	if !ok {
		t.Fatalf("jira not derived: %v", keysOf(refs))
	}
	if jira.Kind() != PluginKindConnector || jira.Name != "jira" || jira.Instance != "tickets" {
		t.Fatalf("jira ref = %+v", jira)
	}
	if jira.Source() != "github.com/acme/plugins//jira" {
		t.Fatalf("jira source = %q", jira.Source())
	}
	sentry := refs["connectors/sentry"]
	if len(sentry.Network) != 1 || sentry.Network[0] != "x:1" {
		t.Fatalf("declared network not carried: %+v", sentry)
	}
	// A runtime's MAP KEY is the runtime name agents select, so it names the
	// plugin even when the reference leaf differs.
	if _, ok := refs["runtimes/modal"]; !ok {
		t.Fatalf("runtime plugin not keyed by its runtimes: name: %v", keysOf(refs))
	}
	if refs["runtimes/modal"].Kind() != PluginKindRuntime {
		t.Fatalf("runtime plugin kind = %q", refs["runtimes/modal"].Kind())
	}
}

// Two instances of the same plugin fold into ONE entry (it runs once), unioning
// what they declare.
func TestPluginRefsFoldsInstances(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Network: []string{"one.example:443"}},
		"b": {Use: "acme/p/jira", Network: []string{"two.example:443"}},
	}}
	refs := c.PluginRefs()
	if len(refs) != 1 {
		t.Fatalf("two instances of one plugin produced %d entries", len(refs))
	}
	got := refs["connectors/jira"]
	if len(got.Network) != 2 {
		t.Fatalf("declared egress not unioned across instances: %+v", got.Network)
	}
}

// Two DIFFERENT sources claiming one implementation name would silently route
// one instance's credentials to the other's binary. Refused.
func TestValidatePluginRefsRejectsNameConflict(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira"},
		"b": {Use: "other/p/jira"},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("a name claimed by two sources was accepted")
	}
	if !strings.Contains(err.Error(), "different sources") {
		t.Fatalf("unhelpful conflict error: %v", err)
	}
	// The same source twice is fine.
	c.ConnectorsMap["b"] = ConnectorRef{Use: "acme/p/jira"}
	if err := c.validatePluginRefs(); err != nil {
		t.Fatalf("two instances of the SAME plugin were refused: %v", err)
	}
}

// A builtin never becomes a plugin, however many instances name it.
func TestPluginRefsIgnoresBuiltins(t *testing.T) {
	c := &Config{
		ConnectorsMap: map[string]ConnectorRef{"a": {Use: "github"}, "b": {Use: "slack"}},
		Runtimes:      map[string]RuntimeConfig{"r": {Use: "paseo"}},
	}
	if refs := c.PluginRefs(); len(refs) != 0 {
		t.Fatalf("builtins produced plugins: %v", keysOf(refs))
	}
}

// A local development binary is a plugin too — just one that is never fetched.
func TestPluginRefsLocalPath(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"dev": {Use: "./bin/conductor-jira"},
	}}
	refs := c.PluginRefs()
	got, ok := refs["connectors/jira"]
	if !ok {
		t.Fatalf("local plugin not derived: %v", keysOf(refs))
	}
	if got.IsRemote() || got.Source() != "" {
		t.Fatalf("a local binary must not be fetched or trust-gated: %+v", got)
	}
}

func keysOf(m map[string]PluginRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
