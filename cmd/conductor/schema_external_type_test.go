package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// Finding 6 (HIGH): `conductor schema <type>` checked connector.TypeDeclsFor
// BEFORE ever building the flow stack, but an EXTERNAL plugin type is only
// registered into that same lookup by building the stack (the instance path,
// a few lines below, already does this in the right order). So `conductor
// schema acme-echo` failed with "no connector configured (and no such
// type)" for every real external plugin that had no configured INSTANCE
// named "acme-echo" — exactly the bare-type-name case the docs promise
// works ("conductor schema <type>" shows each side-by-side version's
// declaration). These tests build the REAL test/plugins/acme-echo fixture
// (buildAcmeEchoPing, side_by_side_versions_test.go) and drive cmdSchema
// through the actual production path — no manual connector.Register* calls
// standing in for what loading the plugin is supposed to do — so they only
// pass once the ordering fix (building the stack before the bare-type
// lookup) is actually in place.

// writeAcmeEchoConfig writes a config with one or two connector instances
// of the acme-echo plugin, referenced by the SAME install key but each
// pinned to its own version (version == "" leaves both unpinned, landing on
// one shared install-state record / one process group).
func writeAcmeEchoConfig(t *testing.T, pins ...string) []string {
	t.Helper()
	doc := "connectors:\n"
	names := []string{"a", "b"}
	for i, pin := range pins {
		use := "acme/plugins/acme-echo"
		if pin != "" {
			use += "@=" + pin
		}
		doc += "  " + names[i] + ":\n    use: " + use + "\n"
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"--config", p}
}

// TestCmdSchemaBareExternalTypeWithOneGroup is finding 6's core
// reproduction: two configured instances of acme-echo sharing ONE resolved
// version (ONE process group, the ordinary case), looked up by its BARE
// TYPE NAME "acme-echo" — not an instance name — exactly `conductor schema
// acme-echo` with no `acme-echo:` entry in connectors:. Before the fix this
// failed outright, because the plugin's type is registered only by
// building the stack, which the old code never did before this lookup.
func TestCmdSchemaBareExternalTypeWithOneGroup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin, sum := buildAcmeEchoPing(t, false)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: "connectors/acme-echo", Kind: config.PluginKindConnector, Name: "acme-echo",
		Resolved: "acme-echo/v1.0.0", Sha256: sum, Path: bin})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	// Two instances, both unpinned — they resolve to the identical version,
	// one shared process group, never two.
	args := writeAcmeEchoConfig(t, "", "")

	out, err := captureStdout(t, func() error { return cmdSchema(append(append([]string(nil), args...), "acme-echo")) })
	if err != nil {
		t.Fatalf("schema acme-echo: %v", err)
	}
	if !strings.Contains(out, "reference echo connector (example plugin)") {
		t.Fatalf("schema acme-echo must print the plugin's own declaration, got:\n%s", out)
	}
	if !strings.Contains(out, "echo") {
		t.Fatalf("schema acme-echo must list its echo verb, got:\n%s", out)
	}
	// One group only: no version-header banner.
	if strings.Contains(out, "version 1 of") {
		t.Fatalf("a single resolved group must not print a side-by-side version header, got:\n%s", out)
	}
}

// TestCmdSchemaBareExternalTypeSideBySideGroups is finding 6's side-by-side
// half: two instances pinned to DIFFERENT versions of acme-echo — two
// process groups, two declarations (one with "ping", one without, like
// side_by_side_versions_test.go) — looked up by the bare type name must
// print BOTH under their own version header, never just whichever group
// happened to build first.
func TestCmdSchemaBareExternalTypeSideBySideGroups(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	binV1, sumV1 := buildAcmeEchoPing(t, false)
	binV2, sumV2 := buildAcmeEchoPing(t, true)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: "connectors/acme-echo", Kind: config.PluginKindConnector, Name: "acme-echo",
		Resolved: "acme-echo/v1.0.0", Sha256: sumV1, Path: binV1})
	state.Put(plugin.Installed{Key: "connectors/acme-echo", Kind: config.PluginKindConnector, Name: "acme-echo",
		Resolved: "acme-echo/v2.0.0", Sha256: sumV2, Path: binV2})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	args := writeAcmeEchoConfig(t, "1.0.0", "2.0.0")

	out, err := captureStdout(t, func() error { return cmdSchema(append(append([]string(nil), args...), "acme-echo")) })
	if err != nil {
		t.Fatalf("schema acme-echo: %v", err)
	}
	if !strings.Contains(out, "version 1 of 2") || !strings.Contains(out, "version 2 of 2") {
		t.Fatalf("schema acme-echo must print both side-by-side groups under version headers, got:\n%s", out)
	}
	if strings.Count(out, "reference echo connector (example plugin)") != 2 {
		t.Fatalf("expected the plugin's declaration printed once per group, got:\n%s", out)
	}
	if !strings.Contains(out, "pong") {
		t.Fatalf("the v2 group's ping verb must be shown (declares \"pong\" output), got:\n%s", out)
	}
}

// TestCmdSchemaUnknownTypeHintListsExternalTypes is finding 6's "types:"
// hint half: once a plugin's type IS registered (by building the stack),
// the "no such type" hint for an UNRELATED unknown name must list it,
// labelled external — not just the builtins, which is all the hint could
// ever have shown before the ordering fix (the stack was never built for
// this lookup at all).
func TestCmdSchemaUnknownTypeHintListsExternalTypes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin, sum := buildAcmeEchoPing(t, false)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: "connectors/acme-echo", Kind: config.PluginKindConnector, Name: "acme-echo",
		Resolved: "acme-echo/v1.0.0", Sha256: sum, Path: bin})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	args := writeAcmeEchoConfig(t, "")

	_, err := captureStdout(t, func() error { return cmdSchema(append(append([]string(nil), args...), "no-such-type-at-all")) })
	if err == nil {
		t.Fatal("expected an error for a genuinely unknown type")
	}
	if !strings.Contains(err.Error(), "acme-echo (external)") {
		t.Fatalf("the types hint must list acme-echo as external, got: %v", err)
	}
}
