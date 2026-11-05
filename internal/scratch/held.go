package scratch

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Held is a scratch directory owned by a live process: it holds an exclusive
// lock on it for as long as it uses it, so a sweep (SweepUnheld) can tell a
// directory whose owner died from one in use, at any age.
type Held struct {
	Path string
	lock *os.File
}

// NewHeld makes a fresh directory in parent (created if missing) named
// prefix + a random suffix, and takes its lock.
func NewHeld(parent, prefix string) (*Held, error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, prefix+"*")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(dir)
	if err == nil {
		if err = lockExclusive(f, false); err != nil {
			f.Close()
		}
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &Held{Path: dir, lock: f}, nil
}

// Remove deletes the directory and drops its lock.
func (h *Held) Remove() {
	if h == nil {
		return
	}
	_ = os.RemoveAll(h.Path)
	if h.lock != nil {
		h.lock.Close()
	}
}

// Lock is the open directory carrying the lock (to hand to a child that must
// keep it held); Remove closes it.
func (h *Held) Lock() *os.File { return h.lock }

// SweepUnheld removes the directories in parent named prefix* whose lock no
// live process holds, and returns how many. Only those older than olderThan:
// a directory NewHeld has just made is not locked yet.
func SweepUnheld(parent, prefix string, olderThan time.Duration) int {
	cutoff := time.Now().Add(-olderThan)
	ents, err := os.ReadDir(parent)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if fi, err := e.Info(); err != nil || fi.ModTime().After(cutoff) {
			continue
		}
		p := filepath.Join(parent, e.Name())
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		held := lockExclusive(f, true) != nil
		if !held {
			if os.RemoveAll(p) == nil {
				n++
			}
		}
		f.Close()
	}
	return n
}

// PluginTmpPrefix names a plugin process's TMPDIR, a held directory in its
// staging directory (<staging root>/<plugin>/.tmp-*).
const PluginTmpPrefix = ".tmp-"

// ProcessTmpDir is the scratch subdirectory holding each daemon's TMPDIR.
const ProcessTmpDir = "tmp"

// systemTmp is the temp dir this process started with, before
// UseForProcessTmp pointed TMPDIR into scratch.
var systemTmp = os.TempDir()

// SystemTempDir is the temp dir the process started with: for small files
// only (a unix socket, whose path must stay short; a key file), never for
// data whose size a caller does not control.
func SystemTempDir() string { return systemTmp }

// UseForProcessTmp points this process's TMPDIR (and so os.TempDir() and
// every child that inherits it: git, go, an unconfined agent) at a fresh
// held directory in <root>/tmp. release removes it; a daemon that dies
// leaves it for the next boot's SweepUnheld.
func UseForProcessTmp() (release func(), err error) {
	r := Root()
	if r == "" {
		return func() {}, nil
	}
	h, err := NewHeld(filepath.Join(r, ProcessTmpDir), "run-")
	if err != nil {
		return nil, err
	}
	prev, had := os.LookupEnv("TMPDIR")
	os.Setenv("TMPDIR", h.Path)
	return func() {
		if had {
			os.Setenv("TMPDIR", prev)
		} else {
			os.Unsetenv("TMPDIR")
		}
		h.Remove()
	}, nil
}
