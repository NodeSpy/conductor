// Package gitwt provisions an agent's checkout as a plain `git` worktree under
// conductor's own state dir, and tears it down when the session that owns it
// closes.
//
// It exists so the **cli** runtime stops going through paseo for a checkout
// (docs/design/cli-git-worktrees.md): a `workspace: worktree` dispatch used to
// call `paseo workspace create` no matter which runtime ran the agent, so a
// claude-code fixer made a paseo workspace it never reused and never removed.
// Here the same two invariants hold — the agent runs in the worktree conductor
// provisions, and git runs as the daemon user with your ssh identity — with
// nothing but the system `git` involved (the same shelling pattern as
// internal/gitdiff).
//
// Layout under <state>:
//
//	<state>/checkouts/<owner>__<repo>   one base clone per repo (blob-filtered)
//	<state>/worktrees/<dispatch-id>     one worktree per dispatch
//
// The type satisfies controller.Provisioner. Removal and the orphan reaper only
// ever touch direct children of <state>/worktrees — never any other path.
package gitwt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

const (
	// DefaultMinAge is how long an unreferenced worktree dir must have sat
	// untouched before the reaper removes it. Generous on purpose: the live
	// set is the primary protection, and age is only the crash-safety net
	// (a daemon killed mid-dispatch leaves a dir no live session claims).
	DefaultMinAge = time.Hour
	// DefaultInterval is the orphan reaper's tick.
	DefaultInterval = 15 * time.Minute
	// removeTimeout bounds the git calls a teardown makes, so a session Close
	// can never block on a wedged git.
	removeTimeout = 30 * time.Second
)

// Provisioner creates and removes git worktrees for dispatches. The zero value
// is not usable — construct it with New.
type Provisioner struct {
	root string

	// RemoteURL maps "owner/repo" to the URL the base clone is made from.
	// nil → DefaultRemoteURL (ssh, matching the identity commits push under).
	RemoteURL func(repo string) string
	// Log receives reaper/teardown notes. nil → silent.
	Log func(format string, args ...any)
	// MinAge / Interval tune the orphan reaper (0 → the Default* above).
	MinAge   time.Duration
	Interval time.Duration
	// Now is the reaper's clock, injectable for tests. nil → time.Now.
	Now func() time.Time

	mu     sync.Mutex
	repoMu map[string]*sync.Mutex // per-repo base-clone fetch lock
	live   map[string]string      // worktree path → its base clone dir
}

// New builds a Provisioner rooted at dir (the conductor state dir; "" resolves
// to config.StateDir(), the same base the store uses).
func New(dir string) *Provisioner {
	if dir == "" {
		dir = config.StateDir()
	}
	return &Provisioner{
		root:   dir,
		repoMu: map[string]*sync.Mutex{},
		live:   map[string]string{},
	}
}

// CheckoutsDir is where the per-repo base clones live.
func (p *Provisioner) CheckoutsDir() string { return filepath.Join(p.root, "checkouts") }

// WorktreesDir is where the per-dispatch worktrees live. Nothing outside it is
// ever removed.
func (p *Provisioner) WorktreesDir() string { return filepath.Join(p.root, "worktrees") }

// ProvisionWorktree resolves the checkout a controller runs an agent in, by the
// dispatch's effective checkout strategy:
//
//	checkout-pr   fetch refs/pull/<n>/head, add a worktree on the PR's branch
//	branch-off    add a worktree on a fresh conductor/<kind>-<n> branch
//	none          no worktree at all ("", "", nil) — the read-only judges
//
// An explicit action work_dir wins over all of it (and is never ours to remove).
// Returns (worktree, worktree, nil): the id a session hands back to
// RemoveWorktree IS the path. Every git failure is wrapped Unrecoverable so the
// engine escalates and retries exactly like the paseo path.
func (p *Provisioner) ProvisionWorktree(ctx context.Context, req dispatch.Request) (string, string, error) {
	wd, err := dispatch.WorkDir(req)
	if err != nil {
		return "", "", dispatch.Unrecoverable(fmt.Errorf("gitwt: render work_dir: %w", err))
	}
	if wd != "" {
		// An operator-named directory: conductor did not create it, so it has
		// no worktree id and is never torn down.
		return "", wd, nil
	}

	strategy := dispatch.EffectiveStrategy(req)
	switch strategy {
	case "checkout-pr", "branch-off":
	default:
		// No repo/PR context (a cron trigger, a read-only judge): the runtime
		// runs in its own default directory, exactly as on the paseo path.
		return "", "", nil
	}

	repo := req.Trigger.Target.CheckoutRepo()
	if repo == "" {
		return "", "", dispatch.Unrecoverable(fmt.Errorf("gitwt: no repo in trigger; cannot create a %s worktree", strategy))
	}
	base, err := p.baseClone(ctx, repo)
	if err != nil {
		return "", "", dispatch.Unrecoverable(fmt.Errorf("gitwt: base checkout for %s: %w", repo, err))
	}
	wt, err := p.newWorktreePath(req)
	if err != nil {
		return "", "", dispatch.Unrecoverable(err)
	}

	switch strategy {
	case "checkout-pr":
		err = p.addPR(ctx, base, wt, req)
	case "branch-off":
		err = p.addBranch(ctx, base, wt, req)
	}
	if err != nil {
		// Leave nothing half-made behind for the reaper to puzzle over.
		_ = os.RemoveAll(wt)
		_, _ = p.git(ctx, base, "worktree", "prune")
		return "", "", dispatch.Unrecoverable(fmt.Errorf("gitwt: %s worktree for %s: %w", strategy, repo, err))
	}

	p.mu.Lock()
	p.live[wt] = base
	p.mu.Unlock()
	return wt, wt, nil
}

// RemoveWorktree tears down a worktree this provisioner created: `git worktree
// remove --force` then `git worktree prune`, and the directory itself as the
// backstop. Idempotent — an already-gone worktree is not an error — and inert
// for any id that is not a direct child of <state>/worktrees (e.g. a paseo
// workspace id from the fallback path), so it can never delete a checkout it
// does not own.
func (p *Provisioner) RemoveWorktree(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	wt := filepath.Clean(id)
	if !p.owns(wt) {
		return nil
	}
	p.mu.Lock()
	base := p.live[wt]
	delete(p.live, wt)
	p.mu.Unlock()
	if base == "" {
		// A worktree from a previous daemon life: recover its base clone from
		// the worktree's own .git pointer file.
		base = baseOf(wt)
	}

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	if base != "" {
		// Best-effort: `worktree remove` fails when the dir is already gone,
		// or when git considers the tree dirty. The RemoveAll + prune below is
		// what makes this idempotent either way.
		_, _ = p.git(rctx, base, "worktree", "remove", "--force", wt)
	}
	if err := os.RemoveAll(wt); err != nil {
		return fmt.Errorf("gitwt: remove worktree %s: %w", wt, err)
	}
	if base != "" {
		_, _ = p.git(rctx, base, "worktree", "prune")
	}
	return nil
}

// Run reaps orphans once at startup and on every tick until ctx ends. Startup
// matters most: a killed daemon leaves worktrees no session will ever close.
func (p *Provisioner) Run(ctx context.Context) {
	p.Reap(ctx)
	t := time.NewTicker(p.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Reap(ctx)
		}
	}
}

// Reap prunes stale worktree bookkeeping from every base clone and removes any
// <state>/worktrees child that no live session claims and that has sat
// untouched for MinAge. Deliberately conservative: a live worktree is skipped
// whatever its age, and nothing outside <state>/worktrees is ever considered.
func (p *Provisioner) Reap(ctx context.Context) {
	if ents, err := os.ReadDir(p.CheckoutsDir()); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				_, _ = p.git(ctx, filepath.Join(p.CheckoutsDir(), e.Name()), "worktree", "prune")
			}
		}
	}
	ents, err := os.ReadDir(p.WorktreesDir())
	if err != nil {
		return
	}
	cutoff := p.now().Add(-p.minAge())
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(p.WorktreesDir(), e.Name())
		p.mu.Lock()
		_, live := p.live[path]
		p.mu.Unlock()
		if live {
			continue // a session is working in it right now
		}
		fi, err := e.Info()
		if err != nil || fi.ModTime().After(cutoff) {
			continue // too fresh to be sure it is an orphan
		}
		if err := p.RemoveWorktree(ctx, path); err != nil {
			p.logf("gitwt: reap %s: %v", path, err)
			continue
		}
		p.logf("gitwt: reaped orphan worktree %s", path)
	}
}

// ---- checkout strategies -------------------------------------------------

// addPR lands the PR head in a fresh worktree. The branch is named after the
// PR's own head ref (falling back to pr-<n>) rather than left detached, so an
// agent's `git push` targets the PR branch the way it does on the paseo path.
func (p *Provisioner) addPR(ctx context.Context, base, wt string, req dispatch.Request) error {
	pr := req.Trigger.Target.PR
	if pr <= 0 {
		return fmt.Errorf("checkout-pr with no PR number")
	}
	if _, err := p.git(ctx, base, "fetch", "--no-tags", "--force", "origin",
		"refs/pull/"+strconv.Itoa(pr)+"/head"); err != nil {
		return err
	}
	branch := prBranch(req)
	_, err := p.git(ctx, base, "worktree", "add", "-B", branch, wt, "FETCH_HEAD")
	if err == nil {
		return nil
	}
	// The branch is already checked out in another live worktree (a second
	// dispatch on the same PR). Take the head detached rather than force the
	// other checkout's branch pointer out from under it.
	_ = os.RemoveAll(wt)
	if _, derr := p.git(ctx, base, "worktree", "add", "--detach", wt, "FETCH_HEAD"); derr != nil {
		return errors.Join(err, derr)
	}
	return nil
}

// addBranch cuts a fresh conductor branch off the trigger's base ref.
func (p *Provisioner) addBranch(ctx context.Context, base, wt string, req dispatch.Request) error {
	branch := dispatch.BranchSlug(ctx, req.Trigger)
	args := []string{"worktree", "add", "-B", branch, wt}
	if start := p.startPoint(ctx, base, req.Trigger.Target.BaseRef); start != "" {
		args = append(args, start)
	}
	_, err := p.git(ctx, base, args...)
	return err
}

// startPoint resolves what a branch-off branches FROM: the remote-tracking ref
// for the trigger's base (freshly fetched), then the plain ref, then the
// remote's default head. "" lets git use the base clone's own HEAD.
func (p *Provisioner) startPoint(ctx context.Context, base, ref string) string {
	var cands []string
	if ref != "" {
		cands = append(cands, "origin/"+ref, ref)
	}
	cands = append(cands, "origin/HEAD")
	for _, c := range cands {
		if _, err := p.git(ctx, base, "rev-parse", "--verify", "--quiet", c+"^{commit}"); err == nil {
			return c
		}
	}
	return ""
}

// ---- base clones ---------------------------------------------------------

// baseClone returns the repo's base clone, cloning it on first use and fetching
// it otherwise. Concurrent dispatches on one repo serialize here (a per-repo
// mutex), so two fixers on the same repo never race a clone or a fetch.
func (p *Provisioner) baseClone(ctx context.Context, repo string) (string, error) {
	dir := filepath.Join(p.CheckoutsDir(), repoSlug(repo))
	mu := p.repoLock(repo)
	mu.Lock()
	defer mu.Unlock()

	if isGitDir(dir) {
		if _, err := p.git(ctx, dir, "fetch", "--prune", "origin"); err != nil {
			return "", fmt.Errorf("fetch: %w", err)
		}
		return dir, nil
	}
	if err := os.MkdirAll(p.CheckoutsDir(), 0o700); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir) // a previous clone that died half-written
	url := p.remoteURL(repo)
	if _, err := p.git(ctx, "", "clone", "--filter=blob:none", url, dir); err != nil {
		// A server with uploadpack.allowFilter off rejects a partial clone
		// outright; a full clone is slower but always works.
		_ = os.RemoveAll(dir)
		if _, ferr := p.git(ctx, "", "clone", url, dir); ferr != nil {
			return "", errors.Join(fmt.Errorf("clone %s: %w", url, err), ferr)
		}
	}
	return dir, nil
}

func (p *Provisioner) repoLock(repo string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	mu, ok := p.repoMu[repo]
	if !ok {
		mu = &sync.Mutex{}
		p.repoMu[repo] = mu
	}
	return mu
}

// DefaultRemoteURL is the clone URL for owner/repo: ssh, so the base checkout's
// origin matches the identity commits are pushed under. CONDUCTOR_GIT_REMOTE_BASE
// overrides the base (e.g. "git://forge/" for a hermetic harness).
func DefaultRemoteURL(repo string) string {
	if base := os.Getenv("CONDUCTOR_GIT_REMOTE_BASE"); base != "" {
		if !strings.HasSuffix(base, "/") && !strings.HasSuffix(base, ":") {
			base += "/"
		}
		return base + repo + ".git"
	}
	return "git@github.com:" + repo + ".git"
}

func (p *Provisioner) remoteURL(repo string) string {
	if p.RemoteURL != nil {
		return p.RemoteURL(repo)
	}
	return DefaultRemoteURL(repo)
}

// ---- paths ---------------------------------------------------------------

// newWorktreePath reserves the directory a dispatch's worktree will be created
// at: <state>/worktrees/<dispatch id>, uniquified when that name is taken (a
// re-dispatch of the same id, or no id at all).
func (p *Provisioner) newWorktreePath(req dispatch.Request) (string, error) {
	if err := os.MkdirAll(p.WorktreesDir(), 0o700); err != nil {
		return "", fmt.Errorf("gitwt: worktrees dir: %w", err)
	}
	slug := pathSlug(req.DispatchID)
	if slug == "" {
		slug = "wt"
	}
	path := filepath.Join(p.WorktreesDir(), slug)
	if _, err := os.Lstat(path); err != nil {
		return path, nil
	}
	// Runtime state, not a replayable workflow: a clock-derived suffix is fine.
	for i := 0; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d", path, time.Now().UnixNano()%1e9+int64(i))
		if _, err := os.Lstat(cand); err != nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("gitwt: no free worktree path under %s", p.WorktreesDir())
}

// owns reports whether path is a direct child of <state>/worktrees — the ONLY
// thing removal and the reaper may touch.
func (p *Provisioner) owns(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	return filepath.Dir(path) == filepath.Clean(p.WorktreesDir()) &&
		filepath.Base(path) != "." && filepath.Base(path) != ".."
}

// baseOf recovers a worktree's base clone from its .git pointer file
// ("gitdir: <base>/.git/worktrees/<name>"), so a worktree left by a previous
// daemon can still be removed through git rather than just unlinked.
func baseOf(wt string) string {
	b, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	const marker = "/.git/worktrees/"
	if i := strings.Index(gitdir, marker); i > 0 {
		return gitdir[:i]
	}
	return ""
}

// repoSlug flattens "owner/repo" into one directory name.
func repoSlug(repo string) string {
	return pathSlug(strings.ReplaceAll(strings.Trim(repo, "/"), "/", "__"))
}

// pathSlug reduces s to a safe single path segment: alphanumerics, dot, dash
// and underscore, with everything else folded to '-'. "" for a name that would
// escape its directory.
func pathSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 96 {
		out = out[:96]
	}
	if out == "." || out == ".." || strings.HasPrefix(out, ".") {
		return ""
	}
	return out
}

// prBranch is the local branch a checkout-pr worktree lands on: the PR's own
// head ref when the trigger carried it (so `git push origin HEAD` targets the
// PR branch), else a deterministic pr-<n>.
func prBranch(req dispatch.Request) string {
	if ref, _ := req.Trigger.Context["head_ref"].(string); safeBranch(ref) {
		return ref
	}
	return "pr-" + strconv.Itoa(req.Trigger.Target.PR)
}

// safeBranch reports whether ref is usable verbatim as a local branch name —
// it comes off event data, so it is validated, never trusted.
func safeBranch(ref string) bool {
	if ref == "" || len(ref) > 200 || strings.ContainsAny(ref, " \t\\?*[]^:~") ||
		strings.Contains(ref, "..") || strings.HasPrefix(ref, "-") ||
		strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") ||
		strings.HasSuffix(ref, ".lock") {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ---- git -----------------------------------------------------------------

// git runs one git command (in dir, or wherever when dir is "") and returns its
// stdout. Terminal prompting is disabled so a missing credential fails fast
// instead of hanging the daemon on a password prompt.
func (p *Provisioner) git(ctx context.Context, dir string, args ...string) (string, error) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err,
			truncate(strings.TrimSpace(errb.String()), 400))
	}
	return out.String(), nil
}

func isGitDir(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (p *Provisioner) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

func (p *Provisioner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Provisioner) minAge() time.Duration {
	if p.MinAge > 0 {
		return p.MinAge
	}
	return DefaultMinAge
}

func (p *Provisioner) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultInterval
}
