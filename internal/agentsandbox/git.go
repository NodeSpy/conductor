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
//   - a linked worktree (.git is a file) — usually of the operator's own
//     repository — needs its own gitdir read-write and the common dir's
//     objects, refs and logs read-write. The rest of the common dir is
//     read-only, and so is everything the operator's own git obeys there:
//     config, hooks, info, every submodule git dir (modules), alternates,
//     replace refs and a shallow graft — and the worktree's .git file,
//     commondir and gitdir, which would otherwise repoint the operator's
//     git at a directory the agent wrote. Sibling worktrees are hidden.
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
		// The .git file itself: repointed, the operator's next git in this
		// worktree would run whatever config the new target holds.
		l.ReadOnly = append(l.ReadOnly, dotgit)
		switch {
		case within(common, ws):
		case SharedGitDirect:
			// sandbox.git.shared: direct — the operator's choice: the common
			// dir read-write (refs, packed-refs, new files at its top), the
			// read-only paths below still protected.
			l.ReadWrite = append(l.ReadWrite, common)
		default:
			// The shared common dir is the operator's repository: read-only,
			// so nothing new appears at its top (a `shallow` file, a config
			// include) — only the object store and the refs an agent's
			// commits, fetches and stash write are read-write.
			l.ReadOnly = append(l.ReadOnly, common)
			for _, sub := range linkedWritable {
				if fi, err := os.Stat(filepath.Join(common, sub)); err == nil && fi.IsDir() {
					l.ReadWrite = append(l.ReadWrite, filepath.Join(common, sub))
				}
			}
		}
		// What the operator's own git obeys or trusts, read-only even inside
		// a read-write path: config, hooks and attributes (info), every
		// submodule's git dir, alternates, replace refs (git show would serve
		// the agent's objects), and a shallow graft. refs/replace and the
		// dirs are created empty when missing (git ignores an empty one), so
		// there is a path to protect; a missing file cannot be created under
		// the read-only common dir.
		for _, sub := range linkedReadOnly {
			p := filepath.Join(common, sub)
			if linkedPrecreate[sub] {
				_ = os.MkdirAll(p, 0o755)
			}
			if _, err := os.Lstat(p); err == nil {
				l.ReadOnly = append(l.ReadOnly, p)
			}
		}
		if gitdir != common {
			l.Hide = append(l.Hide, filepath.Join(common, "worktrees"))
			l.ReadWrite = append(l.ReadWrite, gitdir)
			l.HeadDir = gitdir
			// The worktree's own pointers back into the repository.
			for _, f := range []string{"commondir", "gitdir", "config.worktree"} {
				if _, err := os.Lstat(filepath.Join(gitdir, f)); err == nil {
					l.ReadOnly = append(l.ReadOnly, filepath.Join(gitdir, f))
				}
			}
		}
		return l, nil
	}
	return GitLayout{}, nil
}

// SharedGitDirect is sandbox.git.shared: direct (set at daemon wiring).
var SharedGitDirect bool

// linkedWritable are the common-dir subtrees a linked worktree's git
// writes: new objects, refs (its branch, remote-tracking refs, stash),
// their reflogs, and git-lfs objects.
var linkedWritable = []string{"objects", "refs", "logs", "lfs"}

// linkedReadOnly are the common-dir paths that stay read-only for the agent
// (see ResolveGitLayout); linkedPrecreate those made when missing.
var (
	linkedReadOnly  = []string{"config", "hooks", "info", "modules", filepath.Join("objects", "info"), filepath.Join("refs", "replace"), "shallow", "commondir"}
	linkedPrecreate = map[string]bool{"hooks": true, "modules": true, "info": true, filepath.Join("objects", "info"): true, filepath.Join("refs", "replace"): true}
)

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
