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
	"time"

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

// paseoRun runs the paseo CLI bin ("" = paseo on PATH) — a var for tests.
var paseoRun = func(bin string, args ...string) ([]byte, error) {
	if bin == "" {
		bin = "paseo"
	}
	return exec.Command(bin, args...).CombinedOutput()
}

type paseoSetupOpts struct {
	home, file, conductor, config, socket string
	providers                             []string
	dryRun, noReload                      bool
	// bin is the paseo CLI that reloads the daemon ("" = paseo on PATH).
	bin string
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
	plan, err := planPaseoSetup(o, func(note string) { fmt.Fprintln(out, note) })
	if err != nil {
		return err
	}
	rep := plan.report
	for _, n := range rep.Order {
		line := fmt.Sprintf("%-9s %s", rep.Results[n], n)
		if rep.Results[n] == paseosetup.Skipped {
			line += " (your own entry of that name, without the x-conductor marker: not touched)"
		}
		fmt.Fprintln(out, line)
	}
	fmt.Fprintf(out, "== %s (%s)\n", paseoCounts(rep), plan.o.file)
	if plan.o.dryRun || !rep.Changed() {
		return nil
	}
	reloaded, err := writePaseoSetup(plan)
	if err != nil {
		return err
	}
	if !reloaded {
		fmt.Fprintln(out, "not reloaded (--no-reload): run 'paseo reload' to pick the entries up")
		return nil
	}
	fmt.Fprintln(out, "paseo reloaded")
	return nil
}

// paseoCounts renders a merge report's tally.
func paseoCounts(rep paseosetup.Report) string {
	return fmt.Sprintf("added=%d updated=%d unchanged=%d skipped=%d",
		rep.Count(paseosetup.Added), rep.Count(paseosetup.Updated), rep.Count(paseosetup.Unchanged), rep.Count(paseosetup.Skipped))
}

// paseoSetupPlan is a computed merge: the options it was resolved with, the
// merged file and what changed.
type paseoSetupPlan struct {
	o      paseoSetupOpts
	merged []byte
	report paseosetup.Report
}

// planPaseoSetup resolves o's defaults, reads paseo's config and merges
// conductor's entries into it — writing nothing. note receives what the
// operator should know (pi not offered).
func planPaseoSetup(o paseoSetupOpts, note func(string)) (paseoSetupPlan, error) {
	var err error
	if o.home == "" {
		o.home = os.Getenv("PASEO_HOME")
	}
	if o.home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return paseoSetupPlan{}, err
		}
		o.home = filepath.Join(h, ".paseo")
	}
	if o.file == "" {
		o.file = filepath.Join(o.home, "config.json")
	}
	if o.conductor == "" {
		if o.conductor, err = os.Executable(); err != nil {
			return paseoSetupPlan{}, err
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
		return paseoSetupPlan{}, err
	}
	providers := o.providers
	if len(providers) == 0 {
		providers = []string{"claude", "codex"}
		if piAvailable(o.bin, o.home) {
			providers = append(providers, "pi")
		} else {
			note("pi: not offered by this paseo — no pi-sandboxed entry (pass --providers to force one)")
		}
	}
	want, names, err := paseoEntries(doc, o, providers)
	if err != nil {
		return paseoSetupPlan{}, err
	}
	merged, rep, err := paseosetup.Merge(doc, want, names)
	if err != nil {
		return paseoSetupPlan{}, err
	}
	return paseoSetupPlan{o: o, merged: merged, report: rep}, nil
}

// writePaseoSetup writes a changed plan and reloads paseo (unless
// --no-reload: reloaded false).
func writePaseoSetup(p paseoSetupPlan) (reloaded bool, err error) {
	if err := os.MkdirAll(filepath.Dir(p.o.file), 0o700); err != nil {
		return false, err
	}
	if err := paseosetup.WriteFile(p.o.file, p.merged); err != nil {
		return false, err
	}
	if p.o.noReload {
		return false, nil
	}
	if b, err := paseoRun(p.o.bin, "reload", "--home", p.o.home); err != nil {
		return false, fmt.Errorf("wrote %s, but 'paseo reload' failed (%v): %s — run it once the daemon is up", p.o.file, err, strings.TrimSpace(string(b)))
	}
	return true, nil
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

// piAvailable asks the paseo daemon whether it offers the pi provider,
// waiting out paseo's "loading" while it probes providers after a start.
func piAvailable(bin, home string) bool {
	for try := 0; ; try++ {
		avail, loading := piStatus(bin, home)
		if !loading || try >= paseoProbeTries {
			return avail
		}
		time.Sleep(paseoProbeWait)
	}
}

// paseoProbeTries / paseoProbeWait bound the wait for a provider probe.
var (
	paseoProbeTries = 15
	paseoProbeWait  = 2 * time.Second
)

// piStatus reads `paseo provider ls`: whether pi is available, and whether
// paseo is still probing it.
func piStatus(bin, home string) (available, loading bool) {
	b, err := paseoRun(bin, "provider", "ls", "--home", home, "--json")
	if err != nil {
		return false, false
	}
	var list []map[string]any
	if json.Unmarshal(b, &list) != nil {
		return false, false
	}
	for _, p := range list {
		id, _ := p["provider"].(string)
		if id == "" {
			id, _ = p["id"].(string)
		}
		if id != "pi" {
			continue
		}
		if v, ok := p["available"].(bool); ok {
			return v, false
		}
		if s, ok := p["status"].(string); ok {
			return s == "available" || s == "ready", s == "loading"
		}
	}
	return false, false
}
