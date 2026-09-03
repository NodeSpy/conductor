package dispatch

import (
	"context"
	"fmt"
)

// This file exposes the pieces of the paseo dispatch path that the non-paseo
// controllers (ACP, opencode-native, agent-deck, cli) reuse, so every controller
// preserves conductor's two invariants: the agent runs in the worktree conductor
// provisions (never a runtime-chosen dir), and it acts as YOU (your token for all
// writes, your git author identity). Keeping this here means one checkout path and
// one identity model across all runtimes — a controller that hand-rolled either
// would drift from the paseo backend.

// ProvisionWorktree resolves the conductor-supplied checkout a controller runs an
// agent in, using the same logic as the paseo backend: an explicit WorkDir wins; a
// PR/branch checkout resolves a stable base checkout for the repo and creates an
// isolated worktree up front (returning its workspace id and absolute cwd); any
// other strategy (no repo context) has no dedicated worktree and returns an empty
// cwd, meaning "the runtime's own default directory".
//
// It reuses the Dispatcher's injectable CheckoutDir / WorktreeCreator hooks, so a
// test can provision without touching a live paseo daemon. *Dispatcher satisfies
// the controller package's Provisioner via this method.
func (d *Dispatcher) ProvisionWorktree(ctx context.Context, req Request) (id, cwd string, err error) {
	data := templateData(req)
	if req.Action.WorkDir != "" {
		wd, err := render(req.Action.WorkDir, data)
		if err != nil {
			return "", "", err
		}
		return "", expandTilde(wd), nil
	}
	switch effectiveStrategy(req) {
	case "checkout-pr", "branch-off":
		proj := req.Trigger.Target.CheckoutRepo()
		dir, err := d.resolveCheckoutDir(ctx, proj)
		if err != nil {
			return "", "", fmt.Errorf("resolve checkout dir for %s: %w", proj, err)
		}
		wsID, wcwd, err := d.createWorktree(ctx, req, dir)
		if err != nil {
			return "", "", fmt.Errorf("create worktree for %s: %w", proj, err)
		}
		return wsID, wcwd, nil
	default:
		// No repo/PR context (e.g. a cron trigger): no dedicated worktree. The
		// controller runs the agent in the runtime's default directory.
		return "", "", nil
	}
}

// AgentEnv returns the acts-as-user environment every controller must hand to the
// runtime it drives, identical to what the paseo backend passes via `--env`:
//
//   - GH_TOKEN / GITHUB_TOKEN = YOUR token, so every comment/review/API write is
//     attributed to you, never the App bot;
//   - PC_GH_WRITE_TOKEN = an alias of your token (legacy prompts);
//   - PC_GH_APP_TOKEN = the App installation token, for rate-limited READS only;
//   - GIT_AUTHOR_*/GIT_COMMITTER_* = your git identity, so commits are yours;
//   - the action's own env map, rendered against the trigger's template data.
//
// Factored out of the paseo argv builder so a non-paseo controller can apply the
// same identity without re-deriving it.
func AgentEnv(req Request) ([]string, error) {
	data := templateData(req)
	env := []string{
		"GH_TOKEN=" + req.Tokens.User,
		"GITHUB_TOKEN=" + req.Tokens.User,
		envGHWriteToken + "=" + req.Tokens.User,
		envGHAppToken + "=" + req.Tokens.App,
	}
	if req.Author.Name != "" {
		env = append(env,
			"GIT_AUTHOR_NAME="+req.Author.Name,
			"GIT_COMMITTER_NAME="+req.Author.Name)
	}
	if req.Author.Email != "" {
		env = append(env,
			"GIT_AUTHOR_EMAIL="+req.Author.Email,
			"GIT_COMMITTER_EMAIL="+req.Author.Email)
	}
	for k, v := range req.Action.Env {
		rv, err := render(v, data)
		if err != nil {
			return nil, err
		}
		env = append(env, k+"="+rv)
	}
	return env, nil
}

// RenderPrompt renders a request's action prompt against its trigger template data
// — the exact text the paseo backend sends as the agent's first turn. Controllers
// use it so the prompt a non-paseo runtime receives matches the paseo path
// (guidance wrappers are already baked into Action.Prompt by the engine).
func RenderPrompt(req Request) (string, error) {
	return render(req.Action.Prompt, templateData(req))
}
