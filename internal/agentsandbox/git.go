package agentsandbox

import (
	"bufio"
	"errors"
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
	// Common is a protected linked worktree's shared common dir: each
	// launch gets a private view of it (privateCommon), with its own config
	// and packed-refs, so normal git works while the operator's stay
	// read-only. "" otherwise.
	Common string
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
		for _, a := range alts {
			if err := trustedAlternate(ws, a); err != nil {
				return GitLayout{}, err
			}
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
		// The pointer is the agent's to write (in a clone it can replace
		// .git with one): bound only when git's own registration agrees.
		if err := trustedPointer(ws, gitdir, common); err != nil {
			return GitLayout{}, err
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
			l.Common = common
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

// GitTrust bounds where a workspace's git paths may lead (set at daemon
// wiring): a layout is read from files an agent can write — a per-dispatch
// clone's alternates, a .git pointer file — so what they name is bound into
// a sandbox only inside these limits.
var GitTrust struct {
	// ObjectRoots are where a clone's borrowed object stores may live:
	// conductor's base clones. Empty: any git dir that passes the
	// structural checks (tests, a daemon without a provisioner).
	ObjectRoots []string
	// Forbidden are directories no git path may be inside: conductor's
	// state and config.
	Forbidden []string
	// OwnRepos are where repositories conductor creates inside its state
	// live (the bots' memory repos): a linked worktree of one, registered
	// by git itself, may name its common dir there.
	OwnRepos []string
	// Home is the operator's home: no git path may be it or contain it.
	Home string
}

// errUntrustedGit wraps every refusal of a workspace's git pointers.
var errUntrustedGit = errors.New("agentsandbox: untrusted git layout")

// trustedPointer checks a linked worktree's .git pointer: its gitdir must be
// <common>/worktrees/<name>, whose own gitdir file points back at this
// workspace's .git — the registration `git worktree add` wrote, which no
// agent can forge for a directory outside its sandbox — and the common dir
// must look like a git dir that is neither the home nor above it. A pointer
// straight at a git dir (no worktree registration) is accepted only inside
// the workspace itself.
func trustedPointer(ws, gitdir, common string) error {
	cws, cgit, ccommon := evalOr(ws), evalOr(gitdir), evalOr(common)
	if err := safeGitPath(ccommon); err != nil {
		return fmt.Errorf("%w: %s: common dir %s: %v", errUntrustedGit, ws, common, err)
	}
	if cgit == ccommon {
		if within(cgit, cws) {
			return nil
		}
		return fmt.Errorf("%w: %s: .git points at %s, which is not a worktree registration of a repository", errUntrustedGit, ws, gitdir)
	}
	if filepath.Dir(cgit) != filepath.Join(ccommon, "worktrees") {
		return fmt.Errorf("%w: %s: .git points at %s, outside %s/worktrees", errUntrustedGit, ws, gitdir, common)
	}
	if !looksLikeGitDir(ccommon) {
		return fmt.Errorf("%w: %s: %s is not a git directory", errUntrustedGit, ws, common)
	}
	b, err := os.ReadFile(filepath.Join(cgit, "gitdir"))
	if err != nil {
		return fmt.Errorf("%w: %s: no worktree registration at %s: %v", errUntrustedGit, ws, gitdir, err)
	}
	back := strings.TrimSpace(string(b))
	if !filepath.IsAbs(back) {
		back = filepath.Join(cgit, back)
	}
	if evalOr(back) != filepath.Join(cws, ".git") {
		return fmt.Errorf("%w: %s: %s is registered for %s, not this workspace", errUntrustedGit, ws, gitdir, back)
	}
	return nil
}

// trustedAlternate checks a clone's borrowed object store: <git dir>/objects
// of a git dir inside the workspace or under GitTrust.ObjectRoots.
func trustedAlternate(ws, objs string) error {
	c := evalOr(objs)
	if filepath.Base(c) != "objects" || !looksLikeGitDir(filepath.Dir(c)) {
		return fmt.Errorf("%w: %s: alternate %s is not a git object store", errUntrustedGit, ws, objs)
	}
	if within(c, evalOr(ws)) {
		return nil
	}
	if err := safeGitPath(filepath.Dir(c)); err != nil {
		return fmt.Errorf("%w: %s: alternate %s: %v", errUntrustedGit, ws, objs, err)
	}
	if len(GitTrust.ObjectRoots) == 0 {
		return nil
	}
	for _, r := range GitTrust.ObjectRoots {
		if r != "" && within(c, evalOr(r)) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s: alternate %s is outside conductor's checkouts", errUntrustedGit, ws, objs)
}

// safeGitPath refuses a git path that is /, the home or above it, or inside
// conductor's own state or config — conductor's own checkouts and
// repositories (inside its state dir) excepted.
func safeGitPath(p string) error {
	for _, r := range append(append([]string(nil), GitTrust.ObjectRoots...), GitTrust.OwnRepos...) {
		if r != "" && within(p, evalOr(r)) {
			return nil
		}
	}
	if p == string(filepath.Separator) {
		return fmt.Errorf("is /")
	}
	if h := GitTrust.Home; h != "" && within(evalOr(h), p) {
		return fmt.Errorf("contains the home directory")
	}
	for _, f := range GitTrust.Forbidden {
		if f != "" && (within(p, evalOr(f)) || within(evalOr(f), p)) {
			return fmt.Errorf("overlaps conductor's own state or config")
		}
	}
	return nil
}

// looksLikeGitDir reports a directory with a git dir's shape: a HEAD file
// and objects and refs directories.
func looksLikeGitDir(p string) bool {
	for _, d := range []string{"objects", "refs"} {
		if fi, err := os.Stat(filepath.Join(p, d)); err != nil || !fi.IsDir() {
			return false
		}
	}
	fi, err := os.Stat(filepath.Join(p, "HEAD"))
	return err == nil && fi.Mode().IsRegular()
}

// evalOr is p with symlinks resolved where it exists, else cleaned.
func evalOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

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

// ValidateRecorded re-checks a recorded entry's git paths — read back from
// the persisted registry, or reused for a follow-up dispatch — against the
// same limits a freshly resolved layout meets: every path absolute and safe
// (safeGitPath), a protected worktree's pair still git's own registration
// for this workspace (trustedPointer), the push dir a git dir, and every
// read-write path inside the workspace or one of the recorded git dirs.
func ValidateRecorded(e Entry) error {
	ws := evalOr(e.Workspace)
	var dirs []string
	check := func(what, p string) error {
		if p == "" {
			return nil
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%w: %s: recorded %s %q is not absolute", errUntrustedGit, e.Workspace, what, p)
		}
		if err := safeGitPath(evalOr(p)); err != nil {
			return fmt.Errorf("%w: %s: recorded %s %s: %v", errUntrustedGit, e.Workspace, what, p, err)
		}
		dirs = append(dirs, evalOr(p))
		return nil
	}
	if err := check("common dir", e.GitCommon); err != nil {
		return err
	}
	if err := check("worktree git dir", e.GitWorktree); err != nil {
		return err
	}
	if e.GitWorktree != "" && e.GitCommon == "" {
		return fmt.Errorf("%w: %s: recorded worktree git dir without its common dir", errUntrustedGit, e.Workspace)
	}
	if e.GitCommon != "" && e.GitWorktree != "" {
		if err := trustedPointer(e.Workspace, e.GitWorktree, e.GitCommon); err != nil {
			return err
		}
	}
	if p := e.Push; p != nil {
		if err := check("push git dir", p.GitDir); err != nil {
			return err
		}
		if p.GitDir != "" && !looksLikeGitDir(p.GitDir) {
			return fmt.Errorf("%w: %s: recorded push git dir %s is not a git directory", errUntrustedGit, e.Workspace, p.GitDir)
		}
		if err := check("push worktree git dir", p.HeadDir); err != nil {
			return err
		}
	}
	trusted := dirs
	for _, rw := range e.ReadWrite {
		if err := check("read-write path", rw); err != nil {
			return err
		}
		c := evalOr(rw)
		ok := within(c, ws) || gitShaped(c)
		for _, d := range trusted {
			ok = ok || within(c, d)
		}
		if !ok {
			return fmt.Errorf("%w: %s: recorded read-write path %s is not in the workspace or a git dir", errUntrustedGit, e.Workspace, rw)
		}
	}
	return nil
}

// gitShaped reports a git dir, a direct subdirectory of one (a common dir's
// objects or refs), or a worktree's own git dir under <git dir>/worktrees.
func gitShaped(p string) bool {
	if looksLikeGitDir(p) {
		return true
	}
	parent := filepath.Dir(p)
	if looksLikeGitDir(parent) {
		return true
	}
	return filepath.Base(parent) == "worktrees" && looksLikeGitDir(filepath.Dir(parent))
}
