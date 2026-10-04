package plugin

import (
	"os"
	"path/filepath"
)

// Cross-process install-state locking (finding 6, pre-existing but now more
// consequential now that side-by-side versions makes a reconcile pass
// mutate more install-state records per pass).
//
// installed.yaml has always had only a process-LOCAL mutex (cmd/conductor's
// reconcileMu): it serializes reconcile passes against each other WITHIN one
// `conductor` process, but a concurrent `conductor plugin update` (one OS
// process) and the running daemon's own auto-update cycle (a different OS
// process) each have their OWN, unrelated reconcileMu — nothing stops them
// from both reading installed.yaml, mutating their own in-memory copy, and
// writing it back, with the SECOND writer silently discarding the FIRST
// writer's change (a lost update), even though Save() itself is atomic (temp
// file + rename) — atomicity of one write was never the problem; the gap is
// between one process's read and its own later write.
//
// FileLock is a cross-process ADVISORY lock (flock(2) on a lockfile next to
// installed.yaml) a caller holds across the WHOLE load-mutate-save cycle —
// acquired before LoadInstallState, released after Save(). It is advisory
// (a non-conductor process touching the directory is not blocked by it) and
// best-effort: a platform or filesystem where flock is unavailable (the
// non-unix build, or an NFS mount that refuses it) degrades to a no-op
// rather than failing the caller outright — this is defense in depth over
// the already-atomic Save(), not the only thing standing between two
// writers, and conductor must still run somewhere flock doesn't.
type FileLock struct {
	f *os.File
}

// installLockFileName is the lockfile's name, next to installed.yaml inside
// the same install directory — never committed, never read for content (its
// only purpose is the OS-level lock on the open file description).
const installLockFileName = ".installed.lock"

// LockInstallState acquires dir's cross-process advisory lock, BLOCKING
// until held. Call before LoadInstallState and hold the returned FileLock
// (defer its Unlock) until after Save() — the whole load-mutate-save cycle,
// so no other `conductor` process's own cycle can interleave with this
// one's read and later write.
//
// dir == "" (no state directory resolvable) returns a harmless no-op lock:
// there is no installed.yaml to race over in the first place.
func LockInstallState(dir string) (*FileLock, error) {
	if dir == "" {
		return &FileLock{}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, installLockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFileBlocking(f); err != nil {
		// Best-effort: a platform/filesystem without flock degrades to no
		// cross-process synchronization rather than refusing to reconcile
		// at all.
		_ = f.Close()
		return &FileLock{}, nil
	}
	return &FileLock{f: f}, nil
}

// Unlock releases the lock and closes its file handle. Safe to call on a
// nil *FileLock or a no-op lock (dir == "", or flock unavailable).
func (l *FileLock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	uerr := unlockFile(l.f)
	cerr := l.f.Close()
	l.f = nil
	if uerr != nil {
		return uerr
	}
	return cerr
}
