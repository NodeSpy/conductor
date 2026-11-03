package agentsandbox

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// A protected linked worktree's common git dir is the operator's repository:
// read-only for the agent but for its objects, refs, logs and lfs. Git writes
// config and packed-refs by creating a lock file beside them and renaming it
// into place, which a read-only directory refuses — `git config --local`,
// `git gc`, `pack-refs` and deleting a packed branch failed, and every ref
// update complained about packed-refs.lock. Each launch therefore gets a
// private view of the common dir: a fresh directory of its own holding a copy
// of config, packed-refs and HEAD, with the real objects, refs, logs and lfs
// (read-write) and hooks, info, modules, shallow and the worktree's own git
// dir (as before) placed inside it. The operator's config and packed-refs are
// never written.
//
// Linux: the jail binds the private directory at the common dir's own path
// and the real subtrees into it, so every git (whatever its environment)
// sees one consistent repository. macOS has no mounts: GIT_COMMON_DIR points
// at the private directory, whose subtrees are symlinks to the real ones;
// git's ref store still uses the real common dir (it reads the worktree's
// commondir file), so the real packed-refs and its lock are writable there —
// the same refs the agent can already write as loose files.

// privateCommonDir is where a launch's private view of its common dir is
// built: inside its home, rebuilt for every launch.
func privateCommonDir(home string) string { return filepath.Join(home, ".conductor", "git-common") }

// privateConfig is appended to the private config: nothing gc writes lands
// in the read-only info and objects/info.
const privateConfig = "\n# Added by conductor: the shared repository's info and objects/info are read-only.\n[repack]\n\tupdateServerInfo = false\n[gc]\n\twriteCommitGraph = false\n[fetch]\n\twriteCommitGraph = false\n"

// privateCopies are the common-dir files the private view holds its own
// copies of.
var privateCopies = []string{"config", "packed-refs", "HEAD"}

// preparePrivateCommon builds dir as the launch's private view of common
// (worktree: the worktree's own git dir, under common/worktrees). links:
// subtrees as symlinks (macOS), else empty mount points (the jail binds the
// real ones in).
func preparePrivateCommon(dir, common, worktree string, links bool) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, f := range privateCopies {
		if err := copyRegular(filepath.Join(common, f), filepath.Join(dir, f)); err != nil {
			return err
		}
	}
	// gc's caches beside the protected paths (info/refs and objects/info:
	// attributes and alternates live there) are not written: the private
	// config's own settings, after (so over) the operator's.
	f, err := os.OpenFile(filepath.Join(dir, "config"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(privateConfig)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	subtrees := append(append([]string(nil), linkedWritable...), "hooks", "info", "modules", "shallow", "commondir")
	for _, sub := range subtrees {
		real := filepath.Join(common, sub)
		fi, err := os.Lstat(real)
		if err != nil {
			continue
		}
		at := filepath.Join(dir, sub)
		switch {
		case links:
			err = os.Symlink(real, at)
		case fi.IsDir():
			err = os.Mkdir(at, 0o755)
		default:
			err = os.WriteFile(at, nil, 0o644)
		}
		if err != nil {
			return err
		}
	}
	if worktree == "" || filepath.Dir(worktree) != filepath.Join(common, "worktrees") {
		return nil
	}
	wts := filepath.Join(dir, "worktrees")
	if err := os.Mkdir(wts, 0o755); err != nil {
		return err
	}
	at := filepath.Join(wts, filepath.Base(worktree))
	if links {
		return os.Symlink(worktree, at)
	}
	return os.Mkdir(at, 0o755)
}

// copyRegular copies src to dst when src is a regular file (a missing one is
// skipped; a symlink is never followed).
func copyRegular(src, dst string) error {
	f, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("agentsandbox: %s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// readPackedRefs reads a packed-refs file: ref name to object id.
func readPackedRefs(p string) map[string]string {
	out := map[string]string{}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		if id, ref, ok := strings.Cut(line, " "); ok {
			out[ref] = id
		}
	}
	return out
}

var (
	objectID   = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	restorable = regexp.MustCompile(`^refs/(heads|tags|remotes|notes)/[A-Za-z0-9_+-][A-Za-z0-9._+/-]*$`)
)

// validRefName reports a branch, tag, remote-tracking or notes ref with a
// plain name (no .., no component starting with a dot, no lock suffix).
func validRefName(ref string) bool {
	if !restorable.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") ||
		strings.Contains(ref, "/.") || strings.HasSuffix(ref, ".lock") || strings.HasSuffix(ref, "/") {
		return false
	}
	return true
}

// reconcilePrivateRefs keeps what the launch's git moved into its private
// packed-refs (git pack-refs, git gc) from being lost with it: every ref
// there that the repository itself no longer resolves to the same object —
// no loose ref, and the real packed-refs disagrees — is written back as a
// loose ref, never through a symlink. A ref the launch deleted from its
// packed-refs stays in the operator's. Runs after the launch has ended.
func reconcilePrivateRefs(dir, common string) []string {
	mine := readPackedRefs(filepath.Join(dir, "packed-refs"))
	theirs := readPackedRefs(filepath.Join(common, "packed-refs"))
	var restored []string
	for ref, id := range mine {
		if !validRefName(ref) || !objectID.MatchString(id) || theirs[ref] == id {
			continue
		}
		if _, err := os.Lstat(filepath.Join(common, ref)); err == nil {
			continue // a loose ref wins in both views
		}
		if writeLooseRef(common, ref, id) == nil {
			restored = append(restored, ref)
		}
	}
	return restored
}

// writeLooseRef creates common/ref holding id: every directory on the way is
// a real directory (created when missing), never a symlink, and an existing
// ref is never replaced.
func writeLooseRef(common, ref, id string) error {
	parts := strings.Split(ref, "/")
	at := common
	for _, p := range parts[:len(parts)-1] {
		at = filepath.Join(at, p)
		fi, err := os.Lstat(at)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(at, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("agentsandbox: %s is not a directory", at)
		}
	}
	final := filepath.Join(at, parts[len(parts)-1])
	tmp := final + ".conductor-tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(id + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Link(tmp, final) // fails if a ref appeared meanwhile
	}
	_ = os.Remove(tmp)
	return err
}
