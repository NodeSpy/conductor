package repocache

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A run's overlay layers live beside the store, never in the run's home or
// workspace: mount(2) takes an overlay's upper and work directories as
// paths and follows a symlink in them, so an agent able to write where they
// are could point its writes at a published cache. No sandbox can see the
// store's directory. The same directory is conductor's record of which
// published caches a live run sits on: a run's entry names its home, and is
// in use while that home exists. Nothing here is ever read from a home.

const (
	layersDirName = ".layers"
	warmDirName   = ".warm"
	runHomeFile   = "home"
	runUsesFile   = "uses"
)

func (s *Store) layersRoot() string { return filepath.Join(s.Dir, layersDirName) }
func (s *Store) warmRoot() string   { return filepath.Join(s.Dir, warmDirName) }

// runDir is the ledger entry and layers directory of the run whose home is
// home, created (with its record of the home) if need be.
func (s *Store) runDir(home string) (string, error) {
	if err := os.MkdirAll(s.layersRoot(), 0o700); err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(home))
	d := filepath.Join(s.layersRoot(), hex.EncodeToString(h[:])[:24])
	if err := realDir(d); err != nil {
		return "", err
	}
	return d, os.WriteFile(filepath.Join(d, runHomeFile), []byte(home), 0o600)
}

// writeUses records the published caches a run's overlays sit on and
// removes the slots it no longer uses; a run with none is dropped.
func writeUses(run, home string, used []string, slots map[string]bool) error {
	ents, _ := os.ReadDir(run)
	for _, e := range ents {
		if e.Name() != runHomeFile && e.Name() != runUsesFile && !slots[e.Name()] {
			_ = RemoveAll(filepath.Join(run, e.Name()))
		}
	}
	p := filepath.Join(run, runUsesFile)
	_ = os.Remove(p)
	if len(used) == 0 && len(slots) == 0 {
		return RemoveAll(run)
	}
	return os.WriteFile(p, []byte(strings.Join(used, "\n")+"\n"), 0o600)
}

// runs lists the ledger: each run's directory, the home it names, and
// whether that home still exists.
func (s *Store) runs() []runEntry {
	ents, _ := os.ReadDir(s.layersRoot())
	var out []runEntry
	for _, e := range ents {
		d := filepath.Join(s.layersRoot(), e.Name())
		r := runEntry{dir: d}
		if b, err := readSmall(filepath.Join(d, runHomeFile)); err == nil && filepath.IsAbs(string(b)) {
			r.home = string(b)
			_, err := os.Lstat(r.home)
			r.live = err == nil || !errors.Is(err, fs.ErrNotExist)
		}
		out = append(out, r)
	}
	return out
}

type runEntry struct {
	dir, home string
	live      bool
}

// Used are the published caches a live run's overlays sit on.
func (s *Store) Used() map[string]bool {
	out := map[string]bool{}
	for _, r := range s.runs() {
		if !r.live {
			continue
		}
		f, err := os.Open(filepath.Join(r.dir, runUsesFile))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				out[l] = true
			}
		}
		f.Close()
	}
	return out
}

// sweepRuns removes the layers and ledger entry of every run whose home is
// gone (or that never recorded one).
func (s *Store) sweepRuns() {
	for _, r := range s.runs() {
		if !r.live {
			_ = RemoveAll(r.dir)
		}
	}
}

// readSmall reads a small regular file of conductor's.
func readSmall(p string) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return nil, errors.New("not a small regular file")
	}
	return os.ReadFile(p)
}
