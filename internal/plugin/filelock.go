package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
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

// openLockFile opens (creating if needed) dir's lockfile, refusing to
// follow it if it is a symlink (finding 6, hardening): O_NOFOLLOW on unix
// means a lockfile path an attacker replaced with a symlink to an arbitrary
// file conductor has write access to — ~/.ssh/authorized_keys, say — is
// refused with a clear error instead of silently flock(2)'d (and, since the
// lock is held across the whole load-mutate-save cycle, potentially
// WRITTEN to if the symlink's target happened to collide with a path
// conductor also opens for writing elsewhere). A platform with no O_NOFOLLOW
// wired up (filelock_other.go) performs no symlink check — it also performs
// no locking at all, so there is nothing for a symlinked lockfile to gain.
func openLockFile(dir string) (*os.File, error) {
	path := filepath.Join(dir, installLockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|noFollowFlag, 0o600)
	if err != nil {
		if isSymlinkRefusalErr(err) {
			return nil, fmt.Errorf("plugin install lock %s is a symlink — refusing to follow it", path)
		}
		return nil, err
	}
	return f, nil
}

// LockInstallState acquires dir's cross-process advisory lock, BLOCKING
// until held, with NO timeout. Call before LoadInstallState and hold the
// returned FileLock (defer its Unlock) until after Save() — the whole
// load-mutate-save cycle, so no other `conductor` process's own cycle can
// interleave with this one's read and later write.
//
// This is for the daemon's OWN background reconcile passes (autoUpdateLoop,
// the boot gap-fill) — unattended, so a bound here would just turn a
// slow-but-eventually-finishing peer (a concurrent `conductor plugin
// update`, say) into a spurious failure with nobody watching to retry. An
// INTERACTIVE CLI command should use LockInstallStateCLI instead, which
// notifies and bounds the wait so a human is never left staring at a silent,
// indefinite hang.
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
	f, err := openLockFile(dir)
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

// LockInstallStateCLI is LockInstallState for an interactive CLI command
// (`conductor init`, `plugin add`/`update`/`remove`): it tries the lock
// WITHOUT blocking first, so the overwhelmingly common uncontended case pays
// no delay at all. If another process already holds it — most likely the
// daemon's own auto-update cycle, or a concurrent `conductor plugin`
// invocation — notify (never nil-checked by the caller; pass a no-op if you
// don't care) is called once with a message naming that, so the operator is
// never left watching a silent hang, and the call then blocks for up to
// timeout before giving up with a clear, named error.
//
// A non-positive timeout blocks exactly like LockInstallState (no bound) —
// callers that always want a bound should pass a positive one (10 minutes
// for a CLI command is generous enough to outlast almost any legitimate
// peer while still eventually giving the operator their terminal back).
func LockInstallStateCLI(dir string, timeout time.Duration, notify func(string)) (*FileLock, error) {
	if dir == "" {
		return &FileLock{}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := openLockFile(dir)
	if err != nil {
		return nil, err
	}
	err = lockFileNonBlocking(f)
	if err == nil {
		return &FileLock{f: f}, nil
	}
	if !isLockHeldErr(err) {
		// flock unsupported on this platform/filesystem: degrade exactly
		// like LockInstallState's own best-effort fallback.
		_ = f.Close()
		return &FileLock{}, nil
	}
	if notify != nil {
		notify("waiting for another conductor process (the daemon's plugin update?) to finish…")
	}
	if timeout <= 0 {
		if err := lockFileBlocking(f); err != nil {
			_ = f.Close()
			return &FileLock{}, nil
		}
		return &FileLock{f: f}, nil
	}
	done := make(chan error, 1)
	go func() { done <- lockFileBlocking(f) }()
	select {
	case err := <-done:
		if err != nil {
			_ = f.Close()
			return &FileLock{}, nil
		}
		return &FileLock{f: f}, nil
	case <-time.After(timeout):
		// The goroutine above is left running: f is never closed here, so
		// if it eventually acquires the lock (whenever the current holder
		// releases it) that's a harmless no-op nobody reads — this process
		// reports the timeout and exits shortly after, which closes f and
		// releases whatever it may have just acquired. Nothing leaks beyond
		// this process's own lifetime.
		return nil, fmt.Errorf("plugin install state has been locked by another conductor process for longer than %s — is the daemon mid plugin-update, or another `conductor plugin` command still running? try again once it finishes", timeout)
	}
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
