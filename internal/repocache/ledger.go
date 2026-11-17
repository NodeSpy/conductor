package repocache

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A launch's overlay layers live beside the store, never in its home or
// workspace: mount(2) takes an overlay's upper and work directories as
// paths and follows a symlink in them, so an agent able to write where they
// are could point its writes at a published cache. No sandbox can see the
// store's directory.
//
// Each launch that overlays a cache gets its own directory there — its
// layers and its ledger entry — so launches sharing a home (a kept identity
// home, a bot's) never touch each other's. The launch holds the entry's
// lock file (shared) for as long as it runs and removes the directory when
// it ends; an entry is live while its lock is held, or for LaunchGrace
// after the restore (the launch has not started yet). The published caches
// a live entry names are in use: never evicted. Nothing here is ever read
// from a home, and a home that is never reclaimed pins nothing once its
// launches have ended.

const (
	layersDirName = ".layers"
	warmDirName   = ".warm"
	tooBigDirName = ".toobig"
	storeLockName = ".lock"

	launchLockFile = "lock"
	launchHomeFile = "home"
	launchUsesFile = "uses"
)

// DefaultLaunchGrace is how long a launch's entry counts as live before its
// launch has taken the entry's lock.
const DefaultLaunchGrace = 10 * time.Minute

func (s *Store) layersRoot() string { return filepath.Join(s.Dir, layersDirName) }
func (s *Store) warmRoot() string   { return filepath.Join(s.Dir, warmDirName) }

func (s *Store) grace() time.Duration {
	if s.LaunchGrace != 0 {
		return s.LaunchGrace
	}
	return DefaultLaunchGrace
}

// Locked runs fn holding the store's lock: in this process and, where the
// platform has file locks, against every other conductor process on the
// store. A restore's lookup and its record of what it uses, and an
// eviction's (or a clear's) check of what is in use and its removal, are
// each one critical section, so a launch never gets a cache being removed.
func (s *Store) Locked(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.MkdirAll(s.Dir, 0o700)
	release := lockFile(filepath.Join(s.Dir, storeLockName))
	defer release()
	fn()
}

// newLaunch creates a launch's ledger entry and layers directory, with its
// lock file and the home it is for.
func (s *Store) newLaunch(home string) (string, error) {
	if err := os.MkdirAll(s.layersRoot(), 0o700); err != nil {
		return "", err
	}
	d := filepath.Join(s.layersRoot(), fmt.Sprintf("%d-%s", time.Now().UnixNano(), randHex()))
	if err := os.Mkdir(d, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(d, launchLockFile), nil, 0o600); err != nil {
		return "", err
	}
	return d, writeAtomic(filepath.Join(d, launchHomeFile), []byte(home))
}

// LaunchLock is the lock file of the launch whose layers are under dir.
func LaunchLock(dir string) string { return filepath.Join(dir, launchLockFile) }

// writeUses records the published caches a launch's overlays sit on.
func writeUses(launch string, used []string) error {
	return writeAtomic(filepath.Join(launch, launchUsesFile), []byte(strings.Join(used, "\n")+"\n"))
}

// writeAtomic replaces p in one rename: a reader sees the old content or
// the new, never none.
func writeAtomic(p string, b []byte) error {
	tmp := p + ".tmp-" + randHex()
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type launchEntry struct {
	dir  string
	live bool
}

// launches lists the ledger: each launch's directory and whether it is
// live (its lock held, or restored within the grace).
func (s *Store) launches() []launchEntry {
	ents, _ := os.ReadDir(s.layersRoot())
	var out []launchEntry
	for _, e := range ents {
		d := filepath.Join(s.layersRoot(), e.Name())
		l := launchEntry{dir: d}
		if ns, _, ok := strings.Cut(e.Name(), "-"); ok {
			if n, err := strconv.ParseInt(ns, 10, 64); err == nil && time.Since(time.Unix(0, n)) < s.grace() {
				l.live = true
			}
		}
		if !l.live {
			l.live = lockHeld(filepath.Join(d, launchLockFile))
		}
		out = append(out, l)
	}
	return out
}

// Used are the published caches a live launch's overlays sit on.
func (s *Store) Used() map[string]bool {
	out := map[string]bool{}
	for _, l := range s.launches() {
		if !l.live {
			continue
		}
		f, err := os.Open(filepath.Join(l.dir, launchUsesFile))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				out[t] = true
			}
		}
		f.Close()
	}
	return out
}

// sweepLaunches removes the layers and ledger entry of every launch that
// has ended (its lock free, past the grace) without removing its own.
// The launch's lock is held exclusively through the removal: a launch
// that takes its lock meanwhile waits, then finds its entry gone (and runs
// without its caches) rather than lose its layers under it.
func (s *Store) sweepLaunches() {
	for _, l := range s.launches() {
		if l.live {
			continue
		}
		release, ok := tryLock(filepath.Join(l.dir, launchLockFile))
		if !ok {
			continue
		}
		if s.testHook != nil {
			s.testHook("sweep-locked")
		}
		_ = RemoveAll(l.dir)
		release()
	}
}

// errSwept is a launch lock taken after its entry was swept.
var errSwept = errors.New("the launch's cache layers were swept")

// tooBigMark is the file marking a key whose warm left more than the cap.
func (s *Store) tooBigMark(repo, name, key string) string {
	h := sha256.Sum256([]byte(repo + "\x00" + name + "\x00" + key))
	return filepath.Join(s.Dir, tooBigDirName, hex.EncodeToString(h[:16]))
}

// markTooBig records that key's warm left size bytes, more than the cap.
func (s *Store) markTooBig(repo, name, key string, size int64) {
	p := s.tooBigMark(repo, name, key)
	if os.MkdirAll(filepath.Dir(p), 0o700) == nil {
		_ = writeAtomic(p, []byte(fmt.Sprintf("%s %s %s %d\n", repo, name, key, size)))
	}
}

// isTooBig reports a key whose last warm left more than max (the cap now):
// a raised cap, or none, lets it warm again.
func (s *Store) isTooBig(repo, name, key string, max int64) bool {
	b, err := os.ReadFile(s.tooBigMark(repo, name, key))
	if err != nil || max <= 0 {
		return false
	}
	f := strings.Fields(string(b))
	if len(f) != 4 {
		return false
	}
	size, err := strconv.ParseInt(f[3], 10, 64)
	return err == nil && size > max
}

// ClearMarks removes the over-cap marks of repo's caches (of one name, and
// one key, when given), so they are warmed again.
func (s *Store) ClearMarks(repo, name, key string) {
	dir := filepath.Join(s.Dir, tooBigDirName)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f := strings.Fields(string(b))
		if len(f) < 3 || f[0] != repo || (name != "" && f[1] != name) || (key != "" && f[2] != key) {
			continue
		}
		_ = os.Remove(p)
	}
}
