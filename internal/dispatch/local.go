package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// local runs a command action as a direct subprocess (e.g. critique, or
// `gh pr merge`/`gh pr update-branch`). Env values are templated; tokens are
// injected via the action's env map (CRITIQUE_GITHUB_TOKEN, GH_TOKEN, …).
func (d *Dispatcher) local(ctx context.Context, req Request) (RunRef, error) {
	data := templateData(req)
	cmd, err := renderAll(req.Action.Command, data)
	if err != nil {
		return RunRef{}, fmt.Errorf("render command: %w", err)
	}
	if len(cmd) == 0 {
		return RunRef{}, fmt.Errorf("command action has empty command")
	}
	ref := RunRef{Backend: "local", Kind: req.Trigger.Kind, Argv: cmd}

	if d.DryRun || req.Shadow {
		ref.Shadowed = true
		return ref, nil
	}

	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	if req.Action.WorkDir != "" {
		wd, err := render(req.Action.WorkDir, data)
		if err != nil {
			return ref, err
		}
		c.Dir = expandTilde(wd)
	}
	// Identity default: a command acts as YOU. Strip any inherited GH_TOKEN/
	// GITHUB_TOKEN from the daemon's own environment and set them to your token, so
	// a command's gh/API write posts as you, never the App bot. The App token is
	// available as PC_GH_APP_TOKEN for reads. The action's own env (below) can still
	// set tool-specific tokens (e.g. CRITIQUE_GITHUB_TOKEN for App-pooled reads,
	// CRITIQUE_SUBMIT_TOKEN to submit as you).
	c.Env = envWithout(os.Environ(), "GH_TOKEN", "GITHUB_TOKEN")
	c.Env = append(c.Env,
		"GH_TOKEN="+req.Tokens.User,
		"GITHUB_TOKEN="+req.Tokens.User,
		envGHAppToken+"="+req.Tokens.App,
	)
	for k, v := range req.Action.Env {
		rv, err := render(v, data)
		if err != nil {
			return ref, err
		}
		c.Env = append(c.Env, k+"="+rv)
	}
	out, err := c.CombinedOutput()
	ref.Output = string(out)
	if err != nil {
		return ref, fmt.Errorf("command %q: %w", cmd[0], err)
	}
	return ref, nil
}

// envWithout returns env with any entries for the given keys removed (so we can
// authoritatively re-set identity tokens rather than inherit a stale value).
func envWithout(env []string, keys ...string) []string {
	out := env[:0:0]
	for _, e := range env {
		drop := false
		for _, k := range keys {
			if strings.HasPrefix(e, k+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}
