package plugin

import (
	"os"
	"strings"
	"testing"
)

// CAN'T-EXCEED-DECLARATION: a connector's `network:` may narrow what the plugin
// declared, never widen it.
func TestCheckNetworkWithinManifest(t *testing.T) {
	declared := []string{"*.atlassian.net:443", "api.acme.dev"}

	for _, ok := range [][]string{
		{"acme.atlassian.net:443"},                   // glob host, exact port
		{"api.acme.dev:443"},                         // declared with no port: any port
		{"api.acme.dev:8080"},                        //
		{"acme.atlassian.net:443", "api.acme.dev:1"}, // several, all covered
		nil, // declaring nothing narrows nothing
	} {
		if err := CheckNetworkWithinManifest("jira", declared, ok); err != nil {
			t.Errorf("network %v should be within %v: %v", ok, declared, err)
		}
	}

	for _, bad := range [][]string{
		{"evil.example:443"},                     // host not declared at all
		{"acme.atlassian.net:22"},                // declared host, undeclared port
		{"acme.atlassian.net:443", "evil.dev:1"}, // one good, one not
	} {
		err := CheckNetworkWithinManifest("jira", declared, bad)
		if err == nil {
			t.Errorf("network %v exceeded %v but was accepted", bad, declared)
			continue
		}
		if !strings.Contains(err.Error(), "never widen") {
			t.Errorf("unhelpful widening error for %v: %v", bad, err)
		}
	}
}

// A plugin that declares NO egress is "unspecified", not "denies everything":
// refusing every network: against a terse or older plugin would break working
// setups to enforce a declaration that was never made.
func TestCheckNetworkAgainstUndeclaredPlugin(t *testing.T) {
	if err := CheckNetworkWithinManifest("old", nil, []string{"anything:443"}); err != nil {
		t.Fatalf("a plugin that declared no egress should not refuse a config network:: %v", err)
	}
}

// The effective manifest is what the plugin actually runs with: its own
// declaration, narrowed by the connector's network: when one is set.
func TestEffectiveManifest(t *testing.T) {
	s := Spec{
		Manifest: Manifest{
			Egress:   []string{"*.atlassian.net:443"},
			Commands: []string{"git"},
		},
	}
	if got := s.EffectiveManifest(); len(got.Egress) != 1 || got.Egress[0] != "*.atlassian.net:443" {
		t.Fatalf("with no network:, the plugin's own declaration should stand: %+v", got)
	}

	s.Network = []string{"acme.atlassian.net:443"}
	got := s.EffectiveManifest()
	if len(got.Egress) != 1 || got.Egress[0] != "acme.atlassian.net:443" {
		t.Fatalf("network: did not narrow the effective egress: %+v", got)
	}
	if len(got.Commands) != 1 || got.Commands[0] != "git" {
		t.Fatalf("narrowing egress must not touch declared commands: %+v", got)
	}
}

func TestManifestSummary(t *testing.T) {
	if s := (Manifest{}).Summary(); s != "no declared capabilities" {
		t.Fatalf("empty manifest summary = %q", s)
	}
	m := Manifest{Egress: []string{"a:1"}, Commands: []string{"git", "gh"}, FS: []string{"/tmp"}}
	s := m.Summary()
	for _, want := range []string{"network a:1", "commands git,gh", "fs /tmp"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary %q missing %q", s, want)
		}
	}
	// "I spawn things I am not naming" is surfaced AS SUCH rather than as an
	// empty command list, because it is exactly what cannot be confined.
	if s := (Manifest{Spawns: true}).Summary(); !strings.Contains(s, "unnamed") {
		t.Fatalf("an unnamed-spawn declaration should be visible: %q", s)
	}
}

func TestHostGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, host string
		want          bool
	}{
		{"*", "anything", true},
		{"api.acme.dev", "api.acme.dev", true},
		{"api.acme.dev", "evil.acme.dev", false},
		{"*.acme.dev", "api.acme.dev", true},
		{"*.acme.dev", "acme.dev", false},
		{"*.acme.dev", "api.acme.dev.evil.com", false},
		{"api.*", "api.acme.dev", true},
	} {
		if got := hostGlobMatch(tc.pattern, tc.host); got != tc.want {
			t.Errorf("hostGlobMatch(%q,%q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
		}
	}
}

// A plugin declaring commands it cannot name gets NO path rewrite — conductor
// does not pretend to a confinement it is not performing.
func TestUnnamedSpawnsGetNoPathConfinement(t *testing.T) {
	dir := t.TempDir()
	bin := writeBin(t, dir, "b", []byte("x"), 0o755)
	s := Spec{Name: "p", BinPath: bin, Manifest: Manifest{Spawns: true}}
	got, err := commandPathDir(s)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("an unnamed-spawn plugin got a confined PATH (%q) — that would be a false claim", got)
	}
}

// A declared command that is not installed on this box is simply absent from
// the confined PATH; the plugin finds out when it tries to use it, and the
// install does not fail over it.
func TestMissingDeclaredCommandIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	bin := writeBin(t, dir, "b", []byte("x"), 0o755)
	s := Spec{Name: "p", BinPath: bin, Manifest: Manifest{Commands: []string{"definitely-not-installed-xyz"}}}
	got, err := commandPathDir(s)
	if err != nil {
		t.Fatalf("a missing declared command must not fail the launch: %v", err)
	}
	if got == "" {
		t.Fatal("expected a (empty) confined PATH dir")
	}
}

// A declared "command" containing a path separator is not a command NAME and is
// ignored rather than symlinked somewhere unexpected.
func TestDeclaredCommandPathsIgnored(t *testing.T) {
	dir := t.TempDir()
	bin := writeBin(t, dir, "b", []byte("x"), 0o755)
	s := Spec{Name: "p", BinPath: bin, Manifest: Manifest{Commands: []string{"../../bin/sh", "/bin/sh"}}}
	got, err := commandPathDir(s)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("expected a confined PATH dir")
	}
	// Nothing was linked: both entries were paths, not names.
	if entries, _ := readDirNames(got); len(entries) != 0 {
		t.Fatalf("path-shaped declarations were linked: %v", entries)
	}
}

// readDirNames lists a directory's entries by name.
func readDirNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, nil
}
