package plugin

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// commandLookPath finds a declared command (or a script's `env`
// interpreter) on PATH, as the daemon runs a bare name: the first
// executable file of that name in an absolute PATH entry. A test var.
var commandLookPath = func(name string) (string, error) {
	if strings.Contains(name, "/") {
		return spawnLookPath(name)
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(d) {
			if p, err := exec.LookPath(filepath.Join(d, name)); err == nil {
				return p, nil
			}
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
	hooked    bool
	writes    map[*Manager][]string
	dropped   []string
}

// guardPATH sets m's writable paths (nil: m is gone; m nil: only
// re-derive) and re-derives PATH, from them and every path a sandboxed
// launch was let write (sandbox.GrantedWrites).
func guardPATH(m *Manager, writes []string) {
	pathGuard.Lock()
	defer pathGuard.Unlock()
	if pathGuard.writes == nil {
		pathGuard.writes = map[*Manager][]string{}
	}
	if cur := os.Getenv("PATH"); !pathGuard.guarded || cur != pathGuard.set {
		pathGuard.orig = cur
	}
	switch {
	case m == nil:
	case writes == nil:
		delete(pathGuard.writes, m)
	default:
		pathGuard.writes[m] = writes
	}
	if !pathGuard.hooked {
		// A sandboxed launch let write a new path re-derives PATH.
		pathGuard.hooked = true
		sandbox.OnGrantedWrites(func() { guardPATH(nil, nil) })
	}
	all := sandbox.GrantedWrites()
	for _, ws := range pathGuard.writes {
		all = append(all, ws...)
	}
	kept, dropped := guardedPATH(pathGuard.orig, all)
	for _, d := range dropped {
		if !slices.Contains(pathGuard.dropped, d) {
			log.Printf("conductor's PATH: %s is left out: a plugin conductor runs may write it, or it lies inside a path one may write%s", d, ledgerNote(d))
		}
	}
	pathGuard.dropped = dropped
	if kept != os.Getenv("PATH") {
		os.Setenv("PATH", kept)
	}
	pathGuard.set, pathGuard.guarded = kept, true
}

// ledgerNote says, when p overlaps a path a sandboxed launch was let
// write, where that came from (sandbox.WriteSource): "" otherwise.
func ledgerNote(p string) string {
	ps := pathForms(p)
	for _, g := range sandbox.GrantedWrites() {
		for _, gf := range pathForms(g) {
			for _, x := range ps {
				if within(x, gf) || within(gf, x) {
					return "; " + sandbox.WriteSource(g)
				}
			}
		}
	}
	return ""
}

// UnguardedPATH is PATH as it was before the guard left directories out
// of it: what to record as the PATH the daemon was started with.
func UnguardedPATH() string {
	pathGuard.Lock()
	defer pathGuard.Unlock()
	if cur := os.Getenv("PATH"); !pathGuard.guarded || cur != pathGuard.set {
		return cur
	}
	return pathGuard.orig
}

// guardedPATH is path less its relative and empty entries and every
// directory that overlaps a path in writes (either inside the other), and
// the directories it left out for overlapping.
func guardedPATH(path string, writes []string) (kept string, dropped []string) {
	var ws []string
	for _, w := range writes {
		if filepath.IsAbs(w) {
			ws = append(ws, pathForms(w)...)
		}
	}
	var out []string
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		ds := pathForms(d)
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
