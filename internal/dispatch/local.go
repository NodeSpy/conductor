package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
	c.Env = os.Environ()
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
