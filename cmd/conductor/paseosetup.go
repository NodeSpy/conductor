package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/internal/paseosetup"
)

const paseoUsage = `usage: conductor paseo setup [--home DIR] [--file PATH] [--conductor PATH]
                             [--config PATH | --socket PATH] [--providers claude,codex,pi]
                             [--dry-run] [--no-reload]

Writes conductor's sandboxed provider entries — claude-sandboxed,
codex-sandboxed, and pi-sandboxed where paseo has pi — into a paseo daemon's
config.json, then reloads it. Each entry extends paseo's own provider and runs
the real agent command through 'conductor sandbox exec', so a session started
with it runs in conductor's agent sandbox. Re-running is safe: conductor's
entries (marked "x-conductor": true) are updated in place, everything else in
the file is left as it was, and an entry of the same name that you wrote
yourself is skipped, never overwritten.

  --home DIR        the paseo home (default: $PASEO_HOME, else ~/.paseo)
  --file PATH       the config file (default: <home>/config.json)
  --conductor PATH  the conductor binary the entries run (default: this one)
  --config PATH     the conductor config whose daemon the entries ask (when not the default)
  --socket PATH     the daemon's control socket, directly
  --providers LIST  which providers (default: claude,codex, and pi when paseo has it)
  --dry-run         print what would change; write nothing
  --no-reload       write, but do not run 'paseo reload'
`

func cmdPaseo(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, paseoUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "setup":
		if err := cmdPaseoSetup(args[1:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "conductor paseo setup: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "conductor paseo: unknown subcommand %q\n%s", args[0], paseoUsage)
		return 2
	}
}

// paseoRun runs the paseo CLI (a var for tests).
var paseoRun = func(args ...string) ([]byte, error) {
	return exec.Command("paseo", args...).CombinedOutput()
}

type paseoSetupOpts struct {
	home, file, conductor, config, socket string
	providers                             []string
	dryRun, noReload                      bool
}

func parsePaseoSetup(args []string) (paseoSetupOpts, error) {
	var o paseoSetupOpts
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch a {
		case "--home":
			o.home, err = val()
		case "--file":
			o.file, err = val()
		case "--conductor":
			o.conductor, err = val()
		case "--config":
			o.config, err = val()
		case "--socket":
			o.socket, err = val()
		case "--providers":
			var v string
			if v, err = val(); err == nil {
				for _, p := range strings.Split(v, ",") {
					if p = strings.TrimSpace(p); p != "" {
						o.providers = append(o.providers, p)
					}
				}
			}
		case "--dry-run":
			o.dryRun = true
		case "--no-reload":
			o.noReload = true
		default:
			return o, fmt.Errorf("unknown flag %q\n%s", a, paseoUsage)
		}
		if err != nil {
			return o, err
		}
	}
	if o.config != "" && o.socket != "" {
		return o, errors.New("--config and --socket are exclusive")
	}
	return o, nil
}

func cmdPaseoSetup(args []string, out *os.File) error {
	o, err := parsePaseoSetup(args)
	if err != nil {
		return err
	}
	if o.home == "" {
		o.home = os.Getenv("PASEO_HOME")
	}
	if o.home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.home = filepath.Join(h, ".paseo")
	}
	if o.file == "" {
		o.file = filepath.Join(o.home, "config.json")
	}
	if o.conductor == "" {
		if o.conductor, err = os.Executable(); err != nil {
			return err
		}
	}
	for _, p := range []*string{&o.conductor, &o.config, &o.socket} {
		if *p != "" {
			if abs, err := filepath.Abs(*p); err == nil {
				*p = abs
			}
		}
	}
	doc, err := os.ReadFile(o.file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	providers := o.providers
	if len(providers) == 0 {
		providers = []string{"claude", "codex"}
		if piAvailable(o.home) {
			providers = append(providers, "pi")
		} else {
			fmt.Fprintln(out, "pi: not offered by this paseo — no pi-sandboxed entry (pass --providers to force one)")
		}
	}
	want, names, err := paseoEntries(doc, o, providers)
	if err != nil {
		return err
	}
	merged, rep, err := paseosetup.Merge(doc, want, names)
	if err != nil {
		return err
	}
	for _, n := range rep.Order {
		line := fmt.Sprintf("%-9s %s", rep.Results[n], n)
		if rep.Results[n] == paseosetup.Skipped {
			line += " (your own entry of that name, without the x-conductor marker: not touched)"
		}
		fmt.Fprintln(out, line)
	}
	fmt.Fprintf(out, "== added=%d updated=%d unchanged=%d skipped=%d (%s)\n",
		rep.Count(paseosetup.Added), rep.Count(paseosetup.Updated), rep.Count(paseosetup.Unchanged), rep.Count(paseosetup.Skipped), o.file)
	if o.dryRun || !rep.Changed() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(o.file), 0o700); err != nil {
		return err
	}
	if err := paseosetup.WriteFile(o.file, merged); err != nil {
		return err
	}
	if o.noReload {
		fmt.Fprintln(out, "not reloaded (--no-reload): run 'paseo reload' to pick the entries up")
		return nil
	}
	if b, err := paseoRun("reload", "--home", o.home); err != nil {
		return fmt.Errorf("wrote %s, but 'paseo reload' failed (%v): %s — run it once the daemon is up", o.file, err, strings.TrimSpace(string(b)))
	}
	fmt.Fprintln(out, "paseo reloaded")
	return nil
}

// paseoEntries builds conductor's entry per provider. The agent command an
// entry wraps is the operator's own for that provider when their config sets
// one (a wrapper script that routes the model), else the provider's name.
func paseoEntries(doc []byte, o paseoSetupOpts, providers []string) (map[string]map[string]any, []string, error) {
	var cfg struct {
		Agents struct {
			Providers map[string]struct {
				Command []string `json:"command"`
				Extends string   `json:"extends"`
			} `json:"providers"`
		} `json:"agents"`
	}
	if len(bytes.TrimSpace(doc)) > 0 {
		if err := json.Unmarshal(doc, &cfg); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", o.file, err)
		}
	}
	var daemon []string
	switch {
	case o.config != "":
		daemon = []string{"--config", o.config}
	case o.socket != "":
		daemon = []string{"--socket", o.socket}
	}
	want := map[string]map[string]any{}
	var names []string
	for _, p := range providers {
		label := ""
		for _, k := range paseosetup.Providers {
			if k.Name == p {
				label = k.Label
			}
		}
		if label == "" {
			label = p + " (sandboxed)"
		}
		agentCmd := []string{p}
		if cur, ok := cfg.Agents.Providers[p]; ok && len(cur.Command) > 0 && cur.Extends == "" {
			agentCmd = cur.Command
		}
		name := paseosetup.EntryName(p)
		want[name] = paseosetup.Entry(p, label, o.conductor, daemon, agentCmd)
		names = append(names, name)
	}
	return want, names, nil
}

// piAvailable asks the paseo daemon whether it offers the pi provider.
func piAvailable(home string) bool {
	b, err := paseoRun("provider", "ls", "--home", home, "--json")
	if err != nil {
		return false
	}
	var list []map[string]any
	if json.Unmarshal(b, &list) != nil {
		return false
	}
	for _, p := range list {
		id, _ := p["id"].(string)
		if id == "" {
			id, _ = p["name"].(string)
		}
		if id != "pi" {
			continue
		}
		if v, ok := p["available"].(bool); ok {
			return v
		}
		if s, ok := p["status"].(string); ok {
			return s == "available" || s == "ready"
		}
		if v, ok := p["enabled"].(bool); ok {
			return v
		}
	}
	return false
}
