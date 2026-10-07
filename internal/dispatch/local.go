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
	// The credentials the event's connector declares replace any the daemon's
	// own environment carries under the same names, so a command acts with the
	// identity the connector gives it. The action's own env (below) can still
	// set tool-specific variables.
	names := make([]string, 0, len(req.Credentials.Env))
	for k := range req.Credentials.Env {
		names = append(names, k)
	}
	c.Env = append(envWithout(os.Environ(), names...), req.Credentials.EnvList()...)
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
