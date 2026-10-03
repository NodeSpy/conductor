package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// LocalSnapshotRoot is where a local plugin build is snapshotted by content
// hash before it is ever run (config.PluginLocalSnapshotDir).
func LocalSnapshotRoot() string { return config.PluginLocalSnapshotDir() }

// DefaultLocalSnapshotGrace is how long a local-build snapshot may go
// untouched (GCLocalSnapshotsOld) before it is presumed abandoned. Long
// enough that a daemon down over a weekend, or a sibling daemon sharing this
// state dir that simply hasn't resolved its own plugins yet this boot, never
// loses a snapshot it still depends on.
const DefaultLocalSnapshotGrace = 7 * 24 * time.Hour

// snapshotLocal is the local-build TOCTOU fix. A `use: ./path` reference names
// a file the operator rebuilds at will and verify() has no release sha to pin
// it against, so — left alone — every separate verify()+exec of the RAW path
// (the type-level describe probe, a per-instance describe probe, the live
// spawn, and every crash-respawn in between) could each see different bytes
// if a rebuild lands mid-resolution: verify() hashes one open file handle, but
// the subsequent exec re-opens the path BY NAME, and nothing stops the bytes
// at that name from changing in between or across those separate opens.
//
// Fix: copy the source to a content-addressed, private location —
// <LocalSnapshotRoot>/<sha256>/<name> — HASHING THE BYTES AS THEY ARE WRITTEN
// (one read of the source, through io.MultiWriter, never a separate hash pass
// followed by a second read that could see different bytes if the source is
// mutated in place between the two), then hand the caller that snapshot path
// WITH the sha, so every subsequent verify() for this resolution (every probe
// and the live spawn alike) pins against it exactly as it would a verified
// remote release. The snapshot directory is 0700 and the file 0500 (owner
// read+execute only, no write, for anyone including the daemon itself) — once
// written, nothing can change what that path contains, which is what actually
// closes the window: hash and exec always agree because the thing they both
// read is immutable from the moment this function returns.
//
// Both the per-sha directory and the per-sha root are confirmed to be REAL
// directories owned by this process's own uid, with no group/world
// permission bits, via Lstat (never Stat, which would follow a symlink) —
// never reused, chmod'ed, or written through if they are a symlink or owned
// by someone else. This matters because the state dir's local/ subtree can
// outlive any one daemon process (a restart, an upgrade, a multi-tenant box)
// and nothing about its ownership is re-verified elsewhere.
//
// A rebuilt source binary is invisible until the NEXT resolution (a reload or
// restart calls this again from the then-current source bytes and gets a
// different sha, hence a different snapshot path) — never mid-life: a live
// Client's Spec, once resolved, never re-reads the source path.
//
// Idempotent once a build has been seen before: if the content-addressed
// destination already exists (and passes the same symlink/ownership
// scrutiny), the just-written temp copy is discarded rather than installed —
// a rebuild that reproduces bytes already snapshotted (or simply resolving
// the same unchanged binary again, e.g. at every `conductor plugin list`)
// still reads and re-hashes the source once, but never duplicates the copy.
// Every resolve — fresh write or reuse alike — touches the snapshot
// directory's mtime (touchSnapshotUsed) so GCLocalSnapshotsOld's grace-period
// GC never mistakes a snapshot still being resolved for an abandoned one.
func snapshotLocal(name, srcPath string) (snapPath, sha string, err error) {
	real, err := filepath.EvalSymlinks(srcPath)
	if err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: cannot resolve %s: %w", name, srcPath, err)
	}
	f, err := os.Open(real)
	if err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: cannot open %s: %w", name, real, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: cannot stat %s: %w", name, real, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("plugin %s: local build: %s is a directory", name, real)
	}

	root := LocalSnapshotRoot()
	if root == "" {
		return "", "", fmt.Errorf("plugin %s: local build: no state directory to snapshot into", name)
	}
	if err := secureSnapshotDir(root); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: %w", name, err)
	}

	// Copy to a scratch temp file directly under root WHILE hashing — the
	// hash and the bytes on disk are, by construction, the exact same read of
	// the source; there is no second pass that could observe different bytes.
	tmp, err := os.CreateTemp(root, ".snap-*")
	if err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: snapshot temp file: %w", name, err)
	}
	tmpName := tmp.Name()
	installed := false
	defer func() {
		if !installed {
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), f); err != nil {
		tmp.Close()
		return "", "", fmt.Errorf("plugin %s: local build: copying snapshot: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", "", fmt.Errorf("plugin %s: local build: syncing snapshot: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: closing snapshot: %w", name, err)
	}
	sha = hex.EncodeToString(h.Sum(nil))

	dir := filepath.Join(root, sha)
	dst := filepath.Join(dir, name)

	// The per-sha dir is Lstat-validated (never reused, chmod'ed, or written
	// through if it is a symlink or not ours) BEFORE either the reuse check
	// or the write below — reuse needs it just as much as a fresh write
	// does, since it is what makes `dst` trustworthy at all.
	if err := secureSnapshotDir(dir); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: %w", name, err)
	}

	if dfi, lerr := os.Lstat(dst); lerr == nil {
		// Already snapshotted at this content hash — this resolution, an
		// earlier one this boot, or a prior boot (or another daemon sharing
		// this state dir) entirely. Reuse it ONLY if it is exactly what we
		// would have written: a real regular file, not a symlink.
		if dfi.Mode()&os.ModeSymlink != 0 || !dfi.Mode().IsRegular() {
			return "", "", fmt.Errorf("plugin %s: local build: snapshot file %s is not a plain regular file — refusing to reuse or overwrite it", name, dst)
		}
		touchSnapshotUsed(dir)
		return dst, sha, nil
	} else if !os.IsNotExist(lerr) {
		return "", "", fmt.Errorf("plugin %s: local build: stat snapshot %s: %w", name, dst, lerr)
	}

	// Read+execute only, no write for ANYONE (not even the daemon's own
	// owner) — set BEFORE the rename so the final path is never even briefly
	// visible writable. chmod on the TEMP path we just created (never on a
	// path that could be a symlink: it is our own freshly-created file).
	if err := os.Chmod(tmpName, 0o500); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: snapshot perms: %w", name, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: installing snapshot: %w", name, err)
	}
	installed = true
	touchSnapshotUsed(dir)
	return dst, sha, nil
}

// secureSnapshotDir ensures dir is a REAL directory (never a symlink) owned
// by this process's own effective uid, with no group/world permission bits
// — creating it fresh at exactly 0700 if it does not exist yet. It refuses
// (never silently reuses, and NEVER chmods through) anything looser: this
// runs against paths under the state dir's local/ subtree, which can outlive
// any one daemon process (restarts, upgrades, a multi-tenant box) and whose
// ownership is not re-verified anywhere else before snapshotLocal reuses or
// writes beneath it.
func secureSnapshotDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat snapshot dir %s: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create snapshot dir %s: %w", dir, err)
		}
		// MkdirAll leaves an EXISTING ancestor's mode alone; re-stat (Lstat,
		// not Stat) the leaf we just asked for and re-check it below rather
		// than trusting the create call succeeded exactly as asked.
		fi, err = os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("stat snapshot dir %s after create: %w", dir, err)
		}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snapshot dir %s is a symlink — refusing to reuse or write through it", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("snapshot dir %s exists and is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if ok && st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("snapshot dir %s is owned by uid %d, not this process (uid %d) — refusing to reuse it", dir, st.Uid, os.Geteuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		// Never chmod through what might be a symlink or a dir we do not
		// own — surfaced as a hard refusal so the operator fixes the state
		// dir's permissions themselves, rather than conductor silently
		// tightening (or loosening) something it does not fully control.
		return fmt.Errorf("snapshot dir %s has group/world permission bits set (%v) — refusing to reuse it; remove it and let conductor recreate it", dir, fi.Mode().Perm())
	}
	return nil
}

// touchSnapshotUsed updates dir's mtime to "now" so GCLocalSnapshotsOld (the
// cross-daemon-safe, grace-period GC) treats it as recently used. Called on
// every RESOLVE (snapshotLocal, whether a fresh write or a cache hit) and on
// every SPAWN of a local snapshot (Client.ensureLocked, after verify()
// succeeds) — the two moments docs/design/plugin-contract.md's GC
// coordination promises never race a deletion: a respawn always touches
// before it execs, and GC only ever removes something that has sat
// completely untouched for the whole grace window.
//
// Best-effort: a failure (a read-only mount, a permissions hiccup) is not
// fatal — it just makes this snapshot look more stale to GC than it really
// is. For THIS process, GCLocalSnapshots' exact `keep` set (every sha a live
// Manager in this process actually resolved) already guards it regardless of
// mtime; the grace period exists specifically for a snapshot outside that
// set — a sibling daemon's, or one from an earlier process whose Manager
// already exited — so a single missed touch does not immediately cost it its
// snapshot.
func touchSnapshotUsed(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}

// GCLocalSnapshots removes every local-build snapshot directory under root
// whose sha256 name is not in keep — the content-addressed copy of a
// NOW-STALE build of some local plugin (the operator rebuilt it and a later
// boot/reload resolved the new one). It never touches a directory whose sha
// IS in keep, so a caller must pass the COMPLETE set of shas every currently-
// resolved local Spec in this process depends on (Manager.LocalSnapshotShas)
// — a partial set would GC a snapshot a sibling plugin's live Client is still
// running from.
//
// This alone is NOT safe against a snapshot a DIFFERENT daemon process
// (sharing this state dir) depends on, since that process's live shas are
// invisible to this one's `keep` — see GCLocalSnapshotsOld, which adds the
// cross-process safety net this one cannot provide on its own. Best-effort:
// a removal failure is collected and returned, never panics. Returns the
// sha256 directory names actually removed, for a caller that wants to log
// them.
func GCLocalSnapshots(root string, keep map[string]bool) (removed []string, errs []error) {
	if root == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	for _, e := range ents {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("local snapshot %s: %w", e.Name(), err))
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, errs
}

// GCLocalSnapshotsOld is the CROSS-DAEMON-safe half of GC coordination
// (docs/wiki/Plugins.md "Local builds are snapshotted"): two daemons sharing
// a state dir, or a GC racing a live process's respawn, must not delete a
// snapshot in use — but a snapshot belonging to ANOTHER daemon process (or
// simply one this process has not resolved YET this boot) is invisible to
// GCLocalSnapshots' `keep` set. This removes a directory under root ONLY if
// its mtime is older than olderThan: touchSnapshotUsed refreshes it on every
// resolve and every spawn (snapshotLocal, Client.ensureLocked), so anything
// genuinely live — this process's own resolutions, a sibling daemon's, or
// one about to be respawned after a crash — has a recent mtime and survives;
// only a directory nothing has touched in the whole grace window is
// presumed abandoned. Pair with GCLocalSnapshots (the exact, this-process
// `keep`-set GC) rather than using either alone: the keep set is exact but
// blind to other processes; the grace period covers other processes but
// needs a window wide enough nothing legitimate ages out of it.
//
// Best-effort, like GCLocalSnapshots: a removal failure is collected and
// returned, never panics.
func GCLocalSnapshotsOld(root string, olderThan time.Duration) (removed []string, errs []error) {
	if root == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			errs = append(errs, fmt.Errorf("local snapshot %s: %w", e.Name(), err))
			continue
		}
		if info.ModTime().After(cutoff) {
			continue // touched within the grace window — presumed live
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("local snapshot %s: %w", e.Name(), err))
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, errs
}

// GCLocalSnapshotsSafely is the GC conductor's own boot actually runs
// (cmd/conductor's gcLocalPluginSnapshots): a snapshot is removed only if it
// is BOTH unreferenced by any Manager THIS process built (keep —
// GCLocalSnapshots' exact signal) AND has sat untouched past olderThan
// (GCLocalSnapshotsOld's cross-daemon-safe signal). Combining them is
// strictly safer than either alone: the exact keep set is blind to a
// SIBLING daemon sharing this state dir (whose live snapshot is simply not
// in keep), and the grace period alone would unnecessarily delay cleaning
// up something this process already knows with certainty is gone (e.g. a
// local plugin rebuilt many times across restarts). Best-effort, like both:
// a removal failure is collected and returned, never panics.
func GCLocalSnapshotsSafely(root string, keep map[string]bool, olderThan time.Duration) (removed []string, errs []error) {
	if root == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range ents {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			errs = append(errs, fmt.Errorf("local snapshot %s: %w", e.Name(), err))
			continue
		}
		if info.ModTime().After(cutoff) {
			continue // not in THIS process's keep set, but touched recently — a
			// sibling daemon's, or one about to be respawned; presumed live.
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("local snapshot %s: %w", e.Name(), err))
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, errs
}
