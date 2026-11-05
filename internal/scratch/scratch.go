// Package scratch is where conductor puts temporary data that can be large
// (clones, archives, staging, uploads, builds, sandbox jails): a directory on
// disk (scratch.dir, default <state dir>/scratch), never os.TempDir(), which
// is often a RAM-backed tmpfs where every byte written is memory.
package scratch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	mu   sync.Mutex
	root string
)

// SetRoot sets the scratch root (the config's scratch.dir, default
// <state dir>/scratch). "" falls back to
// os.TempDir().
func SetRoot(dir string) {
	mu.Lock()
	root = dir
	mu.Unlock()
}

// Root is the configured scratch root, or "" when none was set.
func Root() string {
	mu.Lock()
	defer mu.Unlock()
	return root
}

// Dir is the directory scratch files go in: the root (created on first use),
// else os.TempDir() when no root is set or it can't be created.
func Dir() string {
	if r := Root(); r != "" {
		if err := os.MkdirAll(r, 0o700); err == nil {
			return r
		}
	}
	return os.TempDir()
}

// Check makes sure dir exists (creating it, 0700) and is writable, with an
// error that says which directory and what to change.
func Check(dir string) error {
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		var f *os.File
		if f, err = os.CreateTemp(dir, ".probe-*"); err == nil {
			_, err = f.WriteString("ok")
			f.Close()
			os.Remove(f.Name())
		}
	}
	if err != nil {
		return fmt.Errorf("scratch directory %s is not usable: %w (set scratch.dir in the config to a writable directory on disk)", dir, err)
	}
	return nil
}

// MkdirTemp is os.MkdirTemp in Dir().
func MkdirTemp(pattern string) (string, error) {
	return os.MkdirTemp(Dir(), pattern)
}

// Sweep removes the entries of dir whose name starts with prefix and that were
// last modified more than olderThan ago: what a process that died (killed,
// out of memory) left behind. It returns how many it removed.
func Sweep(dir, prefix string, olderThan time.Duration) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-olderThan)
	n := 0
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.ModTime().After(cutoff) {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}
