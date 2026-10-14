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
			"hook":    {Use: "webhook"},                          // builtin: not a plugin
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

// TestPluginRefsUnionDeduplicatesAcrossInstances is item 7's test gap: the
// same network host/secret/env declared by MORE THAN ONE instance must
// appear exactly once in the union (appendUnique) — not once per instance
// that declared it. Also confirms Instances carries each instance's OWN,
// never-unioned grant (config.PluginRef.Instances, the shape
// Manager.InstanceClient/plugin.InstanceSpec actually confine a per-instance
// process to), proven here with two instances declaring DIFFERENT networks.
func TestPluginRefsUnionDeduplicatesAcrossInstances(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {
			Use: "acme/p/jira", Network: []string{"shared.example:443", "one.example:443"},
			AllowSecrets: []string{"JIRA_TOKEN"}, AllowEnv: []string{"HTTP_PROXY"},
		},
		"b": {
			Use: "acme/p/jira", Network: []string{"shared.example:443", "two.example:443"},
			AllowSecrets: []string{"JIRA_TOKEN"}, AllowEnv: []string{"HTTP_PROXY"},
		},
	}}
	refs := c.PluginRefs()
	got := refs["connectors/jira"]

	if len(got.Network) != 3 {
		t.Fatalf("union Network = %v, want 3 entries (shared host deduplicated, not doubled)", got.Network)
	}
	for _, want := range []string{"shared.example:443", "one.example:443", "two.example:443"} {
		found := false
		for _, n := range got.Network {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("union Network missing %q: %v", want, got.Network)
		}
	}
	if len(got.AllowSecrets) != 1 || got.AllowSecrets[0] != "JIRA_TOKEN" {
		t.Fatalf("AllowSecrets declared identically by both instances must dedupe to one entry: %v", got.AllowSecrets)
	}
	if len(got.AllowEnv) != 1 || got.AllowEnv[0] != "HTTP_PROXY" {
		t.Fatalf("AllowEnv declared identically by both instances must dedupe to one entry: %v", got.AllowEnv)
	}

	// Per-instance grants are NEVER unioned — each instance's own entry in
	// Instances carries exactly what THAT connectors: entry declared.
	if len(got.Instances) != 2 {
		t.Fatalf("Instances = %v, want exactly 2 (one per configured instance)", got.Instances)
	}
	a, ok := got.Instances["a"]
	if !ok || len(a.Network) != 2 || a.Network[0] != "shared.example:443" || a.Network[1] != "one.example:443" {
		t.Fatalf("instance a's own grant must be exactly what it declared, not the union: %+v", a)
	}
	b, ok := got.Instances["b"]
	if !ok || len(b.Network) != 2 || b.Network[0] != "shared.example:443" || b.Network[1] != "two.example:443" {
		t.Fatalf("instance b's own grant must be exactly what it declared, not the union: %+v", b)
	}
}

// TestPluginRefsSingleInstanceGrantMatchesUnion is item 7's other gap: for a
// plugin with only ONE configured instance, that instance's own Instances
// entry must still be populated (not left nil/empty just because there is
// nothing to union against) and must equal the top-level union fields
// exactly — a single-instance config is the common case, and
// Manager.InstanceClient reads Instances[name], not the union fields, to
// confine that instance's own process.
func TestPluginRefsSingleInstanceGrantMatchesUnion(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"solo": {
			Use: "acme/p/jira", Network: []string{"solo.example:443"},
			AllowSecrets: []string{"JIRA_TOKEN"}, AllowEnv: []string{"HTTP_PROXY"},
		},
	}}
	refs := c.PluginRefs()
	got := refs["connectors/jira"]

	if len(got.Instances) != 1 {
		t.Fatalf("Instances = %v, want exactly 1 entry for a single-instance config", got.Instances)
	}
	solo, ok := got.Instances["solo"]
	if !ok {
		t.Fatalf("instance %q missing from Instances: %+v", "solo", got.Instances)
	}
	if len(solo.Network) != 1 || solo.Network[0] != "solo.example:443" {
		t.Fatalf("solo's own grant Network = %v, want [solo.example:443]", solo.Network)
	}
	if len(solo.AllowSecrets) != 1 || solo.AllowSecrets[0] != "JIRA_TOKEN" {
		t.Fatalf("solo's own grant AllowSecrets = %v", solo.AllowSecrets)
	}
	if len(solo.AllowEnv) != 1 || solo.AllowEnv[0] != "HTTP_PROXY" {
		t.Fatalf("solo's own grant AllowEnv = %v", solo.AllowEnv)
	}
	// With only one instance, the union fields and that instance's own grant
	// must agree exactly.
	if len(got.Network) != len(solo.Network) || got.Network[0] != solo.Network[0] {
		t.Fatalf("single-instance union Network %v must match the instance's own grant %v", got.Network, solo.Network)
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

// TestPluginRefsExcludesIsolatedFromUnion is the process-model inversion's
// core guarantee at the config layer: isolate: true on one instance must
// drop it out of the union Network/AllowSecrets/AllowEnv entirely — that
// union is what the ONE SHARED process (serving every OTHER, non-isolated
// instance) gets, and an isolated instance's own process must never widen
// (or narrow) that grant, nor vice versa.
func TestPluginRefsExcludesIsolatedFromUnion(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"shared": {Use: "acme/p/jira", Network: []string{"shared.example:443"}},
		"solo":   {Use: "acme/p/jira", Network: []string{"isolated.example:443"}, Isolate: true},
	}}
	refs := c.PluginRefs()
	got := refs["connectors/jira"]
	if len(got.Network) != 1 || got.Network[0] != "shared.example:443" {
		t.Fatalf("union Network must exclude the isolated instance's own: got %v", got.Network)
	}
	if !got.HasSharedInstance() {
		t.Fatal("one non-isolated instance means a shared process must exist")
	}
	if shared := got.SharedInstanceNames(); len(shared) != 1 || shared[0] != "shared" {
		t.Fatalf("SharedInstanceNames = %v, want [shared]", shared)
	}
	if iso := got.IsolatedInstanceNames(); len(iso) != 1 || iso[0] != "solo" {
		t.Fatalf("IsolatedInstanceNames = %v, want [solo]", iso)
	}
	solo, ok := got.Instances["solo"]
	if !ok || !solo.Isolate || len(solo.Network) != 1 || solo.Network[0] != "isolated.example:443" {
		t.Fatalf("solo's own grant must be carried unchanged: %+v", solo)
	}
}

// TestPluginRefsAllInstancesIsolatedHasNoSharedProcess: when EVERY configured
// instance of a connector plugin isolates, there is nothing left for a
// shared process to serve — HasSharedInstance must say so, since that is
// what decides whether SpecFromRef/Manager ever starts one for this key at
// all (the Probe/throwaway-describe path otherwise).
func TestPluginRefsAllInstancesIsolatedHasNoSharedProcess(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Isolate: true},
		"b": {Use: "acme/p/jira", Isolate: true},
	}}
	refs := c.PluginRefs()
	got := refs["connectors/jira"]
	if got.HasSharedInstance() {
		t.Fatal("every instance isolates; HasSharedInstance must be false")
	}
	if len(got.Network) != 0 {
		t.Fatalf("with no shared instance, the union must stay empty: %v", got.Network)
	}
}

// TestCheckIsolationMergeRefusesIncompatibleBlocks is the isolation-block
// merge rule (config.combineIsolation/checkIsolationMerge): two NON-isolated
// instances of the same plugin share one process, so their isolation:
// blocks must actually combine into one the process can run under. Two
// blocks that differ in anything but `network:` (mode, here) have no
// natural join — namespace and container are different sandboxes, not two
// points on a shared strictness scale — so this is a load error naming both
// connectors and the plugin, not a silent pick-one.
func TestCheckIsolationMergeRefusesIncompatibleBlocks(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "namespace"}},
		"b": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "container", Container: &ContainerIsolation{Image: "x"}}},
	}}
	err := c.validatePluginRefs()
	if err == nil {
		t.Fatal("two non-isolated instances with incompatible isolation: blocks were accepted")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") || !strings.Contains(err.Error(), "jira") {
		t.Fatalf("error must name both connectors and the plugin: %v", err)
	}
	// isolate: true on one of them removes the conflict: it gets its own
	// process instead of sharing a's.
	c.ConnectorsMap["b"] = ConnectorRef{Use: "acme/p/jira", Isolation: c.ConnectorsMap["b"].Isolation, Isolate: true}
	if err := c.validatePluginRefs(); err != nil {
		t.Fatalf("isolate: true on the conflicting instance must resolve it: %v", err)
	}
}

// TestCheckIsolationMergeCombinesNetwork is the merge rule's other half: two
// non-isolated instances whose isolation: blocks are identical except for
// `network:` must combine — advisory egress lists union, exactly like the
// plain Network/AllowSecrets/AllowEnv union above them.
func TestCheckIsolationMergeCombinesNetwork(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Egress: []string{"a.example:443"}}}},
		"b": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Egress: []string{"b.example:443"}}}},
	}}
	if err := c.validatePluginRefs(); err != nil {
		t.Fatalf("compatible isolation: blocks differing only in network: must combine: %v", err)
	}
	refs := c.PluginRefs()
	got := refs["connectors/jira"]
	if got.Isolation == nil || got.Isolation.Network == nil {
		t.Fatalf("merged isolation must carry a combined network block: %+v", got.Isolation)
	}
	if len(got.Isolation.Network.Egress) != 2 {
		t.Fatalf("merged egress must union both instances': %v", got.Isolation.Network.Egress)
	}
}

// A deny is a promise of no network beyond the instance's own allowlist: a
// sibling that shares its process must make the same promise. Otherwise the
// sibling's egress would open a path the denying instance never asked for.
func TestCheckIsolationMergeRefusesADenyMismatch(t *testing.T) {
	deny := &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true}}
	for name, sibling := range map[string]*IsolationConfig{
		"sibling egress":     {Mode: "namespace", Network: &IsolationNetwork{Egress: []string{"evil.example:443"}}},
		"sibling no block":   nil,
		"sibling no net":     {Mode: "namespace"},
		"deny, other egress": {Mode: "namespace", Network: &IsolationNetwork{Deny: true, Egress: []string{"evil.example:443"}}},
	} {
		t.Run(name, func(t *testing.T) {
			c := &Config{ConnectorsMap: map[string]ConnectorRef{
				"a": {Use: "acme/p/jira", Isolation: deny},
				"b": {Use: "acme/p/jira", Isolation: sibling},
			}}
			if err := c.validatePluginRefs(); err == nil {
				got := c.PluginRefs()["connectors/jira"].Isolation
				t.Fatalf("a deny merged with %s was accepted: shared process runs under %+v", name, got)
			}
		})
	}
	t.Run("identical deny blocks share", func(t *testing.T) {
		c := &Config{ConnectorsMap: map[string]ConnectorRef{
			"a": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true, Egress: []string{"x.example:443"}}}},
			"b": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true, Egress: []string{"x.example:443"}}}},
		}}
		if err := c.validatePluginRefs(); err != nil {
			t.Fatalf("identical deny blocks must share a process: %v", err)
		}
		got := c.PluginRefs()["connectors/jira"].Isolation.Network
		if !got.Deny || len(got.Egress) != 1 {
			t.Fatalf("merged network must stay the shared deny block: %+v", got)
		}
	})
}

// Mode alone differing is a conflict too (namespace and user are different
// sandboxes, not points on one scale).
func TestCheckIsolationMergeRefusesAModeOnlyMismatch(t *testing.T) {
	c := &Config{ConnectorsMap: map[string]ConnectorRef{
		"a": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "namespace"}},
		"b": {Use: "acme/p/jira", Isolation: &IsolationConfig{Mode: "user"}},
	}}
	if err := c.validatePluginRefs(); err == nil {
		t.Fatal("namespace and user isolation were merged into one process")
	}
}

// A builtin never becomes a plugin, however many instances name it.
func TestPluginRefsIgnoresBuiltins(t *testing.T) {
	c := &Config{
		ConnectorsMap: map[string]ConnectorRef{"a": {Use: "webhook"}, "b": {Use: "cron"}},
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
