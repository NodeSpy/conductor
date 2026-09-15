package controller

import (
	"context"
	"sync"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// routedProvisioner picks the checkout path per dispatch and remembers who
// made each worktree, so teardown goes back to the same provisioner.
//
// It exists for the cli runtime (docs/design/cli-git-worktrees.md): a LOCAL cli
// launch provisions a plain git worktree under conductor's state dir and removes
// it on close, while a launch pinned to a `host:` stays on the paseo path —
// the git provisioner only ever touches THIS box's filesystem, so a remote
// runtime would be handed a path that does not exist where the agent runs.
type routedProvisioner struct {
	host     string      // the controller's configured host: ("" = local)
	git      Provisioner // local, git-native
	fallback Provisioner // paseo-backed (remote launches, and when git is unwired)

	mu    sync.Mutex
	owner map[string]Provisioner // worktree id → the provisioner that made it
}

func newRoutedProvisioner(host string, git, fallback Provisioner) *routedProvisioner {
	return &routedProvisioner{host: host, git: git, fallback: fallback, owner: map[string]Provisioner{}}
}

// pick resolves the provisioner for one dispatch. The step's host overrides the
// controller's (resolveHost), so a step pinned to a host falls back even on a
// local controller.
func (r *routedProvisioner) pick(req dispatch.Request) Provisioner {
	if resolveHost(r.host, req.Step.Host) != "" {
		return r.fallback
	}
	return r.git
}

func (r *routedProvisioner) ProvisionWorktree(ctx context.Context, req dispatch.Request) (string, string, error) {
	p := r.pick(req)
	if p == nil {
		// Nothing wired for this route: the runtime runs in its own default
		// directory, exactly as a nil Provisioner does.
		return "", "", nil
	}
	id, cwd, err := p.ProvisionWorktree(ctx, req)
	if err == nil && id != "" {
		r.mu.Lock()
		r.owner[id] = p
		r.mu.Unlock()
	}
	return id, cwd, err
}

// RemoveWorktree hands the id back to whichever provisioner created it. An id
// from a provisioner we have no record of (a session resumed after a restart)
// is left alone — the git provisioner's orphan reaper reclaims those.
func (r *routedProvisioner) RemoveWorktree(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	r.mu.Lock()
	p := r.owner[id]
	delete(r.owner, id)
	r.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.RemoveWorktree(ctx, id)
}
