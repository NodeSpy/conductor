package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Paseo registers a PROJECT for every directory a workspace is created from, and
// keeps it — listed in its sidebar — after every workspace in it is archived.
// Conductor creates workspaces from two directories of its own: the base
// checkout it clones under ~/.conductor/checkouts/<name> (resolveCheckoutDir)
// and the per-run directories under ~/.conductor/runs/<slug> (runWorkspace).
// Archive reclaims the workspaces, so this file reclaims the projects they leave
// behind: PruneProjects unregisters a conductor-owned project once nothing in it
// is active.
//
// Unregistering is cheap to undo — the next worktree cut from the same checkout
// registers the project again — and touches nothing on disk: paseo drops the
// registry entry only. What it does NOT do is spare live work: `paseo project
// delete` archives every still-active workspace in the project. So a project is
// removed only when it has been seen idle on two consecutive sweeps (a grace
// period that also keeps a project from flickering out between one step's
// archive and the next step's worktree), and the check-then-delete is serialized
// against workspace creation by projectGuard.

// projectGuard serializes PruneProjects (exclusive) against every workspace
// creation (shared, so concurrent dispatches still create in parallel). Without
// it a worktree created between the prune's "is anything active?" listing and
// its delete would be archived out from under the agent about to run in it.
// Package-level because every dispatcher may drive the same paseo daemon.
var projectGuard sync.RWMutex

// holdProjects marks a workspace creation in progress; call the returned func
// once the workspace exists (it is then active, so the prune sees it).
func holdProjects() func() {
	projectGuard.RLock()
	return projectGuard.RUnlock
}

// ProjectPruneInterval is how often RunProjectPrune sweeps. A project is
// removed on the second idle sighting, so it lingers at most twice this long
// after its last workspace is archived.
const ProjectPruneInterval = 5 * time.Minute

// RunProjectPrune sweeps once at startup and on every tick until ctx ends.
// Startup matters: a project left by a run that died with the old daemon has no
// archive coming to trigger anything else. logf reports removals, and a sweep
// failure only when the previous sweep succeeded — a paseo without the
// `project` command would otherwise log the same error every tick.
func (d *Dispatcher) RunProjectPrune(ctx context.Context, interval time.Duration, logf func(string, ...any)) {
	if d.DryRun {
		return
	}
	failing := false
	sweep := func() {
		removed, err := d.PruneProjects(ctx)
		switch {
		case err != nil && !failing && ctx.Err() == nil:
			logf("paseo: project prune: %v", err)
		case err == nil && len(removed) > 0:
			logf("paseo: removed %d idle conductor project(s): %s", len(removed), strings.Join(removed, ", "))
		}
		failing = err != nil
	}
	sweep()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}

// PruneProjects unregisters every conductor-owned paseo project that has been
// idle — no active workspace — on this sweep AND the previous one, returning
// the paths it removed. A project not under conductor's own directories (one
// you registered) is never considered. Any listing failure removes nothing:
// without a full picture of what's active, nothing can be proven idle.
func (d *Dispatcher) PruneProjects(ctx context.Context) ([]string, error) {
	projectGuard.Lock()
	defer projectGuard.Unlock()

	projects, err := d.backend().ListProjects(ctx)
	if err != nil {
		d.setIdleProjects(nil) // a gap in sightings restarts every grace period
		return nil, err
	}
	var owned []ProjectInfo
	for _, p := range projects {
		if p.ProjectID != "" && d.isConductorProjectPath(p.Path) {
			owned = append(owned, p)
		}
	}
	idle := map[string]bool{}
	if len(owned) > 0 {
		active, err := d.backend().ListWorkspaces(ctx)
		if err != nil {
			d.setIdleProjects(nil)
			return nil, err
		}
		for _, p := range owned {
			if !projectInUse(p, active) {
				idle[p.ProjectID] = true
			}
		}
	}

	prev := d.setIdleProjects(idle)

	var removed []string
	for _, p := range owned {
		if !idle[p.ProjectID] || !prev[p.ProjectID] {
			continue
		}
		if err := d.backend().RemoveProject(ctx, p.ProjectID); err != nil {
			continue // still idle next sweep; it is retried then
		}
		removed = append(removed, p.Path)
	}
	sort.Strings(removed)
	return removed, nil
}

// setIdleProjects records this sweep's idle set and returns the previous one.
func (d *Dispatcher) setIdleProjects(idle map[string]bool) map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	prev := d.idleProjects
	d.idleProjects = idle
	return prev
}

// projectInUse reports whether any active workspace belongs to the project.
// `paseo workspace ls` names a workspace's project but carries no project id,
// so membership is matched by name — and, belt and braces, by the workspace
// sitting inside the project's directory (a run workspace's cwd IS its
// project's path). Either match keeps the project: a false "in use" only
// delays a removal, a false "idle" would archive live work.
func projectInUse(p ProjectInfo, active []WorkspaceInfo) bool {
	root := normCwd(p.Path)
	for _, w := range active {
		if p.Name != "" && strings.EqualFold(w.Project, p.Name) {
			return true
		}
		if w.Cwd != "" && pathWithin(normCwd(w.Cwd), root) {
			return true
		}
	}
	return false
}

// isConductorProjectPath reports whether a project's directory is one conductor
// created a workspace from: strictly inside its checkouts/ or runs/ directory.
// Locally that is an exact prefix under ~/.conductor. A remote paseo reports
// paths on its own box, under a login directory this one can't see (see
// cloneParentDir), so there the test is the ".conductor/<dir>/" segment.
func (d *Dispatcher) isConductorProjectPath(path string) bool {
	if path == "" {
		return false
	}
	p := filepath.Clean(path)
	for _, sub := range []string{"checkouts", runWorkspaceDirs} {
		if d.remote() {
			seg := "/.conductor/" + sub + "/"
			if i := strings.Index(p, seg); i >= 0 && len(p) > i+len(seg) {
				return true
			}
			if strings.HasPrefix(p, ".conductor/"+sub+"/") {
				return true
			}
			continue
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		root := filepath.Join(home, ".conductor", sub)
		if p != root && pathWithin(p, root) {
			return true
		}
	}
	return false
}

// pathWithin reports whether p is root or lies beneath it (both cleaned).
func pathWithin(p, root string) bool {
	if root == "" || root == "." {
		return false
	}
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
