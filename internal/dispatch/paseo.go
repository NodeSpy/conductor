package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// paseo runs an agent action via `paseo run`. Reads use the App token
// (GH_TOKEN); commits/pushes are attributed to you (git author env + SSH); a
// separate write token is exposed for posting as you (see ghwrite.go).
func (d *Dispatcher) paseo(ctx context.Context, req Request) (RunRef, error) {
	data := templateData(req)
	prompt, err := render(req.Action.Prompt, data)
	if err != nil {
		return RunRef{}, fmt.Errorf("render prompt: %w", err)
	}

	argv := []string{"run", prompt,
		"--title", fmt.Sprintf("conductor: %s %s", req.Trigger.Target.Repo, req.Trigger.Kind),
	}
	p := req.Profile
	if p.Provider != "" {
		argv = append(argv, "--provider", p.Provider)
	}
	if p.Model != "" {
		argv = append(argv, "--model", p.Model)
	}
	if p.Thinking != "" {
		argv = append(argv, "--thinking", p.Thinking)
	}
	if p.Mode != "" {
		argv = append(argv, "--mode", p.Mode)
	}
	strat := effectiveStrategy(req)

	// Working directory. An explicit WorkDir always wins. Otherwise a worktree
	// checkout (checkout-pr / branch-off) needs --cwd pointed at a local checkout
	// of the target repo, because paseo derives the forge owner/repo from the
	// working directory — not from a flag. Without it, paseo resolves the wrong
	// repo and fails with WORKSPACE_CREATE_FAILED.
	cwd := ""        // --cwd: a base checkout paseo derives the forge repo from
	worktreeWS := "" // pre-created isolated worktree workspace id (pinned via --workspace)
	if req.Action.WorkDir != "" {
		wd, err := render(req.Action.WorkDir, data)
		if err != nil {
			return RunRef{}, err
		}
		cwd = expandTilde(wd)
	} else if strat == "checkout-pr" || strat == "branch-off" {
		// The default resolver may clone (a side effect) and needs a live daemon,
		// so skip it during a preview. An injected resolver is pure — always use it.
		if d.CheckoutDir != nil || (!d.DryRun && !req.Shadow) {
			// Checkout resolution uses the (possibly remapped) paseo project, so an
			// existing workspace is reused; forge ops still use the real Repo.
			proj := req.Trigger.Target.CheckoutRepo()
			dir, err := d.resolveCheckoutDir(ctx, proj)
			if err != nil {
				return RunRef{}, fmt.Errorf("resolve checkout dir for %s: %w", proj, err)
			}
			// Create the isolated worktree up front and pin the agent into it. This
			// avoids `paseo run --new-workspace worktree`, which can silently fall the
			// agent back to $HOME with no PR checked out (exit 0, no worktree) on a
			// transient hiccup — the failure mode that stranded interactive review
			// hand-offs in the scratch workspace. `workspace create` creates-or-errors,
			// so a real failure escalates + retries instead of parking a checkout-less
			// agent. In a preview (dry/shadow) we can't touch the daemon, so keep the
			// old inline `--cwd` + `--new-workspace` argv shape for assertion.
			if d.WorktreeCreator != nil || (!d.DryRun && !req.Shadow) {
				id, _, err := d.createWorktree(ctx, req, dir)
				if err != nil {
					return RunRef{}, fmt.Errorf("create worktree for %s: %w", proj, err)
				}
				worktreeWS = id
			} else {
				cwd = dir
			}
		}
	}
	switch {
	case worktreeWS != "":
		argv = append(argv, "--workspace", worktreeWS)
	case cwd != "":
		argv = append(argv, "--cwd", cwd)
	}

	// Existing-workspace mode only applies when NOT creating a worktree: paseo
	// forbids --workspace together with --new-workspace. For checkout:none we pin a
	// workspace — an explicit one if configured, else the shared scratch workspace —
	// otherwise paseo spins up a throwaway workspace per run that never gets
	// reclaimed. The shared scratch is for AUTO, NON-INTERACTIVE triage (e.g. the
	// assess step); an interactive hand-off is never pinned to it — it gets its own
	// dedicated workspace (and the reaper's hold-set keeps that from being culled).
	if strat == "none" {
		switch {
		case req.Workspace != "":
			argv = append(argv, "--workspace", req.Workspace)
		case req.Interactive:
			// no shared-scratch pin — paseo gives this hand-off its own workspace
		case cwd == "" && (d.ScratchWorkspace != nil || (!d.DryRun && !req.Shadow)):
			// The built-in resolver may create a workspace and needs a live daemon,
			// so skip it during a preview; an injected resolver is pure.
			if id, err := d.resolveScratchWorkspace(ctx); err == nil && id != "" {
				argv = append(argv, "--workspace", id)
			}
		}
	}
	// When we pre-created the worktree, the agent is pinned into it via --workspace
	// above; adding --new-workspace/--worktree-mode too would conflict. Only emit the
	// inline worktree flags on the preview path (no pre-create).
	if worktreeWS == "" {
		argv = append(argv, checkoutArgs(req)...)
	}

	// Identity: the agent acts as YOU. GH_TOKEN is your write token, so every
	// GitHub write (comment/review/API) is attributed to you — never the App bot
	// (commits/pushes already go over SSH as you). The App token is exposed only as
	// PC_GH_APP_TOKEN for optional rate-limited reads; PC_GH_WRITE_TOKEN is kept as
	// an alias of your token for backward compatibility.
	argv = append(argv,
		"--env", "GH_TOKEN="+req.Tokens.User,
		"--env", "GITHUB_TOKEN="+req.Tokens.User,
		"--env", envGHWriteToken+"="+req.Tokens.User,
		"--env", envGHAppToken+"="+req.Tokens.App,
	)
	if req.Author.Name != "" {
		argv = append(argv,
			"--env", "GIT_AUTHOR_NAME="+req.Author.Name,
			"--env", "GIT_COMMITTER_NAME="+req.Author.Name)
	}
	if req.Author.Email != "" {
		argv = append(argv,
			"--env", "GIT_AUTHOR_EMAIL="+req.Author.Email,
			"--env", "GIT_COMMITTER_EMAIL="+req.Author.Email)
	}
	for k, v := range req.Action.Env {
		rv, err := render(v, data)
		if err != nil {
			return RunRef{}, err
		}
		argv = append(argv, "--env", k+"="+rv)
	}

	for _, l := range labelArgs(req) {
		argv = append(argv, "--label", l)
	}
	if p.WaitTimeout > 0 {
		argv = append(argv, "--wait-timeout", p.WaitTimeout.String())
	}
	if req.Wait {
		// Workflow step: run foreground so we can capture structured output.
		if len(req.Action.OutputSchema) > 0 {
			if b, err := json.Marshal(req.Action.OutputSchema); err == nil {
				argv = append(argv, "--output-schema", string(b))
			}
		}
		argv = append(argv, "--json")
	} else {
		argv = append(argv, "--background", "--json")
	}

	ref := RunRef{Backend: "paseo", Kind: req.Trigger.Kind, Argv: append([]string{d.PaseoBin}, argv...)}

	if d.DryRun || req.Shadow {
		ref.Shadowed = true
		return ref, nil
	}

	// One worker per PR (autonomous feedback only): if an agent is already working
	// this PR, hand the new work to it (`paseo send`) so it drains a burst of
	// feedback instead of spawning a duplicate. A sweep re-derivation (CatchUp) is
	// skipped — the live agent is already on it; don't re-nudge.
	//
	// Excludes interactive hand-offs: a background workflow hand-off (review) has
	// already been given its own dedicated worktree above and MUST launch a fresh
	// agent there — never queued onto an existing agent. In particular the same
	// workflow's just-finished `assess` agent (checkout:none, in the scratch
	// workspace) can still be alive when the hand-off dispatches — its async
	// archive races — and queuing to it would run the review in scratch with no PR
	// checked out, orphaning the worktree. The engine's HasLiveAgent gate already
	// dedups workflows, so this routing is redundant for hand-offs anyway.
	if !req.Wait && !req.Interactive {
		if id := d.liveAgentForPR(ctx, req.Trigger.Key()); id != "" {
			if req.CatchUp {
				ref.Skipped = true
				ref.Output = "skipped: agent " + id + " already working this PR"
				return ref, nil
			}
			if err := d.sendToAgent(ctx, id, prompt); err != nil {
				return ref, fmt.Errorf("queue to agent %s: %w", id, err)
			}
			ref.AgentID = id
			ref.Queued = true
			ref.Output = "queued to live agent " + id
			return ref, nil
		}
		// Opt-in: no conductor agent on this PR, but you may have a workspace open on
		// its branch (where you started the work). Route feedback there instead of
		// spawning a duplicate worktree. Adopted agents are yours — never relabeled,
		// never reaped.
		if d.AdoptOpenWorkspaces && isFeedbackKind(req.Trigger.Kind) {
			if id := d.adoptAgentForBranch(ctx, req); !d.remote() && id != "" {
				if req.CatchUp {
					ref.Skipped = true
					ref.Output = "skipped: your open agent " + id + " is on this branch"
					return ref, nil
				}
				if err := d.sendToAgent(ctx, id, prompt); err != nil {
					return ref, fmt.Errorf("queue to open agent %s: %w", id, err)
				}
				ref.AgentID = id
				ref.Queued = true
				ref.Adopted = true
				ref.Output = "adopted your open agent " + id
				return ref, nil
			}
		}
	}

	// Run with bounded retries on transient git-lock/timeout failures — common
	// when a sweep fans out worktree creations onto one shared repo.
	var out []byte
	var detail string
	for attempt := 0; ; attempt++ {
		cmd := d.paseoCmd(ctx, argv...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err = cmd.Output()
		ref.Output = string(out)
		if err == nil {
			ref.AgentID = parseAgentID(out)
			if verr := d.verifyWorktree(ctx, req, &ref); verr != nil {
				return ref, verr
			}
			return ref, nil
		}
		detail = paseoErrDetail(out, stderr.Bytes())
		if attempt >= d.RetryMax || !isTransientPaseoErr(detail) {
			break
		}
		// A timed-out git op can strand a config.lock that poisons every later
		// creation; clear a clearly-stale one before retrying.
		if !d.remote() { // the lock file lives on the remote box; leave it to paseo
			clearStaleGitLock(ctx, d.PaseoBin, cwd)
		}
		select {
		case <-ctx.Done():
			return ref, ctx.Err()
		case <-time.After(d.RetryBackoff):
		}
	}
	if detail != "" {
		return ref, fmt.Errorf("paseo run: %w: %s", err, detail)
	}
	return ref, fmt.Errorf("paseo run: %w", err)
}

// isTransientPaseoErr reports whether a failed paseo run is worth retrying: a git
// lock collision or timeout while creating the worktree, not a real config error.
func isTransientPaseoErr(detail string) bool {
	d := strings.ToLower(detail)
	for _, sig := range []string{
		"could not lock config file",
		"index.lock",
		"file exists",
		"timed out",
		"timeout",
		"resource temporarily unavailable",
	} {
		if strings.Contains(d, sig) {
			return true
		}
	}
	return false
}

// clearStaleGitLock removes a stale <common-git-dir>/config.lock under cwd if it
// is old enough to be abandoned (git config writes are sub-second, so a lock
// older than a minute is a leftover from a killed/timed-out process). Best-effort.
func clearStaleGitLock(ctx context.Context, paseoBin, cwd string) {
	if cwd == "" {
		return
	}
	c := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--git-common-dir")
	outb, err := c.Output()
	if err != nil {
		return
	}
	common := strings.TrimSpace(string(outb))
	if common == "" {
		return
	}
	if !strings.HasPrefix(common, "/") {
		common = cwd + "/" + common
	}
	lock := common + "/config.lock"
	fi, err := os.Stat(lock)
	if err != nil {
		return
	}
	if time.Since(fi.ModTime()) > time.Minute {
		_ = os.Remove(lock)
	}
}

// paseoErrDetail extracts a human-readable reason from a failed `paseo run`.
// With --json paseo prints its error object to stdout ({"error":{code,message}});
// non-JSON diagnostics land on stderr. Prefer whichever carries signal.
func paseoErrDetail(stdout, stderr []byte) string {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(stdout, &e) == nil && e.Error.Message != "" {
		if e.Error.Code != "" {
			return e.Error.Code + ": " + e.Error.Message
		}
		return e.Error.Message
	}
	if s := strings.TrimSpace(string(stderr)); s != "" {
		return truncate(s, 500)
	}
	return truncate(strings.TrimSpace(string(stdout)), 500)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// effectiveStrategy resolves the action's checkout strategy, defaulting from the
// trigger target when unset: a PR → checkout-pr, any repo → branch-off, else none.
func effectiveStrategy(req Request) string {
	// An interactive hand-off must never share the auto scratch workspace. When it
	// has repo context, give it a PR/branch worktree (PR-centric) even if the step
	// was configured checkout:none — the scratch is for non-interactive triage only.
	// With no repo context it still avoids the shared scratch (see the pin below).
	if req.Interactive && (req.Action.Checkout == "" || req.Action.Checkout == "none") {
		return repoStrategy(req)
	}
	if s := req.Action.Checkout; s != "" {
		return s
	}
	return repoStrategy(req)
}

// repoStrategy picks the worktree strategy from the trigger's repo/PR context.
func repoStrategy(req Request) string {
	switch {
	case req.Trigger.Target.PR > 0:
		return "checkout-pr"
	case req.Trigger.Target.Repo != "":
		return "branch-off"
	default:
		return "none" // no repo/PR context (e.g. cron): run in the base workspace
	}
}

// requestedWorktree reports whether the dispatch asked paseo to create an isolated
// worktree (checkout-pr / branch-off), so a fresh PR/branch checkout is expected.
func requestedWorktree(req Request) bool {
	s := effectiveStrategy(req)
	return s == "checkout-pr" || s == "branch-off"
}

// verifyWorktree guards against a silent checkout failure: paseo can return a
// launched agent even when `--worktree-mode checkout-pr` couldn't create the
// worktree (e.g. a flaky-network git fetch), falling the agent back to the base/
// home workspace — so an interactive review hand-off (or a fix agent) ends up with
// no PR checked out. We detect it by the agent's cwd landing in $HOME (the scratch
// fallback; the `Worktree` inspect field is unreliable — null even for real worktree
// agents). On detection we archive the broken agent and return an error, so the
// engine escalates and re-derives the work (sweep/backoff) once the network is
// healthy, rather than silently handing off a checkout-less agent.
//
// Only background dispatches are checked: a foreground workflow step (checkout:none)
// needs no worktree, and inspecting it after it has run to completion is unreliable.
func (d *Dispatcher) verifyWorktree(ctx context.Context, req Request, ref *RunRef) error {
	if req.Wait || ref.AgentID == "" || !requestedWorktree(req) {
		return nil
	}
	if d.remote() {
		// The $HOME-fallback heuristic compares agent cwds against THIS box's
		// home; a remote paseo's paths are the other box's. Trust the CLI.
		return nil
	}
	if !d.agentInHome(ctx, ref.AgentID) {
		return nil // landed in a worktree (cwd isn't the home fallback)
	}
	id := ref.AgentID
	_ = d.paseoCmd(ctx, "archive", id).Run()
	ref.AgentID = ""
	return fmt.Errorf("%s checkout produced no worktree — agent %s fell back to the base workspace (checkout likely failed; archived it)",
		effectiveStrategy(req), id)
}

// agentInHome reports whether an agent's cwd is the home/scratch fallback (i.e. it
// did not get an isolated worktree). Returns false when it can't tell, so a flaky
// inspect never wrongly fails a good dispatch.
func (d *Dispatcher) agentInHome(ctx context.Context, id string) bool {
	out, err := d.paseoCmd(ctx, "inspect", id, "--json").Output()
	if err != nil {
		return false
	}
	var m struct {
		Cwd string `json:"Cwd"`
	}
	if json.Unmarshal(out, &m) != nil || m.Cwd == "" {
		return false
	}
	return isHomeDir(m.Cwd)
}

// checkoutArgs maps an action's checkout strategy to paseo worktree flags.
func checkoutArgs(req Request) []string {
	switch effectiveStrategy(req) {
	case "checkout-pr":
		return []string{"--new-workspace", workspaceMode(req), "--worktree-mode", "checkout-pr",
			"--pr-number", itoa(req.Trigger.Target.PR), "--forge", "github"}
	case "branch-off":
		args := []string{"--new-workspace", workspaceMode(req), "--worktree-mode", "branch-off",
			"--new-branch", branchSlug(req.Trigger)}
		if req.Trigger.Target.BaseRef != "" {
			args = append(args, "--base", req.Trigger.Target.BaseRef)
		}
		return args
	default: // "none"
		return nil
	}
}

func workspaceMode(req Request) string {
	if req.Profile.Workspace != "" {
		return req.Profile.Workspace
	}
	return "worktree"
}

// createWorktree makes an isolated PR/branch worktree workspace via
// `paseo workspace create` and returns its id and cwd. Unlike
// `paseo run --new-workspace worktree`, which can return a launched agent that
// silently fell back to $HOME with no worktree, `workspace create` either
// produces a real worktree or fails — so a genuine failure surfaces as an error
// the engine can escalate + retry, instead of a checkout-less agent stranded in
// the scratch workspace. baseDir is the repo's stable local checkout paseo derives
// the forge repo from.
func (d *Dispatcher) createWorktree(ctx context.Context, req Request, baseDir string) (string, string, error) {
	if d.WorktreeCreator != nil {
		return d.WorktreeCreator(ctx, req, baseDir)
	}
	strat := effectiveStrategy(req)
	argv := []string{"workspace", "create", "--isolation", workspaceMode(req),
		"--path", baseDir, "--mode", strat, "--json"}
	switch strat {
	case "checkout-pr":
		argv = append(argv, "--pr-number", itoa(req.Trigger.Target.PR), "--forge", "github")
	case "branch-off":
		argv = append(argv, "--new-branch", branchSlug(req.Trigger))
		if req.Trigger.Target.BaseRef != "" {
			argv = append(argv, "--base", req.Trigger.Target.BaseRef)
		}
	default:
		return "", "", fmt.Errorf("createWorktree: unexpected strategy %q", strat)
	}
	cmd := d.paseoCmd(ctx, argv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("paseo workspace create (%s): %w%s", strat, err, stderrTail(&stderr))
	}
	var w struct {
		WorkspaceID string `json:"workspaceId"`
		Cwd         string `json:"cwd"`
	}
	if json.Unmarshal(out, &w) != nil || w.WorkspaceID == "" {
		return "", "", fmt.Errorf("paseo workspace create (%s): unparseable output: %s", strat, strings.TrimSpace(string(out)))
	}
	// Belt-and-suspenders: a "successful" create that still landed in the base/home
	// is the very fallback we're guarding against — treat it as a failure.
	if w.Cwd == "" || isHomeDir(w.Cwd) {
		return "", "", fmt.Errorf("paseo workspace create (%s) produced no worktree (cwd=%q)", strat, w.Cwd)
	}
	return w.WorkspaceID, w.Cwd, nil
}

// stderrTail returns a short, prefixed tail of captured stderr for an error
// message, or "" when empty.
func stderrTail(b *bytes.Buffer) string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	if len(s) > 300 {
		s = "…" + s[len(s)-300:]
	}
	return ": " + s
}

// isHomeDir reports whether path is the user's home directory (the scratch/base
// fallback location). Returns false when home can't be determined.
func isHomeDir(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return normCwd(path) == normCwd(home)
}

// resolveCheckoutDir returns a local checkout path for repo (owner/name) that
// paseo can run in so its worktree checkout derives the correct forge repo. It
// reuses an existing paseo workspace for the repo, else clones one. Results are
// memoized per repo. An injected CheckoutDir overrides this (tests).
func (d *Dispatcher) resolveCheckoutDir(ctx context.Context, repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("no repo in trigger; cannot create a worktree checkout")
	}
	if d.CheckoutDir != nil {
		return d.CheckoutDir(ctx, repo)
	}
	// Cache, but validate it still resolves to a git repo — a cached worktree can
	// be archived out from under us (by the reaper or by hand), which would leave
	// paseo with "Create worktree requires a git repository". Evict + re-resolve.
	d.mu.Lock()
	if p, ok := d.repoDirs[repo]; ok {
		d.mu.Unlock()
		// The revalidation is a local git check; a remote dispatcher trusts the
		// memo (a dead remote path surfaces as a paseo error and re-resolves on
		// the retry path).
		if d.remote() || isGitRepo(ctx, p) {
			return p, nil
		}
		d.mu.Lock()
		delete(d.repoDirs, repo)
	}
	d.mu.Unlock()

	dir := d.findWorkspaceDir(ctx, repo)
	if dir == "" {
		// No existing paseo workspace for this repo. Conductor keeps its own base
		// checkout under ~/.conductor/checkouts/<name> (see cloneParentDir) and uses
		// that directory directly — paseo 0.7's `clone` writes the checkout to disk
		// but no longer registers a workspace, so a post-clone `workspace ls` lookup
		// finds nothing.
		target, err := d.cloneTargetDir(repo)
		if err != nil {
			return "", err
		}
		if isGitRepo(ctx, target) {
			dir = target // reuse a prior clone (avoids paseo's "path already exists")
		} else {
			if err := d.cloneRepo(ctx, repo); err != nil {
				return "", err
			}
			if !isGitRepo(ctx, target) {
				return "", fmt.Errorf("cloned %s but %s is not a git checkout", repo, target)
			}
			dir = target
		}
	}
	d.mu.Lock()
	d.repoDirs[repo] = dir
	d.mu.Unlock()
	return dir, nil
}

// findWorkspaceDir returns a stable local checkout dir for repo — the repo's
// primary (main) working tree, derived from any workspace that belongs to it, so
// paseo can create PR/branch worktrees from something that won't be archived out
// from under it. Prefers a local checkout; validates it's a real git repo. "".
func (d *Dispatcher) findWorkspaceDir(ctx context.Context, repo string) string {
	out, err := d.paseoCmd(ctx, "workspace", "ls", "--json").Output()
	if err != nil {
		return ""
	}
	var wl []struct {
		Project   string `json:"project"`
		Cwd       string `json:"cwd"`
		Isolation string `json:"isolation"`
	}
	if json.Unmarshal(out, &wl) != nil {
		return ""
	}
	fallback := ""
	for _, w := range wl {
		// paseo project names are lowercased; match case-insensitively so a repo
		// whose casing differs from the registered project still reuses it.
		if !strings.EqualFold(w.Project, repo) || w.Cwd == "" {
			continue
		}
		// isGitRepo/mainWorkTree are local checks; for a remote paseo the ls
		// output IS the remote truth — use its cwd as reported.
		base := w.Cwd
		if !d.remote() {
			if !isGitRepo(ctx, w.Cwd) {
				continue
			}
			base = mainWorkTree(ctx, w.Cwd) // the stable primary checkout, not a worktree
		}
		if w.Isolation == "local" {
			return base
		}
		if fallback == "" {
			fallback = base
		}
	}
	return fallback
}

// isGitRepo reports whether dir exists and is inside a git working tree.
func isGitRepo(ctx context.Context, dir string) bool {
	if dir == "" {
		return false
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return false
	}
	return exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

// mainWorkTree returns the repo's primary working tree for a path inside it.
// Linked worktrees are ephemeral (the reaper archives them); the main checkout
// is stable. Falls back to dir if it can't be derived or isn't a working tree.
func mainWorkTree(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse",
		"--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return dir
	}
	common := strings.TrimSpace(string(out))
	if strings.HasSuffix(common, "/.git") {
		if main := strings.TrimSuffix(common, "/.git"); isGitRepo(ctx, main) {
			return main
		}
	}
	return dir
}

// cloneRepo clones repo (owner/name) and registers it as a paseo workspace.
// paseo clones into <dir>/<name>; --dir is required by the CLI, so it's passed
// explicitly (a conductor-managed parent under $HOME) rather than relying on a
// server-side default.
func (d *Dispatcher) cloneRepo(ctx context.Context, repo string) error {
	dir, err := d.cloneParentDir()
	if err != nil {
		return fmt.Errorf("paseo clone %s: %w", repo, err)
	}
	// paseo requires --protocol for an owner/repo shorthand (it can't infer one).
	// Use ssh to match our push identity: commits/pushes go over SSH as you, so the
	// base checkout's origin should too.
	proto := d.CloneProtocol
	if proto == "" {
		proto = "ssh"
	}
	if out, err := d.paseoCmd(ctx, "clone", repo, "--dir", dir, "--protocol", proto, "--json").CombinedOutput(); err != nil {
		return fmt.Errorf("paseo clone %s: %w: %s", repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cloneTargetDir is where conductor's base checkout of repo lives: the clone
// parent dir plus the repo's short name (paseo clones owner/repo into <dir>/<name>).
func (d *Dispatcher) cloneTargetDir(repo string) (string, error) {
	parent, err := d.cloneParentDir()
	if err != nil {
		return "", err
	}
	name := filepath.Base(repo)
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("cannot derive checkout dir name from %q", repo)
	}
	return filepath.Join(parent, name), nil
}

// cloneParentDir returns the parent directory conductor clones base checkouts
// into, creating it if needed. Clones are grouped under ~/.conductor/checkouts
// so they don't clutter $HOME; each repo lands in its own <name> subdir.
func (d *Dispatcher) cloneParentDir() (string, error) {
	if d.remote() {
		// A RELATIVE dir: the remote command runs from the ssh login dir (the
		// remote home, or the host's cwd:), so the checkout lands under the
		// remote user's own tree; this box's home would be a foreign path
		// there. paseo creates the directory itself.
		return ".conductor/checkouts", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for clone: %w", err)
	}
	dir := filepath.Join(home, ".conductor", "checkouts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create clone dir %s: %w", dir, err)
	}
	return dir, nil
}

// scratchWorkspaceTitle marks the single shared workspace reused by checkout:none
// agents, so they don't each leak a throwaway home workspace.
const scratchWorkspaceTitle = "conductor-scratch"

// resolveScratchWorkspace returns a reusable local workspace id for checkout:none
// agents: an injected resolver, else a memoized find-by-title, else create one.
func (d *Dispatcher) resolveScratchWorkspace(ctx context.Context) (string, error) {
	if d.ScratchWorkspace != nil {
		return d.ScratchWorkspace(ctx)
	}
	d.mu.Lock()
	defer d.mu.Unlock() // held across resolve so concurrent callers don't each create one
	// Re-resolve by title every time (don't trust a memoized id): the reaper may
	// have archived an idle scratch, so a stale memo would point at a dead
	// workspace. findWorkspaceByTitle returns the current one, or "" → recreate.
	if id := d.findWorkspaceByTitle(ctx, scratchWorkspaceTitle); id != "" {
		d.scratchWS = id
		return id, nil
	}
	id, err := d.createScratchWorkspace(ctx)
	if err != nil {
		return "", err
	}
	d.scratchWS = id
	return id, nil
}

// findWorkspaceByTitle returns the id of a local workspace whose name matches
// title, or "" if none.
func (d *Dispatcher) findWorkspaceByTitle(ctx context.Context, title string) string {
	out, err := d.paseoCmd(ctx, "workspace", "ls", "--json").Output()
	if err != nil {
		return ""
	}
	var wl []struct {
		WorkspaceID string `json:"workspaceId"`
		Name        string `json:"name"`
		Isolation   string `json:"isolation"`
	}
	if json.Unmarshal(out, &wl) != nil {
		return ""
	}
	for _, w := range wl {
		if w.Isolation == "local" && w.Name == title && w.WorkspaceID != "" {
			return w.WorkspaceID
		}
	}
	return ""
}

// createScratchWorkspace makes the shared local scratch workspace at $HOME.
func (d *Dispatcher) createScratchWorkspace(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	if d.remote() {
		home = "." // the remote command's working directory (remote home / host cwd:)
	}
	out, err := d.paseoCmd(ctx, "workspace", "create",
		"--isolation", "local", "--path", home, "--title", scratchWorkspaceTitle, "--json").Output()
	if err != nil {
		return "", fmt.Errorf("paseo workspace create scratch: %w", err)
	}
	var w struct {
		WorkspaceID string `json:"workspaceId"`
		ID          string `json:"id"`
	}
	if json.Unmarshal(out, &w) != nil {
		return "", fmt.Errorf("scratch workspace: unparseable create output")
	}
	if w.WorkspaceID != "" {
		return w.WorkspaceID, nil
	}
	return w.ID, nil
}

func labelArgs(req Request) []string {
	labels := []string{
		"conductor=1",
		"integration=" + req.Trigger.Instance,
		"source=" + req.Trigger.Source,
		"repo=" + req.Trigger.Target.Repo,
		"pr=" + req.Trigger.Key(),
		"kind=" + req.Trigger.Kind,
		"head=" + req.Trigger.Target.HeadSHA,
	}
	if req.Trigger.Variant != "" {
		labels = append(labels, "variant="+req.Trigger.Variant)
	}
	if req.Profile.ArchiveWhenDone {
		labels = append(labels, "archive=1")
	}
	for k, v := range req.Profile.Labels {
		labels = append(labels, k+"="+v)
	}
	for k, v := range req.Trigger.Labels {
		labels = append(labels, k+"="+v)
	}
	return labels
}

func branchSlug(t core.Trigger) string {
	s := fmt.Sprintf("conductor/%s-%d", t.Kind, t.Target.Number)
	return strings.ReplaceAll(s, " ", "-")
}

// HasLiveAgent reports whether any non-archived conductor agent exists for this
// PR+kind — running OR idle-but-open (e.g. an interactive review agent parked for
// you). `paseo ls` excludes archived agents, so any match means one is still in
// play. Used to gate re-dispatch of live-gated kinds (reviews).
func (d *Dispatcher) HasLiveAgent(ctx context.Context, prKey, kind string) bool {
	out, err := d.paseoCmd(ctx, "ls", "--json",
		"--label", "conductor=1", "--label", "pr="+prKey, "--label", "kind="+kind).Output()
	if err != nil {
		return false
	}
	var agents []json.RawMessage
	if json.Unmarshal(out, &agents) != nil {
		return false
	}
	return len(agents) > 0
}

// Archive soft-deletes a finished agent (paseo archive), used to clean up a
// non-interactive workflow step's agent the instant it finishes rather than
// leaving it for the reaper's next poll. A blank id is a no-op.
func (d *Dispatcher) Archive(ctx context.Context, agentID string) error {
	if agentID == "" {
		return nil
	}
	return d.paseoCmd(ctx, "archive", agentID).Run()
}

// liveAgentForPR returns the id of a non-archived conductor agent already working
// this PR (any kind), or "" if none — the "one worker per PR" target for queuing
// new feedback via `paseo send`.
func (d *Dispatcher) liveAgentForPR(ctx context.Context, prKey string) string {
	out, err := d.paseoCmd(ctx, "ls", "--json",
		"--label", "conductor=1", "--label", "pr="+prKey).Output()
	if err != nil {
		return ""
	}
	var agents []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out, &agents) != nil {
		return ""
	}
	for _, a := range agents {
		if a.ID != "" {
			return a.ID
		}
	}
	return ""
}

// isFeedbackKind reports whether a kind is PR feedback eligible for open-workspace
// adoption (mirrors the github integration's feedbackKind).
func isFeedbackKind(k string) bool { return k == "new_comment" || k == "changes_requested" }

// agentInfo is the subset of `paseo ls --json` adoption needs.
type agentInfo struct {
	ID     string `json:"id"`
	Cwd    string `json:"cwd"`
	Status string `json:"status"`
}

// adoptCand is a candidate open agent whose checkout is on the PR's head branch.
type adoptCand struct {
	id     string
	active string // RFC3339 last-active timestamp (lexically sortable); "" if unknown
}

// adoptAgentForBranch finds an agent whose checkout is already on this PR's head
// branch — e.g. a workspace you opened by hand — so feedback is routed to it instead
// of spawning a duplicate worktree. Returns the most-recently-active match, or "".
func (d *Dispatcher) adoptAgentForBranch(ctx context.Context, req Request) string {
	headRef, _ := req.Trigger.Context["head_ref"].(string)
	if headRef == "" {
		return "" // no branch to match on (dispatch stays repo-agnostic; the github side supplies it)
	}
	repo := req.Trigger.Target.Repo
	var cands []adoptCand
	for _, a := range d.listAgents(ctx) {
		dir := expandTilde(a.Cwd)
		if a.ID == "" || dir == "" || !isGitRepo(ctx, dir) {
			continue
		}
		if d.gitBranch(ctx, dir) != headRef || !gitRepoMatches(ctx, dir, repo) {
			continue
		}
		cands = append(cands, adoptCand{id: a.ID, active: d.agentLastActive(ctx, a.ID)})
	}
	return pickAdoptTarget(cands)
}

// pickAdoptTarget returns the id of the most-recently-active candidate (RFC3339
// timestamps sort lexically), or "" if there are none. Ties keep the first seen.
func pickAdoptTarget(cands []adoptCand) string {
	best := ""
	bestActive := ""
	for _, c := range cands {
		if best == "" || c.active > bestActive {
			best, bestActive = c.id, c.active
		}
	}
	return best
}

// listAgents lists non-archived agents via `paseo ls --json`.
func (d *Dispatcher) listAgents(ctx context.Context) []agentInfo {
	out, err := d.paseoCmd(ctx, "ls", "--json").Output()
	if err != nil {
		return nil
	}
	var agents []agentInfo
	if json.Unmarshal(out, &agents) != nil {
		return nil
	}
	return agents
}

// gitBranch returns the current branch of a checkout (empty on detached/err).
func (d *Dispatcher) gitBranch(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitRepoMatches reports whether dir's origin remote points at repo (owner/name),
// so a head branch that collides across repos can't cause a wrong adoption.
func gitRepoMatches(ctx context.Context, dir, repo string) bool {
	if repo == "" {
		return false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(repo))
}

// agentLastActive returns an agent's last-active timestamp (LastUsage, else
// UpdatedAt, else CreatedAt) via `paseo inspect --json`, for recency ranking.
func (d *Dispatcher) agentLastActive(ctx context.Context, id string) string {
	out, err := d.paseoCmd(ctx, "inspect", "--json", id).Output()
	if err != nil {
		return ""
	}
	var m struct {
		LastUsage string `json:"LastUsage"`
		UpdatedAt string `json:"UpdatedAt"`
		CreatedAt string `json:"CreatedAt"`
	}
	if json.Unmarshal(out, &m) != nil {
		return ""
	}
	switch {
	case m.LastUsage != "":
		return m.LastUsage
	case m.UpdatedAt != "":
		return m.UpdatedAt
	default:
		return m.CreatedAt
	}
}

// sendToAgent queues a follow-up task to an existing agent.
func (d *Dispatcher) sendToAgent(ctx context.Context, id, prompt string) error {
	cmd := d.paseoCmd(ctx, "send", id, prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return fmt.Errorf("%w: %s", err, truncate(s, 300))
		}
		return err
	}
	return nil
}

// parseAgentID best-effort extracts an agent id from `paseo run --json` output.
func parseAgentID(out []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		return ""
	}
	for _, k := range []string{"id", "agentId", "agent_id", "agentID"} {
		if v, ok := obj[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
