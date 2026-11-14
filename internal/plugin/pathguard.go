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
	// writes are each Manager's writable paths' forms.
	writes  map[*Manager][]string
	dropped []string
}

// dirForms caches the forms of PATH's directories: resolved once, before
// the guard's lock is taken, so re-deriving PATH (on every new sandboxed
// write) touches no filesystem.
var dirForms struct {
	sync.Mutex
	m map[string][]string
}

func cachedForms(d string) []string {
	dirForms.Lock()
	f, ok := dirForms.m[d]
	dirForms.Unlock()
	if ok {
		return f
	}
	f = pathForms(d)
	dirForms.Lock()
	if dirForms.m == nil {
		dirForms.m = map[string][]string{}
	}
	dirForms.m[d] = f
	dirForms.Unlock()
	return f
}

// guardPATH sets m's writable paths (nil: m is gone; m nil: only
// re-derive) and re-derives PATH, from them and every path a sandboxed
// launch was let write (sandbox.GrantedForms). Paths are resolved before
// the lock is taken.
func guardPATH(m *Manager, writes []string) {
	var forms []string
	for _, w := range writes {
		if filepath.IsAbs(w) {
			forms = append(forms, pathForms(w)...)
		}
	}
	pathGuard.Lock()
	orig := pathGuard.orig
	pathGuard.Unlock()
	for _, p := range []string{os.Getenv("PATH"), orig} {
		for _, d := range filepath.SplitList(p) {
			if filepath.IsAbs(d) {
				cachedForms(d)
			}
		}
	}
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
		pathGuard.writes[m] = forms
	}
	if !pathGuard.hooked {
		// A sandboxed launch let write a new path re-derives PATH.
		pathGuard.hooked = true
		sandbox.OnGrantedWrites(func() { guardPATH(nil, nil) })
	}
	var all []string
	for _, g := range sandbox.GrantedForms() {
		all = append(all, g.Forms...)
	}
	for _, ws := range pathGuard.writes {
		all = append(all, ws...)
	}
	kept, dropped := guardedPATHForms(pathGuard.orig, all, cachedForms)
	for _, d := range dropped {
		if !slices.Contains(pathGuard.dropped, d) {
			log.Printf("conductor's PATH: %s is left out: a plugin conductor runs may write it, or it lies inside a path one may write%s", d, ledgerNote(cachedForms(d)))
		}
	}
	pathGuard.dropped = dropped
	if kept != os.Getenv("PATH") {
		os.Setenv("PATH", kept)
	}
	pathGuard.set, pathGuard.guarded = kept, true
}

// ledgerNote says, when a path with forms ps overlaps a path a sandboxed
// launch was let write, where that came from (sandbox.WriteSource): ""
// otherwise. It compares the recorded forms: no filesystem call.
func ledgerNote(ps []string) string {
	for _, g := range sandbox.GrantedForms() {
		for _, gf := range g.Forms {
			for _, x := range ps {
				if within(x, gf) || within(gf, x) {
					return "; " + sandbox.WriteSource(g.Path)
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
	return guardedPATHForms(path, ws, pathForms)
}

// guardedPATHForms is guardedPATH for writes already resolved, with forms
// giving a PATH directory's.
func guardedPATHForms(path string, ws []string, forms func(string) []string) (kept string, dropped []string) {
	var out []string
	for _, d := range filepath.SplitList(path) {
		if !filepath.IsAbs(d) {
			continue
		}
		ds := forms(d)
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
