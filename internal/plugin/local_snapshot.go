package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/NodeSpy/conductor/internal/config"
)

// LocalSnapshotRoot is where a local plugin build is snapshotted by content
// hash before it is ever run (config.PluginLocalSnapshotDir).
func LocalSnapshotRoot() string { return config.PluginLocalSnapshotDir() }

// snapshotLocal is the local-build TOCTOU fix. A `use: ./path` reference names
// a file the operator rebuilds at will and verify() has no release sha to pin
// it against, so — left alone — every separate verify()+exec of the RAW path
// (the type-level describe probe, a per-instance describe probe, the live
// spawn, and every crash-respawn in between) could each see different bytes
// if a rebuild lands mid-resolution: verify() hashes one open file handle, but
// the subsequent exec re-opens the path BY NAME, and nothing stops the bytes
// at that name from changing in between or across those separate opens.
//
// Fix: hash the source ONCE per resolution and copy it to a content-addressed,
// private location — <LocalSnapshotRoot>/<sha256>/<name> — then hand the
// caller that snapshot path WITH the sha, so every subsequent verify() for
// this resolution (every probe and the live spawn alike) pins against it
// exactly as it would a verified remote release. The snapshot directory is
// 0700 and the file 0500 (owner read+execute only, no write, for anyone
// including the daemon itself) — once written, nothing can change what that
// path contains, which is what actually closes the window: hash and exec
// always agree because the thing they both read is immutable from the moment
// this function returns.
//
// A rebuilt source binary is invisible until the NEXT resolution (a reload or
// restart calls this again from the then-current source bytes and gets a
// different sha, hence a different snapshot path) — never mid-life: a live
// Client's Spec, once resolved, never re-reads the source path.
//
// Idempotent and cheap once a build has been seen before: if the
// content-addressed destination already exists, the copy is skipped — a
// rebuild that reproduces bytes already snapshotted (or simply resolving the
// same unchanged binary again, e.g. at every `conductor plugin list`) costs
// one hash and one stat, not a fresh copy.
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

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: hashing %s: %w", name, real, err)
	}
	sha = hex.EncodeToString(h.Sum(nil))

	root := LocalSnapshotRoot()
	if root == "" {
		return "", "", fmt.Errorf("plugin %s: local build: no state directory to snapshot into", name)
	}
	dir := filepath.Join(root, sha)
	dst := filepath.Join(dir, name)
	if fi, statErr := os.Stat(dst); statErr == nil && fi.Mode().IsRegular() {
		// Already snapshotted at this content hash — this resolution, an
		// earlier one this boot, or a prior boot entirely — reuse it.
		return dst, sha, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: snapshot dir: %w", name, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // MkdirAll leaves an EXISTING dir's mode alone
		return "", "", fmt.Errorf("plugin %s: local build: snapshot dir perms: %w", name, err)
	}
	// Reuse the SAME handle we just hashed — never re-open the source by path
	// between hash and copy, the same discipline verify() follows between
	// stat and hash.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: rewind: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, ".snap-*")
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
	if _, err := io.Copy(tmp, f); err != nil {
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
	// Read+execute only, no write for ANYONE (not even the daemon's own
	// owner) — set BEFORE the rename so the final path is never even briefly
	// visible writable.
	if err := os.Chmod(tmpName, 0o500); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: snapshot perms: %w", name, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", "", fmt.Errorf("plugin %s: local build: installing snapshot: %w", name, err)
	}
	installed = true
	return dst, sha, nil
}

// GCLocalSnapshots removes every local-build snapshot directory under root
// whose sha256 name is not in keep — the content-addressed copy of a
// NOW-STALE build of some local plugin (the operator rebuilt it and a later
// boot/reload resolved the new one). It never touches a directory whose sha
// IS in keep, so a caller must pass the COMPLETE set of shas every currently-
// resolved local Spec in this process depends on (Manager.LocalSnapshotShas)
// — a partial set would GC a snapshot a sibling plugin's live Client is still
// running from. Best-effort: a removal failure is collected and returned,
// never panics. Returns the sha256 directory names actually removed, for a
// caller that wants to log them.
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
