package plugin

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// CommandPathEnv names the PATH a declared command is looked up on, in the
// order the operator's shell looks it up (`command -v`): `service install`
// writes the installing shell's PATH there. Unset, PATH is used. It is only
// where conductor looks for a plugin's commands — what it finds is checked
// like any program — never the PATH conductor runs its own tools from.
const CommandPathEnv = "CONDUCTOR_COMMAND_PATH"

// commandLookPath finds a declared command (or a script's `env`
// interpreter) the way the operator's shell would: on CommandPathEnv when
// set, else PATH. A test var.
var commandLookPath = func(name string) (string, error) {
	cp := os.Getenv(CommandPathEnv)
	if cp == "" || strings.Contains(name, "/") {
		return spawnLookPath(name)
	}
	for _, d := range filepath.SplitList(cp) {
		if !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// The daemon's PATH is what it runs its own tools from, unjailed (git,
// ssh-keygen, unshare, ps, lsof): a directory on it that a jailed plugin
// may write, or that lies inside one, would let the plugin choose what
// conductor runs. Each Manager that guards PATH (Deps.GuardPATH) registers
// what its plugins may write; PATH is the PATH the process started with,
// less every directory that overlaps one of them (and any entry that is not
// an absolute path), recomputed whenever a Manager's plugins change or it
// closes. PATH set by anything else since is taken as the one to guard.
var pathGuard struct {
	sync.Mutex
	// orig is PATH before the guard; set is what the guard last made it.
	// A PATH set since by anything else is taken as the new orig.
	orig, set string
	guarded   bool
	writes    map[*Manager][]string
	dropped   []string
}

// guardPATH sets m's writable paths (nil: m is gone) and re-derives PATH.
func guardPATH(m *Manager, writes []string) {
	pathGuard.Lock()
	defer pathGuard.Unlock()
	if pathGuard.writes == nil {
		pathGuard.writes = map[*Manager][]string{}
	}
	if cur := os.Getenv("PATH"); !pathGuard.guarded || cur != pathGuard.set {
		pathGuard.orig = cur
	}
	if writes == nil {
		delete(pathGuard.writes, m)
	} else {
		pathGuard.writes[m] = writes
	}
	var all []string
	for _, ws := range pathGuard.writes {
		all = append(all, ws...)
	}
	kept, dropped := guardedPATH(pathGuard.orig, all)
	for _, d := range dropped {
		if !slices.Contains(pathGuard.dropped, d) {
			log.Printf("conductor's PATH: %s is left out: a plugin conductor runs may write it, or it lies inside a path one may write", d)
		}
	}
	pathGuard.dropped = dropped
	if kept != os.Getenv("PATH") {
		os.Setenv("PATH", kept)
	}
	pathGuard.set, pathGuard.guarded = kept, true
}

// guardedPATH is path less its relative and empty entries and every
// directory that overlaps a path in writes (either inside the other), and
// the directories it left out for overlapping.
func guardedPATH(path string, writes []string) (kept string, dropped []string) {
	var ws []string
	for _, w := range writes {
		if !filepath.IsAbs(w) {
			continue
		}
		ws = append(ws, filepath.Clean(w))
		if r, err := filepath.EvalSymlinks(w); err == nil {
			ws = append(ws, r)
		}
	}
	var out []string
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		ds := []string{filepath.Clean(d)}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			ds = append(ds, r)
		}
		over := false
		for _, x := range ds {
			for _, w := range ws {
				over = over || within(x, w) || within(w, x)
			}
		}
		if over {
			if !slices.Contains(dropped, d) {
				dropped = append(dropped, d)
			}
			continue
		}
		out = append(out, d)
	}
	return strings.Join(out, string(os.PathListSeparator)), dropped
}
