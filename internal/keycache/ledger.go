package keycache

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A launch's ledger entry and overlay layers live beside the store, never in its home or
// workspace: mount(2) takes an overlay's upper and work directories as
// paths and follows a symlink in them, so an agent able to write where they
// are could point its writes at a published cache. No sandbox can see the
// store's directory.
//
// Each launch that overlays a cache gets its own directory there — its
// layers and its ledger entry — so launches sharing a home (a kept identity
// home, a bot's) never touch each other's. The launch holds the entry's
// lock file (shared) for as long as it runs, and marks the entry ended when
// it stops (EndLaunch). The entry then waits, up to KeepEnded, for its
// step's end: conductor publishes from it (holding its lock exclusively) if
// the step succeeded, and removes it either way. An entry is live while its
// lock is held, or for LaunchGrace after the restore (the launch has not
// started yet). The published caches a live entry names are in use: never
// evicted. Nothing here is ever read from a home, and a home that is never
// reclaimed pins nothing once its launches have ended.

const (
	layersDirName = ".layers"
	storeLockName = ".lock"

	launchLockFile     = "lock"
	launchHomeFile     = "home"
	launchUsesFile     = "uses"
	launchKeyFile      = "key"
	launchDispatchFile = "dispatch"
	launchSlotsFile    = "slots"
	launchEndedFile    = "ended"
)

// DefaultLaunchGrace is how long a launch's entry counts as live before its
// launch has taken the entry's lock.
const DefaultLaunchGrace = 10 * time.Minute

// DefaultKeepEnded is how long an ended launch's layers wait for its step
// to publish them.
const DefaultKeepEnded = time.Hour

func (s *Store) layersRoot() string { return filepath.Join(s.Dir, layersDirName) }

func (s *Store) keepEnded() time.Duration {
	if s.KeepEnded != 0 {
		return s.KeepEnded
	}
	return DefaultKeepEnded
}

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

// launchRecord is what a launch's entry records: the home it is for, the
// key it publishes to (its ID), and its dispatch.
type launchRecord struct {
	home, key, dispatch string
}

// newLaunch creates a launch's ledger entry and layers directory, with its
// lock file, the home it is for, its key and its dispatch.
func (s *Store) newLaunch(rec launchRecord) (string, error) {
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
	for f, v := range map[string]string{launchHomeFile: rec.home, launchKeyFile: rec.key, launchDispatchFile: rec.dispatch} {
		if err := writeAtomic(filepath.Join(d, f), []byte(v)); err != nil {
			return "", err
		}
	}
	return d, nil
}

// EndLaunch marks the launch whose lock is lock as ended (the launcher
// calls it when the launch stops, before it releases the lock): its layers
// wait for its step's end.
func EndLaunch(lock string) {
	if lock == "" || filepath.Base(lock) != launchLockFile {
		return
	}
	_ = writeAtomic(filepath.Join(filepath.Dir(lock), launchEndedFile), []byte(time.Now().UTC().Format(time.RFC3339Nano)))
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
	// ended is when the launch said it stopped (zero: it never did).
	ended time.Time
}

func readSmall(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 4096))
	return strings.TrimSpace(string(b))
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
		if fi, err := os.Lstat(filepath.Join(d, launchEndedFile)); err == nil {
			l.ended = fi.ModTime()
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
// is not live (its lock free, past the grace) and is not waiting for its
// step to publish (ended within KeepEnded); all such when all is set (the
// store is over its cap). The launch's lock is held exclusively through
// the removal: a launch that takes its lock meanwhile waits, then finds its
// entry gone (and runs without its caches) rather than lose its layers
// under it.
func (s *Store) sweepLaunches(all bool) {
	for _, l := range s.launches() {
		if l.live || (!all && !l.ended.IsZero() && time.Since(l.ended) < s.keepEnded()) {
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
