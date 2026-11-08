package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/configmigrate"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// A config still carrying the bundled webhook connector's retired smee_url.
const retiredSmeeConfig = `# keep this comment
connectors:
  hooks:
    use: webhook
    smee_url: ${HOOKS_RELAY}
    sources:
      alarms: { path: /alarms }
triggers:
  - on: hooks.alarms
    name: alarm
    steps: [{ id: t, type: command, command: ["true"] }]
`

func writeRetired(t *testing.T) (string, []string) {
	t.Helper()
	t.Setenv("HOOKS_RELAY", "https://relay.example/ch")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(retiredSmeeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, []string{"--config", p}
}

func stubMigrateOptions(t *testing.T, ensure func([]string) error, check func(string) (map[string]string, error)) {
	t.Helper()
	old := migrateOptions
	t.Cleanup(func() { migrateOptions = old })
	migrateOptions = func(string, bool) configmigrate.Options {
		return configmigrate.Options{Ensure: func(u []string, _ map[string][]string) error { return ensure(u) }, Check: check, Logf: logf, PortFree: func(int) bool { return true }}
	}
}

// Boot rewrites a retired shape before the load: the plugin it needs is
// installed first, the file is backed up, and the config that loads is the
// rewritten one.
func TestBootMigratesBeforeLoad(t *testing.T) {
	path, args := writeRetired(t)
	var ensured []string
	stubMigrateOptions(t, func(u []string) error { ensured = u; return nil }, nil)

	cfg, warning, err := resolveBootConfig(args, nil)
	if err != nil || warning != "" {
		t.Fatalf("boot: warning=%q err=%v", warning, err)
	}
	if strings.Join(ensured, ",") != "smee" {
		t.Fatalf("ensured %v before writing, want smee", ensured)
	}
	got, _ := os.ReadFile(path)
	for _, want := range []string{"# keep this comment", "expose: hooks-smee", "hooks-smee:\n    use: smee\n    channel: ${HOOKS_RELAY}"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if b, err := os.ReadFile(path + ".pre-smee"); err != nil || string(b) != retiredSmeeConfig {
		t.Fatalf("backup: %v", err)
	}
	if _, ok := cfg.ConnectorsMap["hooks-smee"]; !ok {
		t.Fatal("the loaded config is not the rewritten one")
	}
}

// When the plugin the rewrite needs can't be installed, nothing is
// rewritten, boot continues on the config as it is, and the warning says
// why.
func TestBootMigrationHeldWhenPluginUnavailable(t *testing.T) {
	path, args := writeRetired(t)
	stubMigrateOptions(t, func([]string) error { return errors.New("offline") }, nil)

	cfg, warning, err := resolveBootConfig(args, nil)
	if err != nil || cfg == nil {
		t.Fatalf("boot must continue on the config as it is: %v", err)
	}
	if !strings.Contains(warning, "nothing was rewritten") || !strings.Contains(warning, "offline") {
		t.Fatalf("warning = %q", warning)
	}
	if got, _ := os.ReadFile(path); string(got) != retiredSmeeConfig {
		t.Fatal("rewritten despite the failed install")
	}
}

// The real validation refuses a rewrite whose new connector can't build (the
// smee plugin isn't installed here), and leaves the file alone.
func TestBootMigrationRefusedWhenItsConnectorWouldBeDisabled(t *testing.T) {
	path, args := writeRetired(t)
	stubMigrateOptions(t, func([]string) error { return nil }, checkConfigConnectors)

	_, warning, err := resolveBootConfig(args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "hooks-smee would not work: it starts disabled") {
		t.Fatalf("warning = %q", warning)
	}
	if got, _ := os.ReadFile(path); string(got) != retiredSmeeConfig {
		t.Fatal("rewritten despite the failed validation")
	}
	if _, err := os.Stat(path + ".pre-smee"); !os.IsNotExist(err) {
		t.Fatal("a backup was left by a refused rewrite")
	}
}

// `config migrate --dry-run` prints the rewrite and writes nothing; the real
// run rewrites and names the backup; a second run has nothing to do.
func TestCmdConfigMigrate(t *testing.T) {
	path, args := writeRetired(t)
	stubMigrateOptions(t, func([]string) error { return nil }, nil)

	out, err := captureStdout(t, func() error { return cmdConfig(append([]string{"migrate", "--dry-run"}, args...)) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "expose: hooks-smee") {
		t.Fatalf("dry run output:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != retiredSmeeConfig {
		t.Fatal("the dry run wrote")
	}

	out, err = captureStdout(t, func() error { return cmdConfig(append([]string{"migrate"}, args...)) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rewrote "+path+" (backup: "+path+".pre-smee)") {
		t.Fatalf("output:\n%s", out)
	}
	out, err = captureStdout(t, func() error { return cmdConfig(append([]string{"migrate"}, args...)) })
	if err != nil || !strings.Contains(out, "nothing to migrate") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
}

// validate (the self-update preflight) names a pending rewrite and checks
// the plugin it will need — without rewriting anything.
func TestValidateNamesPendingMigration(t *testing.T) {
	path, _ := writeRetired(t)
	oldAPI := validateReleaseAPI
	t.Cleanup(func() { validateReleaseAPI = oldAPI })

	validateReleaseAPI = fakeReleaseAPI{err: errors.New("network unreachable")}
	out, err := captureStdout(t, func() error { return cmdValidate([]string{path, "--require-plugins"}) })
	if err == nil || !strings.Contains(err.Error(), "neither installed nor fetchable") {
		t.Fatalf("a pending rewrite whose plugin can't be fetched must fail --require-plugins, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "notice: config migrate") || !strings.Contains(out, "hooks-smee") {
		t.Fatalf("validate must name the pending rewrite:\n%s", out)
	}
	validateReleaseAPI = fakeReleaseAPI{tags: []string{"connectors/smee/v1.2.0"}}
	if out, err := captureStdout(t, func() error { return cmdValidate([]string{path, "--require-plugins"}) }); err != nil {
		t.Fatalf("fetchable: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(path); string(got) != retiredSmeeConfig {
		t.Fatal("validate rewrote the config")
	}
}

// The migration approves a held smee update for exactly what it wires (it
// declares expose_local and reaches the channel the rewrite pins), and
// nothing more: an update that also asks for anything else stays held for
// the operator, and so does the rewrite.
func TestApproveForMigration(t *testing.T) {
	grant := configmigrate.Grant{Use: "smee", ExposeLocal: true, Egress: []string{"config:channel"}}
	old := plugin.Manifest{Egress: []string{"smee.io:443", "config:smee_base"}}
	for _, c := range []struct {
		name    string
		held    *plugin.Manifest
		wantErr string
	}{
		{"exactly what it wires", &plugin.Manifest{Egress: []string{"smee.io:443", "config:smee_base", "config:channel"}, ExposeLocal: true}, ""},
		{"asks for more", &plugin.Manifest{Egress: []string{"smee.io:443", "config:smee_base", "config:channel", "elsewhere.example:443"}, ExposeLocal: true}, "elsewhere.example:443"},
		{"does not declare expose_local", &plugin.Manifest{Egress: []string{"smee.io:443", "config:smee_base", "config:channel"}}, "no smee release declaring expose_local"},
		{"nothing held", nil, "no smee release declaring expose_local"},
	} {
		t.Run(c.name, func(t *testing.T) {
			config.SetStateDir(t.TempDir())
			t.Cleanup(func() { config.SetStateDir("") })
			bin := filepath.Join(t.TempDir(), "conductor-smee")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			st := plugin.LoadInstallState(plugin.InstallDir())
			cur := plugin.Installed{Key: "connectors/smee", Kind: config.PluginKindConnector, Name: "smee", Use: "smee",
				Resolved: "connectors/smee/v1.1.0", Sha256: strings.Repeat("a", 64), Path: bin, Manifest: old}
			st.Put(cur)
			if c.held != nil {
				next := cur
				next.Resolved, next.Sha256, next.Manifest = "connectors/smee/v1.2.0", strings.Repeat("b", 64), *c.held
				st.Pending = append(st.Pending, plugin.Pending{Installed: next, Replaces: cur.Resolved, Widens: old.Widens(*c.held)})
			}
			if err := st.Save(); err != nil {
				t.Fatal(err)
			}
			notes, err := approveForMigration("connectors/smee", "smee", grant, false)
			now, _ := plugin.LoadInstallState(plugin.InstallDir()).Get("connectors/smee")
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to name %q", err, c.wantErr)
				}
				if now.Resolved != cur.Resolved {
					t.Fatalf("installed %s despite the refusal", now.Resolved)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if now.Resolved != "connectors/smee/v1.2.0" || !now.Manifest.ExposeLocal {
				t.Fatalf("installed %+v", now)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], "approved smee connectors/smee/v1.2.0") || !strings.Contains(notes[0], "expose") {
				t.Fatalf("notes = %q", notes)
			}
		})
	}
}

// smee inherits the pre-jail mark of what it takes over: a plugin (github)
// installed before the jail, or the bundled webhook connector (conductor's
// own relay); not a plugin installed since. (Not named github here: this
// test binary registers an in-process github fixture.)
func TestInheritPreJail(t *testing.T) {
	for _, c := range []struct {
		name     string
		from     string
		ghPre    bool
		inherits bool
	}{
		{"a forge plugin from before the jail", "inherit-forge", true, true},
		{"a forge plugin installed since", "inherit-forge", false, false},
		{"the bundled webhook connector", "webhook", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			config.SetStateDir(t.TempDir())
			t.Cleanup(func() { config.SetStateDir("") })
			st := plugin.LoadInstallState(plugin.InstallDir())
			st.Put(plugin.Installed{Key: "connectors/inherit-forge", Name: "inherit-forge", Resolved: "connectors/inherit-forge/v2.11.1", PreJail: c.ghPre})
			st.Put(plugin.Installed{Key: "connectors/smee", Name: "smee", Resolved: "connectors/smee/v1.1.0"})
			if err := st.Save(); err != nil {
				t.Fatal(err)
			}
			got, err := inheritPreJail(map[string][]string{"smee": {c.from}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if got["connectors/smee"] != c.inherits {
				t.Fatalf("inherits = %v, want %v", got, c.inherits)
			}
			rec, _ := plugin.LoadInstallState(plugin.InstallDir()).Get("connectors/smee")
			if rec.PreJail != c.inherits {
				t.Fatalf("the installed smee's pre_jail = %v", rec.PreJail)
			}
		})
	}
}
