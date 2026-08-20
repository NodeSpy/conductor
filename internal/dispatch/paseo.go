package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

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
	if req.Workspace != "" {
		argv = append(argv, "--workspace", req.Workspace)
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
	argv = append(argv, "--background", "--json")

	ref := RunRef{Backend: "paseo", Kind: req.Trigger.Kind, Argv: append([]string{d.PaseoBin}, argv...)}

	if d.DryRun || req.Shadow {
		ref.Shadowed = true
		return ref, nil
	}

	// Running-agent guard: skip if an agent for this PR+kind is already active.
	if active, _ := d.agentActive(ctx, req.Trigger); active {
		ref.Output = "skipped: agent already running for this pr+kind"
		return ref, nil
	}

	out, err := exec.CommandContext(ctx, d.PaseoBin, argv...).Output()
	ref.Output = string(out)
	if err != nil {
		return ref, fmt.Errorf("paseo run: %w", err)
	}
	ref.AgentID = parseAgentID(out)
	return ref, nil
}

// checkoutArgs maps an action's checkout strategy to paseo worktree flags.
func checkoutArgs(req Request) []string {
	strat := req.Action.Checkout
	if strat == "" {
		if req.Trigger.Target.PR > 0 {
			strat = "checkout-pr"
		} else {
			strat = "branch-off"
		}
	}
	switch strat {
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
