package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCfg drops a config in a temp dir and returns the --config args for it.
func writeCfg(t *testing.T, doc string) []string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"--config", p}
}

// `plugin list` answers "what is available, and where does it come from?" for
// builtins and plugins alike — the ORIGIN column is the point.
func TestCmdPluginListShowsOriginAndKind(t *testing.T) {
	args := writeCfg(t, `
connectors:
  gh: { use: github }
  tickets: { use: acme/plugins/jira }
runtimes:
  local: { use: paseo, default: true }
`)
	out, err := captureStdout(t, func() error { return cmdPluginList(args) })
	if err != nil {
		t.Fatal(err)
	}
	// A builtin connector and a builtin runtime, both tagged builtin.
	for _, want := range []string{"github", "paseo", "builtin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plugin list missing %q:\n%s", want, out)
		}
	}
	// The plugin shows its origin and that it is not installed — a direction,
	// not a crash.
	if !strings.Contains(out, "jira") || !strings.Contains(out, "github") {
		t.Fatalf("plugin list missing the jira plugin:\n%s", out)
	}
	if !strings.Contains(out, "not installed") {
		t.Fatalf("an uninstalled plugin should say so:\n%s", out)
	}
}

// `plugin list --caps` surfaces the permission manifest — what the operator
// accepted when they added the plugin.
func TestCmdPluginListCaps(t *testing.T) {
	args := writeCfg(t, "connectors:\n  tickets: { use: acme/plugins/jira }\n")
	out, err := captureStdout(t, func() error {
		return cmdPluginList(append(args, "--caps"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "permissions:") {
		t.Fatalf("--caps did not surface the permission manifest:\n%s", out)
	}
	if !strings.Contains(out, "use: acme/plugins/jira") {
		t.Fatalf("--caps did not surface the reference:\n%s", out)
	}
}

// TestCmdPluginListCapsDefaultSharesUnionAsOneProcess is the NEW default:
// `plugin list --caps` on a connector plugin configured as two instances with
// DIFFERENT `network:`, neither isolate: true, must show ONE shared-process
// line carrying the UNION of both — that one process is what actually runs
// for both of them (docs/wiki/Plugins.md "Multi-instance isolation") — never
// split per instance, since there is no separate process to split per
// instance in the first place.
func TestCmdPluginListCapsDefaultSharesUnionAsOneProcess(t *testing.T) {
	args := writeCfg(t, `
connectors:
  a: { use: acme/plugins/jira, network: ["one.example:443"] }
  b: { use: acme/plugins/jira, network: ["two.example:443"] }
`)
	out, err := captureStdout(t, func() error {
		return cmdPluginList(append(args, "--caps"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "declared capabilities:") {
		t.Fatalf("--caps did not surface the plugin's declared capabilities:\n%s", out)
	}
	if !strings.Contains(out, "shared process") {
		t.Fatalf("--caps did not label the default shared-process grant:\n%s", out)
	}
	if !strings.Contains(out, "one.example:443") || !strings.Contains(out, "two.example:443") {
		t.Fatalf("--caps lost one instance's network from the union:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "shared process") {
			if !strings.Contains(line, "one.example:443") || !strings.Contains(line, "two.example:443") {
				t.Fatalf("the shared-process grant line did not carry the union of both instances' network:\n%s", out)
			}
			if !strings.Contains(line, "a") || !strings.Contains(line, "b") {
				t.Fatalf("the shared-process grant line did not label which instances it serves:\n%s", out)
			}
		}
	}
	if strings.Contains(out, "own process") {
		t.Fatalf("neither instance isolates; --caps must show no own-process line:\n%s", out)
	}
}

// TestCmdPluginListCapsIsolateShowsOwnProcessGrant is the isolate: true half:
// one isolated instance's grant must show on its OWN line, narrowed to
// exactly what it declared, separate from the shared process the other
// (non-isolated) instance still uses — never unioned together onto one line.
func TestCmdPluginListCapsIsolateShowsOwnProcessGrant(t *testing.T) {
	args := writeCfg(t, `
connectors:
  a: { use: acme/plugins/jira, network: ["one.example:443"], isolate: true }
  b: { use: acme/plugins/jira, network: ["two.example:443"] }
`)
	out, err := captureStdout(t, func() error {
		return cmdPluginList(append(args, "--caps"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "instance a (isolate: true, own process): ") {
		t.Fatalf("--caps did not label instance a's own isolated process:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "instance a") {
			if !strings.Contains(line, "one.example:443") || strings.Contains(line, "two.example:443") {
				t.Fatalf("instance a's own grant must carry only its own network, not b's:\n%s", out)
			}
		}
		if strings.Contains(line, "shared process") {
			if !strings.Contains(line, "two.example:443") || strings.Contains(line, "one.example:443") {
				t.Fatalf("the shared process must carry only non-isolated instance b's network, not a's:\n%s", out)
			}
		}
	}
}

// TestCmdPluginShowDefaultSharesUnionAsOneProcess is `plugin show`'s half of
// the same default-sharing behavior.
func TestCmdPluginShowDefaultSharesUnionAsOneProcess(t *testing.T) {
	args := writeCfg(t, `
connectors:
  a: { use: acme/plugins/jira, network: ["one.example:443"] }
  b: { use: acme/plugins/jira, network: ["two.example:443"] }
`)
	out, err := captureStdout(t, func() error { return cmdPluginShow(append(args, "jira")) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "shared process") {
		t.Fatalf("plugin show did not label the default shared-process grant:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "shared process") {
			if !strings.Contains(line, "one.example:443") || !strings.Contains(line, "two.example:443") {
				t.Fatalf("shared-process grant line did not carry the union of both instances' network:\n%s", out)
			}
		}
	}
	if strings.Contains(out, "own process") {
		t.Fatalf("neither instance isolates; plugin show must show no own-process line:\n%s", out)
	}
}

// `plugin add` on a builtin installs nothing and just prints the stub — adding
// `github` should never reach for the plugin repo.
func TestCmdPluginAddBuiltinInstallsNothing(t *testing.T) {
	args := writeCfg(t, "connectors:\n  gh: { use: github }\n")
	out, err := captureStdout(t, func() error {
		return cmdPluginAdd(append(args, "slack"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing to install") {
		t.Fatalf("adding a builtin tried to install it:\n%s", out)
	}
	if !strings.Contains(out, "use: slack") {
		t.Fatalf("no config stub printed:\n%s", out)
	}
}

// `plugin add` refuses a kind-mismatched reference up front, before any fetch.
func TestCmdPluginAddRefusesKindMismatch(t *testing.T) {
	args := writeCfg(t, "connectors:\n  gh: { use: github }\n")
	_, err := captureStdout(t, func() error {
		return cmdPluginAdd(append(args, "paseo")) // a runtime, under connectors
	})
	if err == nil || !strings.Contains(err.Error(), "can never be wired as a connector") {
		t.Fatalf("want a kind refusal, got %v", err)
	}
}

// `plugin show` prints a builtin's surface without spawning anything.
func TestCmdPluginShowBuiltin(t *testing.T) {
	args := writeCfg(t, "connectors:\n  gh: { use: github }\n")
	out, err := captureStdout(t, func() error {
		return cmdPluginShow(append(args, "command"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "builtin connector") {
		t.Fatalf("plugin show did not identify a builtin:\n%s", out)
	}

	out, err = captureStdout(t, func() error { return cmdPluginShow(append(args, "paseo")) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "builtin runtime") {
		t.Fatalf("plugin show did not identify a builtin runtime:\n%s", out)
	}
}

// TestCmdPluginShowPerInstanceGrantsNotUnion is `plugin show`'s half of the
// finding-3 regression (cmdPluginListCapsShowsPerInstanceGrantsNotUnion
// above covers `plugin list --caps`): two instances of a non-shared-process
// plugin with different `network:` must each show their OWN grant.
func TestCmdPluginShowPerInstanceGrantsNotUnion(t *testing.T) {
	args := writeCfg(t, `
connectors:
  a: { use: acme/plugins/jira, network: ["one.example:443"], isolate: true }
  b: { use: acme/plugins/jira, network: ["two.example:443"], isolate: true }
`)
	out, err := captureStdout(t, func() error { return cmdPluginShow(append(args, "jira")) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "instance a") || !strings.Contains(out, "instance b") {
		t.Fatalf("plugin show did not label each isolated instance's own grant:\n%s", out)
	}
	if !strings.Contains(out, "one.example:443") || !strings.Contains(out, "two.example:443") {
		t.Fatalf("plugin show lost one instance's own narrowed network:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "one.example:443") && strings.Contains(line, "two.example:443") {
			t.Fatalf("instance grants were unioned onto one line:\n%s", out)
		}
	}
}

// `connectors ls` shows each instance's resolved origin, and the `use:` when it
// differs from the type it resolved to.
func TestCmdConnectorsLsShowsUse(t *testing.T) {
	args := writeCfg(t, `
connectors:
  timer: { use: cron, schedules: { tick: { every: 1h } } }
  box:   { use: command }
`)
	out, err := captureStdout(t, func() error { return cmdConnectors(append(args, "ls")) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "builtin") {
		t.Fatalf("connectors ls did not show the resolved origin:\n%s", out)
	}
	for _, want := range []string{"timer", "box", "cron", "command"} {
		if !strings.Contains(out, want) {
			t.Fatalf("connectors ls missing %q:\n%s", want, out)
		}
	}
}

// `plugin update` on a config with no plugins is a no-op that says so, rather
// than an error.
func TestCmdPluginUpdateNoPlugins(t *testing.T) {
	args := writeCfg(t, "connectors:\n  gh: { use: github }\n")
	out, err := captureStdout(t, func() error { return cmdPluginUpdate(args) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing to update") {
		t.Fatalf("expected a no-op message:\n%s", out)
	}
}

// A stale `type:` produces a MIGRATION-SPECIFIC error, not an opaque decode
// failure — this is what keeps an auto-updating fleet out of a crash-loop.
func TestStaleTypeNamesTheMigration(t *testing.T) {
	args := writeCfg(t, "connectors:\n  gh: { type: github }\n")
	_, _, err := loadConfig(args)
	if err == nil {
		t.Fatal("a stale type: loaded silently")
	}
	if !strings.Contains(err.Error(), "config migrate") {
		t.Fatalf("error does not name the migration: %v", err)
	}

	args = writeCfg(t, "connectors:\n  gh: { use: github }\nruntimes:\n  r: { type: paseo }\n")
	if _, _, err := loadConfig(args); err == nil || !strings.Contains(err.Error(), "config migrate") {
		t.Fatalf("a stale runtime type: should name the migration, got %v", err)
	}
}

// A connector `network:` may NARROW what a plugin declares, never widen it —
// but the shape check happens at load, before anything is installed.
func TestNetworkShapeValidatedAtLoad(t *testing.T) {
	args := writeCfg(t, "connectors:\n  api: { use: rest, base_url: https://x.example, network: [\"https://nope\"] }\n")
	_, _, err := loadConfig(args)
	if err == nil || !strings.Contains(err.Error(), "is a URL") {
		t.Fatalf("want a network: shape error, got %v", err)
	}
}
