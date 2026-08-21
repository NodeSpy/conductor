package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Reaper archives conductor agents that requested archive-when-done once they
// go idle. It polls the local daemon only (`paseo ls`) — no GitHub API.
type Reaper struct {
	PaseoBin string
	Interval time.Duration
	Log      func(string, ...any)
}

// Run reaps on an interval until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) {
	if r.Interval <= 0 {
		r.Interval = time.Minute
	}
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reap(ctx)
		}
	}
}

func (r *Reaper) reap(ctx context.Context) {
	// Filter on archive=1 only. `paseo ls` treats repeated --label as LAST-WINS
	// (not AND), so a second --label would just override the first — and archive=1
	// is set exclusively by the conductor, and only for archive_when_done agents,
	// so it already implies conductor=1 and is exactly the reap set. Interactive
	// hand-off (background) agents never get this label, so they're never reaped.
	out, err := exec.CommandContext(ctx, r.PaseoBin, "ls", "--json",
		"--label", "archive=1").Output()
	if err != nil {
		return
	}
	var agents []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Cwd    string `json:"cwd"`
	}
	if err := json.Unmarshal(out, &agents); err != nil {
		return
	}
	type idleAgent struct{ id, cwd string }
	var idle []idleAgent
	for _, a := range agents {
		if a.ID == "" {
			continue
		}
		switch strings.ToLower(a.Status) {
		case "idle", "completed", "done", "":
			idle = append(idle, idleAgent{a.ID, a.Cwd})
		}
	}
	if len(idle) == 0 {
		return
	}

	// Map agent cwd -> its worktree workspace so we can archive the *workspace*
	// (which reclaims the worktree AND the agent it owns). Only worktree-isolation
	// workspaces are archivable — never a shared/base checkout.
	worktrees := r.worktreeWorkspaces(ctx)
	for _, a := range idle {
		// Don't cull an agent that still needs the user — it set a hold marker in
		// its worktree, or it's blocked on a permission decision.
		if r.needsUser(ctx, a.id, a.cwd) {
			if r.Log != nil {
				r.Log("reaper: keeping agent %s — waiting on you (hold marker / pending permission)", a.id)
			}
			continue
		}
		if wksID := worktrees[normCwd(a.cwd)]; wksID != "" {
			if err := exec.CommandContext(ctx, r.PaseoBin, "workspace", "archive", wksID).Run(); err == nil && r.Log != nil {
				r.Log("reaper: archived idle agent %s + worktree %s", a.id, wksID)
			}
			continue
		}
		// No isolated worktree (e.g. checkout: none in a shared workspace): the
		// agent has nothing to reclaim beyond itself.
		if err := exec.CommandContext(ctx, r.PaseoBin, "archive", a.id).Run(); err == nil && r.Log != nil {
			r.Log("reaper: archived idle agent %s", a.id)
		}
	}
}

// needsUser reports whether an idle agent should be spared from reaping because
// it's waiting on the user: either it created a hold marker in its worktree, or
// it's blocked on a pending permission decision.
func (r *Reaper) needsUser(ctx context.Context, id, cwd string) bool {
	if holdMarkerPresent(cwd) {
		return true
	}
	out, err := exec.CommandContext(ctx, r.PaseoBin, "inspect", id, "--json").Output()
	if err != nil {
		return false
	}
	var d struct {
		PendingPermissions []json.RawMessage `json:"PendingPermissions"`
	}
	if json.Unmarshal(out, &d) != nil {
		return false
	}
	return len(d.PendingPermissions) > 0
}

// holdMarkerPresent reports whether the agent set a .paseo-hold marker in cwd.
func holdMarkerPresent(cwd string) bool {
	if cwd == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(normCwd(cwd), HoldMarker))
	return err == nil
}

// worktreeWorkspaces maps workspace cwd -> id for worktree-isolation workspaces
// only, so the reaper never archives a shared or base checkout.
func (r *Reaper) worktreeWorkspaces(ctx context.Context) map[string]string {
	out, err := exec.CommandContext(ctx, r.PaseoBin, "workspace", "ls", "--json").Output()
	if err != nil {
		return nil
	}
	return parseWorktreeWorkspaces(out)
}

// parseWorktreeWorkspaces builds the cwd->id map from `paseo workspace ls --json`,
// keeping only worktree-isolation entries.
func parseWorktreeWorkspaces(data []byte) map[string]string {
	var wl []struct {
		WorkspaceID string `json:"workspaceId"`
		Cwd         string `json:"cwd"`
		Isolation   string `json:"isolation"`
	}
	if json.Unmarshal(data, &wl) != nil {
		return nil
	}
	m := map[string]string{}
	for _, w := range wl {
		if w.Isolation == "worktree" && w.Cwd != "" && w.WorkspaceID != "" {
			m[normCwd(w.Cwd)] = w.WorkspaceID
		}
	}
	return m
}

// normCwd canonicalizes a workspace/agent path so they compare equal regardless
// of source: `paseo ls` reports agent cwd as `~/…` while `paseo workspace ls`
// reports it absolute. Expand a leading `~` to $HOME, then clean.
func normCwd(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				p = home
			} else {
				p = filepath.Join(home, p[2:])
			}
		}
	}
	return filepath.Clean(p)
}
