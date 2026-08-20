package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	cwd := ""
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
			dir, err := d.resolveCheckoutDir(ctx, req.Trigger.Target.Repo)
			if err != nil {
				return RunRef{}, fmt.Errorf("resolve checkout dir for %s: %w", req.Trigger.Target.Repo, err)
			}
			cwd = dir
		}
	}
	if cwd != "" {
		argv = append(argv, "--cwd", cwd)
	}

	// Existing-workspace mode only applies when NOT creating a worktree: paseo
	// forbids --workspace together with --new-workspace. For checkout:none we
	// always pin a workspace — an explicit one if configured, else a shared
	// scratch workspace — otherwise paseo spins up a throwaway workspace per run
	// (e.g. review triage) that never gets reclaimed.
	if strat == "none" {
		if req.Workspace != "" {
			argv = append(argv, "--workspace", req.Workspace)
		} else if cwd == "" && (d.ScratchWorkspace != nil || (!d.DryRun && !req.Shadow)) {
			// The built-in resolver may create a workspace and needs a live daemon,
			// so skip it during a preview; an injected resolver is pure.
			if id, err := d.resolveScratchWorkspace(ctx); err == nil && id != "" {
				argv = append(argv, "--workspace", id)
			}
		}
	}
	argv = append(argv, checkoutArgs(req)...)

	// Reads on the App token; write token exposed for posting as you.
	argv = append(argv,
		"--env", "GH_TOKEN="+req.Tokens.App,
		"--env", "GITHUB_TOKEN="+req.Tokens.App,
		"--env", envGHWriteToken+"="+req.Tokens.User,
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

	// Running-agent guard (background autopilot only): skip if an agent for this
	// PR+kind is already active. Workflow steps run to completion in order.
	if !req.Wait {
		if active, _ := d.agentActive(ctx, req.Trigger); active {
			ref.Output = "skipped: agent already running for this pr+kind"
			return ref, nil
		}
	}

	// Run with bounded retries on transient git-lock/timeout failures — common
	// when a sweep fans out worktree creations onto one shared repo.
	var out []byte
	var detail string
	for attempt := 0; ; attempt++ {
		cmd := exec.CommandContext(ctx, d.PaseoBin, argv...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err = cmd.Output()
		ref.Output = string(out)
		if err == nil {
			ref.AgentID = parseAgentID(out)
			return ref, nil
		}
		detail = paseoErrDetail(out, stderr.Bytes())
		if attempt >= d.RetryMax || !isTransientPaseoErr(detail) {
			break
		}
		// A timed-out git op can strand a config.lock that poisons every later
		// creation; clear a clearly-stale one before retrying.
		clearStaleGitLock(ctx, d.PaseoBin, cwd)
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
	if s := req.Action.Checkout; s != "" {
		return s
	}
	switch {
	case req.Trigger.Target.PR > 0:
		return "checkout-pr"
	case req.Trigger.Target.Repo != "":
		return "branch-off"
	default:
		return "none" // no repo/PR context (e.g. cron): run in the base workspace
	}
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
		if isGitRepo(ctx, p) {
			return p, nil
		}
		d.mu.Lock()
		delete(d.repoDirs, repo)
	}
	d.mu.Unlock()

	dir := d.findWorkspaceDir(ctx, repo)
	if dir == "" {
		// No existing checkout for this repo — clone a base checkout once.
		if err := d.cloneRepo(ctx, repo); err != nil {
			return "", err
		}
		if dir = d.findWorkspaceDir(ctx, repo); dir == "" {
			return "", fmt.Errorf("cloned %s but could not locate its workspace", repo)
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
	out, err := exec.CommandContext(ctx, d.PaseoBin, "workspace", "ls", "--json").Output()
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
		if w.Project != repo || w.Cwd == "" || !isGitRepo(ctx, w.Cwd) {
			continue
		}
		base := mainWorkTree(ctx, w.Cwd) // the stable primary checkout, not a worktree
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
func (d *Dispatcher) cloneRepo(ctx context.Context, repo string) error {
	if out, err := exec.CommandContext(ctx, d.PaseoBin, "clone", repo, "--json").CombinedOutput(); err != nil {
		return fmt.Errorf("paseo clone %s: %w: %s", repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// scratchWorkspaceTitle marks the single shared workspace reused by checkout:none
// agents, so they don't each leak a throwaway home workspace.
const scratchWorkspaceTitle = "paseo-conductor-scratch"

// resolveScratchWorkspace returns a reusable local workspace id for checkout:none
// agents: an injected resolver, else a memoized find-by-title, else create one.
func (d *Dispatcher) resolveScratchWorkspace(ctx context.Context) (string, error) {
	if d.ScratchWorkspace != nil {
		return d.ScratchWorkspace(ctx)
	}
	d.mu.Lock()
	defer d.mu.Unlock() // held across resolve so concurrent callers don't each create one
	if d.scratchWS != "" {
		return d.scratchWS, nil
	}
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
	out, err := exec.CommandContext(ctx, d.PaseoBin, "workspace", "ls", "--json").Output()
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
	out, err := exec.CommandContext(ctx, d.PaseoBin, "workspace", "create",
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

// agentActive reports whether a conductor agent for this PR+kind is running.
func (d *Dispatcher) agentActive(ctx context.Context, t core.Trigger) (bool, error) {
	out, err := exec.CommandContext(ctx, d.PaseoBin, "ls", "--json",
		"--label", "conductor=1", "--label", "pr="+t.Key(), "--label", "kind="+t.Kind).Output()
	if err != nil {
		return false, err
	}
	var agents []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &agents); err != nil {
		return false, nil
	}
	for _, a := range agents {
		switch strings.ToLower(a.Status) {
		case "idle", "archived", "completed", "done", "":
		default:
			return true, nil
		}
	}
	return false, nil
}

// HasLiveAgent reports whether any non-archived conductor agent exists for this
// PR+kind — running OR idle-but-open (e.g. an interactive review agent parked for
// you). `paseo ls` excludes archived agents, so any match means one is still in
// play. Used to gate re-dispatch of live-gated kinds (reviews).
func (d *Dispatcher) HasLiveAgent(ctx context.Context, prKey, kind string) bool {
	out, err := exec.CommandContext(ctx, d.PaseoBin, "ls", "--json",
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
