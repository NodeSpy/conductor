package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// verify implements verify-before-execute (§8.4): it confirms the binary at
// s.BinPath matches the configured SHA-256 pin, reading from a path with safe
// permissions, and returns the hex digest. It is called BEFORE the binary is
// ever spawned. A mismatch, an unpinned-and-not-opted-in plugin, or an
// attacker-writable path is a hard refusal — never exec-then-check.
//
// "Safe permissions" means: neither the binary nor any parent directory up to
// the filesystem root is world-writable (a world-writable dir lets an attacker
// swap the binary between verify and exec — the classic TOCTOU window; we
// additionally re-stat by opening the file once and hashing that same handle).
func verify(s Spec) (digest string, err error) {
	if s.BinPath == "" {
		return "", fmt.Errorf("plugin %s: empty source path", s.Name)
	}
	if !filepath.IsAbs(s.BinPath) {
		return "", fmt.Errorf("plugin %s: source path is not absolute: %s", s.Name, s.BinPath)
	}

	// Resolve symlinks FIRST and verify the real target: otherwise a symlink in
	// a safe directory could point at an attacker-writable target whose
	// permissions we'd never inspect (and whose content could be swapped after
	// verification). Everything below — perms, parent walk, hash, exec — must
	// concern the resolved path, not the link.
	real, err := filepath.EvalSymlinks(s.BinPath)
	if err != nil {
		return "", fmt.Errorf("plugin %s: cannot resolve source: %w", s.Name, err)
	}

	// Open once and hash THAT handle, so the bytes we verify are the bytes we
	// will exec (defends against a swap between stat and hash).
	f, err := os.Open(real)
	if err != nil {
		return "", fmt.Errorf("plugin %s: cannot open source: %w", s.Name, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("plugin %s: cannot stat source: %w", s.Name, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("plugin %s: source is a directory: %s", s.Name, real)
	}
	// Group- OR world-writable is refused: in a shared deploy group a
	// group-writable binary is just as attacker-swappable as a world-writable
	// one, defeating the sha pin. Operators pin at mode 0755.
	if info.Mode()&0o022 != 0 {
		return "", fmt.Errorf("plugin %s: source is group/world-writable (%v) — refusing to run an attacker-swappable binary: %s", s.Name, info.Mode(), real)
	}
	if err := checkParentPerms(real); err != nil {
		return "", fmt.Errorf("plugin %s: %w", s.Name, err)
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("plugin %s: hashing source: %w", s.Name, err)
	}
	digest = hex.EncodeToString(h.Sum(nil))

	if s.Sha256 == "" {
		if s.AllowUnverified {
			// Explicit, insecure dev opt-in: no pin to check against. The
			// caller logs a prominent warning; we still return the digest so
			// it can be shown/pinned.
			return digest, nil
		}
		return "", fmt.Errorf("plugin %s: no sha256 pin configured (set sha256: %s, or allow_unverified: true for dev)", s.Name, digest)
	}
	if !strings.EqualFold(digest, s.Sha256) {
		return "", fmt.Errorf("plugin %s: sha256 mismatch — refusing to execute (pinned %s, on disk %s)", s.Name, strings.ToLower(s.Sha256), digest)
	}
	return digest, nil
}

// VerifyOnly runs verify-before-execute (sha + safe perms) and discards the
// digest — the health check `plugin list` uses without spawning the binary.
func VerifyOnly(s Spec) error {
	_, err := verify(s)
	return err
}

// checkParentPerms walks from the binary's directory up to root and refuses any
// group- or world-writable directory without the sticky bit — such a directory
// lets an attacker (same-group, or anyone) replace the binary (or an ancestor)
// out from under us, defeating the sha pin regardless of the file's own mode.
func checkParentPerms(path string) error {
	dir := filepath.Dir(path)
	for {
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("cannot stat parent %s: %w", dir, err)
		}
		// Group- or world-writable AND not sticky (0o1000) is the dangerous
		// case; /tmp is world-writable but sticky, which prevents cross-user
		// swaps. Group-writable ancestors matter for the same shared-deploy-
		// group reason the file check refuses group-write.
		if info.Mode()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("parent directory is group/world-writable without sticky bit (%v) — attacker-swappable: %s", info.Mode(), dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil // reached root
		}
		dir = parent
	}
}
