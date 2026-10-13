package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// HonorsLaunchFields marks a runner that implements a step's launch fields —
// detach:, repo:, images: (see config.Step). The engine refuses to hand a
// step carrying any of them to a runner without this method: a runtime that
// silently ignored detach: would launch an OWNED, credentialed agent where the
// operator asked for a forgotten one, and one that ignored images: would drop
// the attachments without a word.
func (d *Dispatcher) HonorsLaunchFields() bool { return true }

// UsesLaunchFields reports whether a step sets any field only a runner with
// HonorsLaunchFields can carry out.
func UsesLaunchFields(req Request) bool {
	return req.Step.Detach || req.Step.Repo != "" || len(req.Step.Images) > 0
}

// launchFieldArgs is the `paseo run` argv for a step's mode: and images:.
// Both arrive ALREADY RENDERED (flow.renderLaunchFields) and are used
// literally — never templated again here, because a rendered value can carry
// event-supplied text and a second render would evaluate any {{…}} inside it.
func launchFieldArgs(s config.Step) []string {
	var argv []string
	if m := strings.TrimSpace(s.Mode); m != "" {
		argv = append(argv, "--mode", m)
	}
	for _, img := range s.Images {
		if img = expandTilde(strings.TrimSpace(img)); img != "" {
			argv = append(argv, "--image", img)
		}
	}
	return argv
}

// repoRefRe matches a plain "owner/name" repo reference.
var repoRefRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// gitURLRe matches the clonable URL forms a checkout accepts: https/ssh/git
// URLs and scp-style git@host:owner/name. No whitespace, no leading dash (a
// value that could be read as a flag by a downstream git/paseo argv).
var gitURLRe = regexp.MustCompile(`^(?:(?:https|ssh|git)://[^\s]+|git@[A-Za-z0-9.-]+:[^\s]+)$`)

// ValidateRepoRef checks a rendered repo reference is "owner/name" or a git
// URL a checkout can clone from.
func ValidateRepoRef(s string) error {
	if strings.HasPrefix(s, "-") || strings.Contains(s, "..") {
		return fmt.Errorf("%q is not owner/name or a git URL", s)
	}
	if repoRefRe.MatchString(s) || gitURLRe.MatchString(s) {
		return nil
	}
	return fmt.Errorf("%q is not owner/name or a git URL", s)
}

// withStepRepo applies a non-detach agent step's `repo:` override. The repo
// is the CHECKOUT target only (Target.Project, the field checkout resolution
// already reads): forge authority — skill creds, own-repo scope — stays on the
// trigger's own Target.Repo, so naming a repo here grants nothing beyond a
// working copy. When the override names a different repo than the trigger's,
// the trigger's base ref and PR number (which belong to the trigger's repo)
// are dropped for this dispatch, and an unset checkout defaults to
// branch-off. override reports whether a different repo was selected.
func withStepRepo(req Request) (_ Request, override bool, _ error) {
	repo := strings.TrimSpace(req.Step.Repo)
	if repo == "" {
		return req, false, nil
	}
	if err := ValidateRepoRef(repo); err != nil {
		return req, false, Unrecoverable(fmt.Errorf("repo: %w", err))
	}
	if repo != req.Trigger.Target.CheckoutRepo() {
		req.Trigger.Target.Project = repo
		req.Trigger.Target.BaseRef = ""
		req.Trigger.Target.PR = 0
		override = true
	}
	if req.Action.Checkout == "" {
		req.Action.Checkout = "branch-off"
	}
	return req, override, nil
}

// detachRepo resolves the checkout repo for a `detach:` step: its own
// `repo:` when set, else the trigger's own target. Rejects a blank or
// malformed result rather than letting dispatch fail opaquely downstream.
func detachRepo(req Request) (string, error) {
	repo := req.Trigger.Target.CheckoutRepo()
	if r := strings.TrimSpace(req.Step.Repo); r != "" {
		repo = r
	}
	if repo == "" {
		return "", fmt.Errorf("detach: no repo to check out — set step `repo:` (this trigger carries none of its own)")
	}
	if err := ValidateRepoRef(repo); err != nil {
		return "", fmt.Errorf("detach: repo: %w", err)
	}
	return repo, nil
}

// detachBaseRef is the base branch a detach worktree forks from: the
// trigger's own BaseRef only when the step kept the trigger's own repo — an
// override to a DIFFERENT repo has no meaningful relationship to the
// trigger's branch, so it gets paseo's own default-branch behavior instead.
func detachBaseRef(req Request) string {
	if r := strings.TrimSpace(req.Step.Repo); r != "" && r != req.Trigger.Target.CheckoutRepo() {
		return ""
	}
	return req.Trigger.Target.BaseRef
}

// detachTitle is the paseo agent title for a detach launch: the step's own
// `agent:` label when set (flow renders it), else the trigger's title — never
// conductor's own "conductor: <repo>#<n> <kind>" shape, which would mislabel
// what is now the user's own workspace.
func detachTitle(req Request) string {
	if t := strings.TrimSpace(req.Step.Agent); t != "" {
		return t
	}
	if t := strings.TrimSpace(req.Trigger.Title); t != "" {
		return t
	}
	return "handover"
}

// detachBranchRe is the character set a detach branch may use: git's own
// ref rules are looser, but this is enough for any sensible name and keeps a
// form-supplied value from smuggling flags, refspecs, or path tricks.
var detachBranchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// detachBranch is the new worktree's branch: the step's `branch:` when set
// (validated), else "handover/<slug of title>-<random>". The random suffix
// makes the derived name unique per launch, so two hand-offs with the same
// title never share a workspace.
func detachBranch(req Request, title string) (string, error) {
	if b := strings.TrimSpace(req.Step.Branch); b != "" {
		if !detachBranchRe.MatchString(b) || strings.Contains(b, "..") || strings.HasSuffix(b, "/") ||
			strings.HasSuffix(b, ".lock") || strings.Contains(b, "//") {
			return "", fmt.Errorf("detach: branch: %q is not a valid branch name", b)
		}
		return b, nil
	}
	slug := SanitizeBranchSuffix(title)
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "handover"
	}
	var buf [3]byte
	_, _ = rand.Read(buf[:])
	return "handover/" + slug + "-" + hex.EncodeToString(buf[:]), nil
}

// paseoDetached launches a `detach: true` step: the step IS the whole launch
// (Step.Detach's doc) — a fresh branch-off worktree, `paseo run -d`
// (background, no wait), and then forgotten. Unlike paseo(), it:
//
//   - never calls queueOrAdopt, and asks the backend for a FRESH worktree
//     (CreateWorktreeOptions.Fresh) — it never adopts an existing workspace
//     on the branch, and refuses outright if the backend hands back one the
//     ownership ledger records (it would put the user's agent inside a
//     workspace conductor may later archive);
//   - never records the agent or workspace in d.Owned, so Archive (the
//     ownership chokepoint) refuses this id forever;
//   - never calls SkillEnv / injects CONDUCTOR_* or token/identity env;
//   - appends no guidance — the prompt it receives (req.Action.Prompt) is
//     already the bare templated prompt, because flow's execAgent skips the
//     whole guidance/memory/handoff-wrapper block for a detach step.
func (d *Dispatcher) paseoDetached(ctx context.Context, req Request) (RunRef, error) {
	// No conductor credential reaches a detached launch in any form — not
	// as env, and not through a {{.credentials.<name>}} (or a declared
	// template alias) in its prompt.
	req.Credentials = Credentials{}
	prompt, err := promptText(req)
	if err != nil {
		return RunRef{}, fmt.Errorf("render prompt: %w", err)
	}
	if err := checkPromptSize(prompt); err != nil {
		return RunRef{}, err
	}
	repo, err := detachRepo(req)
	if err != nil {
		return RunRef{}, Unrecoverable(err)
	}
	title := detachTitle(req)
	branch, err := detachBranch(req, title)
	if err != nil {
		return RunRef{}, Unrecoverable(err)
	}

	argv := []string{"run", prompt, "--title", title}
	if req.Provider != "" {
		argv = append(argv, "--provider", req.Provider)
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if req.Step.Thinking != "" {
		argv = append(argv, "--thinking", req.Step.Thinking)
	}
	argv = append(argv, launchFieldArgs(req.Step)...)

	ref := RunRef{Backend: "paseo", Kind: req.Trigger.Kind, Branch: branch, Detached: true}
	baseRef := detachBaseRef(req)

	if d.DryRun || req.Shadow {
		// Preview: render the argv a real launch would use without touching
		// the daemon (no checkout resolution, no worktree creation).
		argv = append(argv, "--new-workspace", "worktree", "--new-branch", branch)
		if baseRef != "" {
			argv = append(argv, "--base", baseRef)
		}
		argv = append(argv, "-d", "--json")
		ref.Argv = append([]string{d.PaseoBin}, argv...)
		ref.Shadowed = true
		return ref, nil
	}

	dir, err := d.resolveCheckoutDir(ctx, repo)
	if err != nil {
		return RunRef{}, Unrecoverable(fmt.Errorf("detach: resolve checkout dir for %s: %w", repo, err))
	}
	res, err := d.backend().CreateWorktree(ctx, CreateWorktreeOptions{
		Isolation: "worktree", Path: dir, Strategy: "branch-off",
		NewBranch: branch, BaseRef: baseRef, Fresh: true,
	})
	if err != nil {
		return RunRef{}, Unrecoverable(fmt.Errorf("detach: create worktree for %s: %w", repo, err))
	}
	if d.Owned.HasWorkspace(res.WorkspaceID) {
		return RunRef{}, Unrecoverable(fmt.Errorf("detach: branch %q resolved to workspace %s, which conductor owns — refusing to launch a detached agent into it", branch, res.WorkspaceID))
	}
	ref.WorkspaceID = res.WorkspaceID
	if !d.remote() {
		ref.Workdir = res.Cwd
	}
	argv = append(argv, "--workspace", res.WorkspaceID, "-d", "--json")
	ref.Argv = append([]string{d.PaseoBin}, argv...)

	result, err := d.backend().RunAgent(ctx, RunAgentOptions{Args: argv})
	ref.Output = result.Output
	if err != nil {
		return ref, err
	}
	ref.AgentID = result.AgentID
	// Deliberately NO d.Owned.AddAgent / AddWorkspace / BindDispatch: the
	// whole point of detach is that conductor forgets this workspace the
	// instant it launches it. See Dispatcher.Archive.
	return ref, nil
}
