package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// Only a paseo home this box can name is written: a runtime's home:, else
// $PASEO_HOME, else an existing ~/.paseo — never a server: endpoint, a
// host: box, or a home guessed from an address.
func TestPaseoSetupTargets(t *testing.T) {
	user := t.TempDir()
	noPaseo := t.TempDir()
	os.MkdirAll(filepath.Join(user, ".paseo"), 0o700)
	cfgOf := func(rts map[string]config.RuntimeConfig) *config.Config { return &config.Config{Runtimes: rts} }
	srv := config.RuntimeConfig{Use: "paseo", Server: "127.0.0.1:6767"}
	withBin := config.RuntimeConfig{Use: "paseo", Home: "/p/two", Bin: "/opt/paseo"}
	for _, c := range []struct {
		name  string
		cfg   *config.Config
		env   map[string]string
		user  string
		homes string // runtime=home|… ("-" = skipped)
	}{
		{"no runtime: ~/.paseo", cfgOf(nil), nil, user, "=" + filepath.Join(user, ".paseo")},
		{"no runtime: $PASEO_HOME wins", cfgOf(nil), map[string]string{"PASEO_HOME": "/env/home"}, user, "=/env/home"},
		{"no runtime, no home anywhere", cfgOf(nil), nil, noPaseo, "=-"},
		{"declared home", cfgOf(map[string]config.RuntimeConfig{"paseo": paseoRT("/p/home", "", true)}), map[string]string{"PASEO_HOME": "/env/home"}, user, "paseo=/p/home"},
		{"host: is another box", cfgOf(map[string]config.RuntimeConfig{"gpu": paseoRT("/far", "box", false)}), nil, user, "gpu=-"},
		{"server: only names an endpoint", cfgOf(map[string]config.RuntimeConfig{"s": srv}), nil, user, "s=-"},
		{"no home: ambient", cfgOf(map[string]config.RuntimeConfig{"paseo": paseoRT("", "", true)}), nil, user, "paseo=" + filepath.Join(user, ".paseo")},
		{"two runtimes, one home: once", cfgOf(map[string]config.RuntimeConfig{"a": paseoRT("/p/home", "", true), "b": paseoRT("/p/home", "", false), "c": withBin}), nil, user, "a=/p/home|c=/p/two"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, tg := range paseoSetupTargets(c.cfg, env(c.env), c.user) {
				h := tg.home
				if h == "" {
					h = "-"
					if tg.why == "" {
						t.Errorf("a skipped target says why: %+v", tg)
					}
				}
				got = append(got, tg.runtime+"="+h)
				if tg.runtime == "c" && tg.bin != "/opt/paseo" {
					t.Errorf("the runtime's own paseo CLI reloads it: %+v", tg)
				}
			}
			if strings.Join(got, "|") != c.homes {
				t.Fatalf("targets %q, want %q", strings.Join(got, "|"), c.homes)
			}
		})
	}
}

// At start the daemon merges its entries into the runtime's paseo config —
// pointed at its own control socket, the operator's entries untouched — and
// reloads paseo; a restart with nothing to change writes and reloads nothing.
func TestStartupPaseoSetup(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "config.json")
	os.WriteFile(file, []byte(`{"daemon":{"listen":"127.0.0.1:7777"},"agents":{"providers":{"codex-sandboxed":{"extends":"codex","command":["mine"]}}}}`), 0o600)
	calls := stubPaseo(t, `[{"provider":"pi","status":"unavailable"}]`)
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{"paseo": {Use: "paseo", Home: home, Bin: "/opt/paseo"}}}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	var bins []string
	old := paseoRun
	paseoRun = func(bin string, args ...string) ([]byte, error) {
		bins = append(bins, bin)
		return old(bin, args...)
	}

	startupPaseoSetup(cfg, "/st/control.sock", logf)
	all := strings.Join(logs, "\n")
	if !strings.Contains(all, "runtime paseo: added=1 updated=0 unchanged=0 skipped=1") || !strings.Contains(all, "paseo reloaded") {
		t.Fatalf("logs:\n%s", all)
	}
	if !strings.Contains(all, "codex-sandboxed is your own entry") {
		t.Fatalf("the skipped operator entry is logged:\n%s", all)
	}
	var c struct {
		Daemon map[string]any `json:"daemon"`
		Agents struct {
			Providers map[string]map[string]any `json:"providers"`
		} `json:"agents"`
	}
	b, _ := os.ReadFile(file)
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	cl := c.Agents.Providers["claude-sandboxed"]
	cmd, _ := json.Marshal(cl["command"])
	self, _ := os.Executable()
	want, _ := json.Marshal([]string{self, "sandbox", "exec", "--socket", "/st/control.sock", "--tool", "claude", "--", "claude"})
	if string(cmd) != string(want) || cl["x-conductor"] != true {
		t.Fatalf("claude-sandboxed asks this daemon: %s", b)
	}
	if mine, _ := json.Marshal(c.Agents.Providers["codex-sandboxed"]["command"]); string(mine) != `["mine"]` || c.Daemon["listen"] != "127.0.0.1:7777" {
		t.Fatalf("the operator's entry and the rest of the file are untouched: %s", b)
	}
	last := (*calls)[len(*calls)-1]
	if strings.Join(last, " ") != "reload --home "+home || bins[len(bins)-1] != "/opt/paseo" {
		t.Fatalf("reload of this home with the runtime's paseo: %v %v", *calls, bins)
	}

	// A restart: unchanged, nothing written, no reload.
	before, _ := os.Stat(file)
	n := len(*calls)
	logs = nil
	startupPaseoSetup(cfg, "/st/control.sock", logf)
	if all := strings.Join(logs, "\n"); !strings.Contains(all, "added=0 updated=0 unchanged=1 skipped=1, nothing to write") {
		t.Fatalf("restart logs:\n%s", all)
	}
	for _, c := range (*calls)[n:] {
		if c[0] == "reload" {
			t.Fatal("no change, no reload")
		}
	}
	if after, _ := os.Stat(file); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unchanged config is not rewritten")
	}

	// A new conductor binary or socket: conductor's entry is updated.
	logs = nil
	startupPaseoSetup(cfg, "/st2/control.sock", logf)
	if all := strings.Join(logs, "\n"); !strings.Contains(all, "updated=1") {
		t.Fatalf("a moved socket updates the entry:\n%s", all)
	}
}

// paseo down at boot: the entries are written anyway (paseo reads them when
// it starts) and the failed reload is logged, not fatal.
func TestStartupPaseoSetupReloadFails(t *testing.T) {
	home := t.TempDir()
	old := paseoRun
	paseoRun = func(_ string, args ...string) ([]byte, error) {
		if args[0] == "reload" {
			return []byte("daemon not running"), errors.New("exit status 1")
		}
		return []byte(`[]`), nil
	}
	t.Cleanup(func() { paseoRun = old })
	var logs []string
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{"paseo": {Use: "paseo", Home: home}}}
	startupPaseoSetup(cfg, "/s.sock", func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	all := strings.Join(logs, "\n")
	if !strings.Contains(all, "'paseo reload' failed") || !strings.Contains(all, "daemon not running") {
		t.Fatalf("logs:\n%s", all)
	}
	if b, err := os.ReadFile(filepath.Join(home, "config.json")); err != nil || !strings.Contains(string(b), "claude-sandboxed") {
		t.Fatalf("written regardless: %s %v", b, err)
	}
	// A config paseo itself could not read is left alone, and said so.
	bad := t.TempDir()
	os.WriteFile(filepath.Join(bad, "config.json"), []byte("{not json"), 0o600)
	logs = nil
	cfg = &config.Config{Runtimes: map[string]config.RuntimeConfig{"paseo": {Use: "paseo", Home: bad}}}
	startupPaseoSetup(cfg, "/s.sock", func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if b, _ := os.ReadFile(filepath.Join(bad, "config.json")); string(b) != "{not json" || !strings.Contains(strings.Join(logs, "\n"), "refused until") {
		t.Fatalf("unreadable config: %s / %v", b, logs)
	}
	// Nothing to name: skipped, with the fix.
	logs = nil
	t.Setenv("PASEO_HOME", "")
	t.Setenv("HOME", t.TempDir())
	startupPaseoSetup(&config.Config{}, "/s.sock", func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if all := strings.Join(logs, "\n"); !strings.Contains(all, "skipped: no paseo home") {
		t.Fatalf("logs:\n%s", all)
	}
}
