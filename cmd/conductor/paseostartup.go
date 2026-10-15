package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/paseosetup"
	"github.com/NodeSpy/conductor/internal/paseover"
)

// The daemon writes its own paseo provider entries. With the agent sandbox
// on, conductor's paseo dispatches name the conductor-owned
// <provider>-sandboxed entries, and paseo refuses a provider it does not
// have — so on a box that auto-updated into the sandbox, every paseo
// dispatch was refused until someone ran `conductor paseo setup`. At start
// the daemon applies that same merge itself: conductor's x-conductor entries
// only (an operator's entry of the same name is skipped, never touched),
// pointed at this daemon's control socket, then `paseo reload`.
// `conductor paseo setup` stays the manual form.

// paseoSetupTarget is one paseo daemon whose config this conductor writes.
type paseoSetupTarget struct {
	runtime string // the runtimes: entry ("" = the built-in default)
	home    string // its paseo home ("" = not one conductor can name: skip)
	bin     string // its paseo CLI ("" = paseo on PATH)
	why     string // why a target is skipped
}

// paseoSetupTargets are the paseo homes the daemon's paseo dispatches go
// to, as far as this box can name them: a local runtime's home:, else
// $PASEO_HOME, else ~/.paseo when it exists. A runtime that names only a
// server:, a host: one (another box), or a box where no home can be named is
// skipped — conductor writes only a config it knows is that daemon's, never
// one found by probing an address.
func paseoSetupTargets(cfg *config.Config, getenv func(string) string, userHome string) []paseoSetupTarget {
	ambient := func() (string, string) {
		if h := strings.TrimSpace(getenv("PASEO_HOME")); h != "" {
			return paseover.ExpandTilde(h), ""
		}
		if userHome != "" {
			d := filepath.Join(userHome, ".paseo")
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				return d, ""
			}
		}
		return "", "no paseo home to write to ($PASEO_HOME unset, no ~/.paseo) — set the runtime's home:, or run `conductor paseo setup --home DIR`"
	}
	defs := paseoRuntimeDefs(cfg)
	if len(defs) == 0 {
		// No paseo runtime declared: the built-in one, on the box's own
		// paseo.
		h, why := ambient()
		return []paseoSetupTarget{{home: h, why: why}}
	}
	var out []paseoSetupTarget
	seen := map[string]bool{}
	for _, d := range defs {
		t := paseoSetupTarget{runtime: d.Name, bin: d.Bin}
		switch {
		case d.Host != "":
			t.why = "runs on host " + d.Host + " — run `conductor paseo setup` there"
		case strings.TrimSpace(d.Home) != "":
			t.home = paseover.ExpandTilde(strings.TrimSpace(d.Home))
		case strings.TrimSpace(d.Server) != "":
			t.why = "names only server: " + d.Server + " — its home is unknown here; add home:, or run `conductor paseo setup --home DIR`"
		default:
			t.home, t.why = ambient()
		}
		if t.home != "" {
			if seen[t.home] {
				continue
			}
			seen[t.home] = true
		}
		out = append(out, t)
	}
	return out
}

// startupPaseoSetup merges conductor's entries into each target's config
// and logs what it did. sock is this daemon's control socket, which the
// entries' `sandbox exec` asks.
func startupPaseoSetup(cfg *config.Config, sock string, logf func(string, ...any)) {
	self, err := os.Executable()
	if err != nil {
		logf("paseo setup (start): cannot resolve conductor's own binary (%v) — run `conductor paseo setup`", err)
		return
	}
	home, _ := os.UserHomeDir()
	for _, t := range paseoSetupTargets(cfg, os.Getenv, home) {
		name := t.runtime
		if name == "" {
			name = "paseo"
		}
		if t.home == "" {
			logf("paseo setup (start): runtime %s skipped: %s", name, t.why)
			continue
		}
		o := paseoSetupOpts{home: t.home, conductor: self, socket: sock, bin: t.bin}
		plan, err := planPaseoSetup(o, func(note string) { logf("paseo setup (start): runtime %s: %s", name, note) })
		if err != nil {
			logf("paseo setup (start): runtime %s (%s): %v — sandboxed paseo dispatches are refused until `conductor paseo setup` succeeds", name, t.home, err)
			continue
		}
		rep := plan.report
		for _, n := range rep.Order {
			if rep.Results[n] == paseosetup.Skipped {
				logf("paseo setup (start): runtime %s: %s is your own entry (no x-conductor marker) — not touched; paseo dispatches using it are not conductor's sandbox", name, n)
			}
		}
		if !rep.Changed() {
			logf("paseo setup (start): runtime %s: %s, nothing to write (%s)", name, paseoCounts(rep), plan.o.file)
			continue
		}
		reloaded, err := writePaseoSetup(plan)
		switch {
		case err != nil:
			logf("paseo setup (start): runtime %s: %s: %v", name, paseoCounts(rep), err)
		case reloaded:
			logf("paseo setup (start): runtime %s: %s (%s); paseo reloaded", name, paseoCounts(rep), plan.o.file)
		}
	}
}
