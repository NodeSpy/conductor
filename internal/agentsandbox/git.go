package agentsandbox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GitPaths are the paths outside a workspace its git needs, read while the
// workspace is still conductor's own — when it is provisioned, before any
// agent ran in it; nothing here is re-read from the agent-writable checkout
// later.
//
//   - a per-dispatch clone (.git is a directory) borrows the base clone's
//     objects through objects/info/alternates: those object stores,
//     read-only. Its own refs, config and objects are inside the workspace.
//   - a linked worktree (.git is a file) needs its own gitdir read-write and
//     the common dir's objects and refs read-write — but never the common
//     dir's config, hooks or alternates, which conductor's own git in the
//     base clone obeys — and its sibling worktrees' gitdirs are hidden.
//
// A workspace that is not a git checkout needs nothing.
type GitLayout struct {
	ReadOnly  []string
	ReadWrite []string
	Hide      []string
	// GitDir is the trusted git dir conductor's own git uses for this
	// workspace (its pushes): the base clone's .git behind a per-dispatch
	// clone, the common dir of a linked worktree. "" when there is none.
	GitDir string
	// Clone reports a per-dispatch clone (its objects are borrowed by
	// snapshot, never through the clone's own alternates).
	Clone bool
	// HeadDir is a linked worktree's own git dir (where its HEAD is); ""
	// for a clone.
	HeadDir string
}

// ResolveGitLayout reads ws's git layout (see GitLayout).
func ResolveGitLayout(ws string) (GitLayout, error) {
	dotgit := filepath.Join(ws, ".git")
	fi, err := os.Lstat(dotgit)
	if err != nil {
		return GitLayout{}, nil
	}
	switch {
	case fi.IsDir():
		alts, err := readAlternates(filepath.Join(dotgit, "objects", "info", "alternates"), filepath.Join(dotgit, "objects"))
		if err != nil {
			return GitLayout{}, err
		}
		l := GitLayout{ReadOnly: alts, Clone: len(alts) > 0}
		if len(alts) == 1 && filepath.Base(alts[0]) == "objects" {
			l.GitDir = filepath.Dir(alts[0])
		}
		return l, nil
	case fi.Mode().IsRegular():
		b, err := os.ReadFile(dotgit)
		if err != nil {
			return GitLayout{}, err
		}
		gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(ws, gitdir)
		}
		gitdir = filepath.Clean(gitdir)
		common := gitdir
		if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
			cd := strings.TrimSpace(string(c))
			if !filepath.IsAbs(cd) {
				cd = filepath.Join(gitdir, cd)
			}
			common = filepath.Clean(cd)
		}
		if fi, err := os.Stat(common); err != nil || !fi.IsDir() {
			return GitLayout{}, fmt.Errorf("agentsandbox: %s: git common dir %s is not a directory", ws, common)
		}
		l := GitLayout{GitDir: common}
		if !within(common, ws) {
			l.ReadWrite = append(l.ReadWrite, common)
		}
		for _, sub := range []string{"config", "hooks", filepath.Join("objects", "info")} {
			p := filepath.Join(common, sub)
			if sub != "config" {
				_ = os.MkdirAll(p, 0o755)
			}
			if _, err := os.Stat(p); err == nil {
				l.ReadOnly = append(l.ReadOnly, p)
			}
		}
		if gitdir != common {
			l.Hide = append(l.Hide, filepath.Join(common, "worktrees"))
			l.ReadWrite = append(l.ReadWrite, gitdir)
			l.HeadDir = gitdir
		}
		return l, nil
	}
	return GitLayout{}, nil
}

// readAlternates reads an alternates file: absolute object directories, and
// relative ones resolved against objDir. Each must exist.
func readAlternates(p, objDir string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(objDir, line)
		}
		line = filepath.Clean(line)
		if fi, err := os.Stat(line); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("agentsandbox: alternate object store %s is not a directory", line)
		}
		out = append(out, line)
	}
	return out, sc.Err()
}
