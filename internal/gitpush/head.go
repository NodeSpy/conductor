package gitpush

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// isSHA reports a full object id (SHA-1 or SHA-256).
func isSHA(s string) bool { return (len(s) == 40 || len(s) == 64) && isHex(s) }

// headDir is where a checkout's HEAD lives, relative to root: a clone's
// .git, or — for a linked worktree, whose root is its own git dir — "".
func headRel(clone bool, name string) string {
	if clone {
		return filepath.Join(".git", name)
	}
	return name
}

// CheckedOutBranch is the branch a checkout is on ("" when detached), read
// from its HEAD file. For a linked worktree (.git is a file) the git dir it
// names is followed — call it only while the checkout is conductor's own
// (when it is provisioned), as the sandbox registry does.
func CheckedOutBranch(ws string) (string, error) {
	fi, err := os.Lstat(filepath.Join(ws, ".git"))
	if err != nil {
		return "", err
	}
	var b []byte
	if fi.IsDir() {
		b, err = readUnder(ws, ".git/HEAD", 4096)
	} else {
		var p []byte
		if p, err = os.ReadFile(filepath.Join(ws, ".git")); err == nil {
			gd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(p)), "gitdir:"))
			if !filepath.IsAbs(gd) {
				gd = filepath.Join(ws, gd)
			}
			b, err = os.ReadFile(filepath.Join(gd, "HEAD"))
		}
	}
	if err != nil {
		return "", err
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
	if !ok {
		return "", nil
	}
	return ref, nil
}

// readHead resolves the commit a checkout's HEAD names, reading only under
// root with O_NOFOLLOW: root/<.git/>HEAD, then (a symbolic HEAD) the loose
// ref or packed-refs under refsRoot. It returns the sha and the branch HEAD
// is on ("" when detached).
func readHead(root string, clone bool) (sha, branch string, err error) {
	b, err := readUnder(root, headRel(clone, "HEAD"), 4096)
	if err != nil {
		return "", "", fmt.Errorf("read HEAD: %w", err)
	}
	h := strings.TrimSpace(string(b))
	if isSHA(h) {
		return h, "", nil
	}
	ref, ok := strings.CutPrefix(h, "ref: ")
	if !ok || !strings.HasPrefix(ref, "refs/heads/") {
		return "", "", fmt.Errorf("HEAD is neither a commit nor a branch")
	}
	branch = strings.TrimPrefix(ref, "refs/heads/")
	if err := CheckBranch(branch); err != nil {
		return "", "", err
	}
	if !clone {
		// A linked worktree's branch lives in the common dir: resolved by
		// conductor's git there (the caller).
		return "", branch, nil
	}
	if lb, err := readUnder(root, filepath.Join(".git", ref), 4096); err == nil {
		if s := strings.TrimSpace(string(lb)); isSHA(s) {
			return s, branch, nil
		}
		return "", "", fmt.Errorf("%s does not hold a commit id", ref)
	}
	pb, err := readUnder(root, ".git/packed-refs", 64<<20)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	for _, line := range strings.Split(string(pb), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == ref && isSHA(f[0]) {
			return f[0], branch, nil
		}
	}
	return "", "", fmt.Errorf("%s is not set", ref)
}

// splitRel splits a relative path into its components ("" and "." dropped;
// ".." cannot escape: the path is cleaned as if rooted).
func splitRel(rel string) []string {
	var out []string
	for _, c := range strings.Split(filepath.Clean("/"+rel), "/") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}
