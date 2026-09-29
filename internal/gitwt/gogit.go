// This file is the go-git fallback gitwt uses when the `git` binary is not on
// PATH (hasGit() false) — a reviewer box that has no git installed at all
// must still be able to provision a checkout and read a PR's diff.
//
// It deliberately does NOT try to reproduce linked worktrees: go-git has no
// supported equivalent of `git worktree add` (it operates on one working tree
// per Repository). Each dispatch's checkout is instead a SEPARATE LOCAL CLONE
// of the base clone (go-git PlainClone with the base clone's own directory as
// the source URL), landed on the same branch name the git path would use, with
// `origin` re-pointed at the real remote afterward so a later push targets
// the actual forge rather than the local base clone. Documented limits:
//
//   - Base clones are always FULL clones — go-git has no `--filter=blob:none`
//     equivalent, so there is no partial-clone speedup in fallback mode.
//   - No rebase support; a fixer step that needs one still requires git (see
//     internal/preflight, which flags this as an error for a fixer config).
//   - Any merge this package might one day need to do fast-forward-only; a
//     three-way merge is not implemented.
package gitwt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	ggssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// ---- base clone -----------------------------------------------------------

// baseCloneGoGit is baseClone's go-git implementation: PlainClone on first
// use (always full — see the package doc), Fetch (pruned) otherwise. Callers
// hold the per-repo lock, exactly as baseClone's git path requires.
func (p *Provisioner) baseCloneGoGit(ctx context.Context, repo, dir string) (string, error) {
	url := p.remoteURL(repo)
	if isGitDir(dir) {
		r, err := gogit.PlainOpen(dir)
		if err != nil {
			return "", fmt.Errorf("gitwt: open base clone (go-git): %w", err)
		}
		auth, err := sshAuthFor(url)
		if err != nil {
			return "", err
		}
		err = r.FetchContext(ctx, &gogit.FetchOptions{RemoteName: "origin", Auth: auth, Prune: true, Force: true})
		if err != nil && err != gogit.NoErrAlreadyUpToDate {
			return "", fmt.Errorf("gitwt: fetch (go-git): %w", err)
		}
		return dir, nil
	}
	if err := os.MkdirAll(p.CheckoutsDir(), 0o700); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir)
	auth, err := sshAuthFor(url)
	if err != nil {
		return "", err
	}
	if _, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{URL: url, Auth: auth}); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("gitwt: clone %s (go-git): %w", url, err)
	}
	return dir, nil
}

// ---- checkout strategies ---------------------------------------------------

// addPRGoGit is addPR's go-git implementation: fetch refs/pull/<n>/head
// straight into a local branch on the base clone, then hand off to
// cloneWorktreeFromBase.
func (p *Provisioner) addPRGoGit(ctx context.Context, base, wt string, req dispatch.Request) error {
	pr := req.Trigger.Target.PR
	if pr <= 0 {
		return fmt.Errorf("checkout-pr with no PR number")
	}
	baseRepo, err := gogit.PlainOpen(base)
	if err != nil {
		return fmt.Errorf("gitwt: open base clone (go-git): %w", err)
	}
	url := p.remoteURL(req.Trigger.Target.CheckoutRepo())
	auth, err := sshAuthFor(url)
	if err != nil {
		return err
	}
	branch := prBranch(req)
	spec := gogitcfg.RefSpec(fmt.Sprintf("+refs/pull/%d/head:%s", pr, plumbing.NewBranchReferenceName(branch)))
	err = baseRepo.FetchContext(ctx, &gogit.FetchOptions{
		RemoteName: "origin", Auth: auth, Force: true,
		RefSpecs: []gogitcfg.RefSpec{spec},
	})
	if err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("gitwt: fetch PR #%d (go-git): %w", pr, err)
	}
	// Resolved from the BASE clone, which has full history, not the worktree
	// clone below: that one is single-branch, so its remote-tracking refs
	// only cover the branch it checked out — refs/remotes/origin/<BaseRef>
	// would not resolve there even though the commit itself is reachable.
	baseHash := diffBaseHash(baseRepo, req.Trigger.Target.BaseRef)
	return p.cloneWorktreeFromBase(ctx, base, wt, branch, req, baseHash)
}

// addBranchGoGit is addBranch's go-git implementation: cut the conductor
// branch on the BASE clone (a plain ref write — it never touches the base
// clone's own checked-out files) at the resolved start point, then hand off
// to cloneWorktreeFromBase.
func (p *Provisioner) addBranchGoGit(ctx context.Context, base, wt string, req dispatch.Request) error {
	baseRepo, err := gogit.PlainOpen(base)
	if err != nil {
		return fmt.Errorf("gitwt: open base clone (go-git): %w", err)
	}
	branch := dispatch.BranchSlug(ctx, req.Trigger)
	start, err := startPointGoGit(baseRepo, req.Trigger.Target.BaseRef)
	if err != nil {
		return fmt.Errorf("gitwt: resolve branch-off start point (go-git): %w", err)
	}
	refName := plumbing.NewBranchReferenceName(branch)
	if err := baseRepo.Storer.SetReference(plumbing.NewHashReference(refName, start)); err != nil {
		return fmt.Errorf("gitwt: create branch %s (go-git): %w", branch, err)
	}
	// The new branch starts exactly at start, so there is nothing to show yet
	// (writePRDiffFallback's baseHash==HEAD check is what makes this a no-op).
	return p.cloneWorktreeFromBase(ctx, base, wt, branch, req, start)
}

// startPointGoGit mirrors startPoint's precedence: the remote-tracking ref
// for the trigger's base, then the plain branch ref, then the remote default
// head, then the base clone's own HEAD.
func startPointGoGit(repo *gogit.Repository, baseRef string) (plumbing.Hash, error) {
	if baseRef != "" {
		for _, name := range []plumbing.ReferenceName{
			plumbing.NewRemoteReferenceName("origin", baseRef),
			plumbing.NewBranchReferenceName(baseRef),
		} {
			if ref, err := repo.Reference(name, true); err == nil {
				return ref.Hash(), nil
			}
		}
	}
	if ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", "HEAD"), true); err == nil {
		return ref.Hash(), nil
	}
	head, err := repo.Head()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return head.Hash(), nil
}

// cloneWorktreeFromBase makes the dispatch's checkout: a fresh local clone of
// base (single-branch, on branch), with origin re-pointed at the repo's real
// remote URL afterward so a later push targets the actual forge. baseHash is
// what writePRDiffFallback diffs HEAD against (the zero hash for "nothing to
// compare"), resolved by the caller from the BASE clone's full refs rather
// than the worktree's own (single-branch) ones.
func (p *Provisioner) cloneWorktreeFromBase(ctx context.Context, base, wt, branch string, req dispatch.Request, baseHash plumbing.Hash) error {
	_, err := gogit.PlainCloneContext(ctx, wt, false, &gogit.CloneOptions{
		URL:           base,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
	})
	if err != nil {
		return fmt.Errorf("gitwt: clone worktree from base (go-git): %w", err)
	}
	if err := p.repointOrigin(wt, req); err != nil {
		return err
	}
	return p.writePRDiffFallback(wt, baseHash)
}

// repointOrigin swaps the freshly cloned worktree's `origin` (which go-git
// set to the base clone's own directory) for the repo's real remote URL, so
// `Push` targets the actual forge rather than the local base clone.
func (p *Provisioner) repointOrigin(wt string, req dispatch.Request) error {
	repo, err := gogit.PlainOpen(wt)
	if err != nil {
		return err
	}
	_ = repo.DeleteRemote("origin")
	realURL := p.remoteURL(req.Trigger.Target.CheckoutRepo())
	_, err = repo.CreateRemote(&gogitcfg.RemoteConfig{Name: "origin", URLs: []string{realURL}})
	return err
}

// ---- reviewer diff (no git at all) ----------------------------------------

// writePRDiffFallback writes the full base...HEAD diff (base = the
// merge-base of baseHash and HEAD) to <wt>/.conductor/pr.diff, and excludes
// .conductor/ from the worktree's own git status — so a reviewer box with no
// git installed can still read what the PR proposes, and gitdiff.Proposed
// (which reads "uncommitted"/"unpushed" state, not this file) never mistakes
// conductor's own bookkeeping for the agent's proposed change. A no-op when
// baseHash is zero (nothing resolved to compare against) or equals HEAD
// (nothing to show — the ordinary branch-off case, which starts exactly at
// its base).
func (p *Provisioner) writePRDiffFallback(wt string, baseHash plumbing.Hash) error {
	if baseHash.IsZero() {
		return nil
	}
	repo, err := gogit.PlainOpen(wt)
	if err != nil {
		return err
	}
	head, err := repo.Head()
	if err != nil {
		return err
	}
	if baseHash == head.Hash() {
		return nil
	}
	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return err
	}
	baseCommit, err := repo.CommitObject(baseHash)
	if err != nil {
		return nil // base commit not present in this (single-branch) clone: skip rather than fail the whole checkout
	}
	bases, err := baseCommit.MergeBase(headCommit)
	if err != nil || len(bases) == 0 {
		return nil
	}
	patch, err := bases[0].Patch(headCommit)
	if err != nil {
		return err
	}
	text := patch.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}

	dir := filepath.Join(wt, ".conductor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "pr.diff"), []byte(text), 0o600); err != nil {
		return err
	}
	return excludeConductorDir(wt)
}

// diffBaseHash resolves the remote tip to diff a PR checkout against: the
// trigger's own base ref on origin, else the remote's default branch
// (origin/HEAD). The zero hash means neither is known.
func diffBaseHash(repo *gogit.Repository, baseRef string) plumbing.Hash {
	if baseRef != "" {
		if ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", baseRef), true); err == nil {
			return ref.Hash()
		}
	}
	if ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", "HEAD"), true); err == nil {
		return ref.Hash()
	}
	return plumbing.ZeroHash
}

// excludeConductorDir adds .conductor/ to the checkout's own
// .git/info/exclude, so it never shows up as an untracked path in the
// worktree's own status (and thus never leaks into a git-path diff either,
// were git later installed on this box).
func excludeConductorDir(wt string) error {
	gitDir := filepath.Join(wt, ".git")
	fi, err := os.Stat(gitDir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return nil // a linked worktree's .git is a file; not our case here
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return err
	}
	excludePath := filepath.Join(infoDir, "exclude")
	existing, _ := os.ReadFile(excludePath)
	if strings.Contains(string(existing), ".conductor/") {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(".conductor/\n")
	return err
}

// ---- push -------------------------------------------------------------

// Push pushes localRef (a branch name, short or full "refs/heads/…") to
// remoteRef on the worktree's origin: through hardened git when it is
// installed, through go-git otherwise. It never force-pushes unless force is
// true — a non-fast-forward push is reported as an error either way, not
// silently forced. Exported for the daemon's brokered-push path (the
// Provisioner is the one thing that knows which transport a given checkout
// actually needs).
func (p *Provisioner) Push(ctx context.Context, worktree, localRef, remoteRef string, force bool) error {
	if hasGit() {
		args := []string{"push"}
		if force {
			args = append(args, "--force")
		}
		args = append(args, "origin", localRef+":"+remoteRef)
		_, err := p.git(ctx, worktree, args...)
		return err
	}
	return p.pushGoGit(ctx, worktree, localRef, remoteRef, force)
}

// pushGoGit is Push's go-git implementation.
func (p *Provisioner) pushGoGit(ctx context.Context, worktree, localRef, remoteRef string, force bool) error {
	repo, err := gogit.PlainOpen(worktree)
	if err != nil {
		return fmt.Errorf("gitwt: push: open worktree (go-git): %w", err)
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("gitwt: push: resolve origin (go-git): %w", err)
	}
	url := ""
	if urls := remote.Config().URLs; len(urls) > 0 {
		url = urls[0]
	}
	auth, err := sshAuthFor(url)
	if err != nil {
		return err
	}
	spec := fullRef(localRef) + ":" + fullRef(remoteRef)
	if force {
		spec = "+" + spec
	}
	err = repo.PushContext(ctx, &gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitcfg.RefSpec{gogitcfg.RefSpec(spec)},
		Auth:       auth,
		Force:      force,
	})
	if err != nil && err != gogit.NoErrAlreadyUpToDate {
		return fmt.Errorf("gitwt: push (go-git): %w", err)
	}
	return nil
}

// fullRef expands a short ref name ("feature") to a full branch ref
// ("refs/heads/feature"); a name that already looks qualified is untouched.
func fullRef(ref string) string {
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

// BaseCloneFor exports the live worktree→base-clone mapping this Provisioner
// tracks in memory: the base clone dir a still-live worktree was created
// from, and whether worktree is one this process provisioned (false after a
// restart — see RemoveWorktree's baseOf fallback for that case).
func (p *Provisioner) BaseCloneFor(worktree string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	base, ok := p.live[filepath.Clean(worktree)]
	return base, ok
}

// ---- ssh auth ---------------------------------------------------------

// sshAuthFor resolves the auth go-git's ssh transport needs for url: nil (no
// auth) for anything that isn't an ssh remote (a local path, file://, git://,
// http(s)://); for an ssh remote (ssh://… or the scp-like git@host:path
// form), the ssh-agent at SSH_AUTH_SOCK when one is running, else the first
// default identity file under ~/.ssh that exists (id_ed25519, id_rsa,
// id_ecdsa — the same order ssh itself tries). A passphrase-protected key
// with no agent running cannot be unlocked here; that surfaces as a push/
// fetch error naming the key, not a silent failure.
func sshAuthFor(url string) (transport.AuthMethod, error) {
	if !isSSHURL(url) {
		return nil, nil
	}
	user := "git"
	if i := strings.Index(url, "@"); i > 0 && strings.Index(url, "://") < 0 {
		user = url[:i]
	} else if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		if j := strings.Index(rest, "@"); j > 0 {
			user = rest[:j]
		}
	}
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		if a, err := ggssh.NewSSHAgentAuth(user); err == nil {
			return a, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("gitwt: no ssh-agent and no home directory to find a default key for %s", url)
	}
	for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		keyPath := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(keyPath); err != nil {
			continue
		}
		if a, err := ggssh.NewPublicKeysFromFile(user, keyPath, ""); err == nil {
			return a, nil
		}
	}
	return nil, fmt.Errorf("gitwt: no ssh auth available for %s (no SSH_AUTH_SOCK agent and no default key under ~/.ssh)", url)
}

// isSSHURL reports whether url needs the ssh transport: an explicit ssh://
// scheme, or the scp-like "[user@]host:path" shorthand (a bare local path or
// another scheme's URL is never mistaken for it — both either start with
// "/"/"." or carry their own "://").
func isSSHURL(url string) bool {
	if strings.HasPrefix(url, "ssh://") {
		return true
	}
	if strings.Contains(url, "://") {
		return false
	}
	idx := strings.Index(url, ":")
	if idx <= 0 {
		return false
	}
	host := url[:idx]
	return !strings.ContainsAny(host, "/\\")
}
