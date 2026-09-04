package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/hosts"
)

// Reaper archives conductor agents that requested archive-when-done once they
// go idle. It polls the local daemon only (`paseo ls`) — no GitHub API.
// reaperGraceDefault is the startup grace: an agent younger than this is never
// reaped. A freshly launched agent reports "idle" before the model engages, so a
// reaper tick landing in that window would kill it before it does any work.
const reaperGraceDefault = 3 * time.Minute

type Reaper struct {
	PaseoBin string
	// Remote runs the reaper's paseo invocations on an SSH host — one reaper
	// per remote paseo runtime (its agents live on that box). nil = local.
	Remote   *hosts.Target
	Interval time.Duration
	MinAge   time.Duration // don't reap agents younger than this (default reaperGraceDefault)
	Log      func(string, ...any)

	// Held is the conductor's explicit "never reap" set — agent ids handed off for
	// you to drive (background workflow steps). The engine populates it at launch.
	// This is the authoritative keep-signal for hand-offs, independent of labels or
	// markers (which the reaper can't reliably observe for a background agent).
	Held *HoldSet

	// held remembers agents that have entered a back-and-forth with the user (asked
	// a question / set a hold marker). Once an agent interacts it becomes the user's
	// to drive and close, so the reaper leaves it alone for the rest of its life —
	// even after the question is answered and the pending permission clears. Pruned
	// when the agent is no longer listed (the user archived it).
	held map[string]bool
}

// markAndSpare records whether an agent has entered user interaction and reports
// whether it should be spared. Once held, it stays held regardless of holdingNow.
// firstHold is true only on the poll where it transitions into the held set.
func (r *Reaper) markAndSpare(id string, holdingNow bool) (spared, firstHold bool) {
	if r.held == nil {
		r.held = map[string]bool{}
	}
	if r.held[id] {
		return true, false
	}
	if holdingNow {
		r.held[id] = true
		return true, true
	}
	return false, false
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
	// hand-off agents shouldn't carry this label — but that's protection by absence;
	// the authoritative guard is the engine-registered Held set, checked per agent
	// below, so a hand-off survives even if it somehow lands in this list.
	out, err := r.paseoCmd(ctx, "ls", "--json",
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
	// Track everything currently listed so we can forget held agents you've closed.
	present := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.ID != "" {
			present[a.ID] = true
		}
	}

	if len(idle) > 0 {
		// Map agent cwd -> its worktree workspace so we can archive the *workspace*
		// (which reclaims the worktree AND the agent it owns). Only worktree-isolation
		// workspaces are archivable — never a shared/base checkout.
		worktrees := r.worktreeWorkspaces(ctx)
		for _, a := range idle {
			// Explicit hand-off hold (engine-registered at launch): never reap,
			// regardless of labels/markers. This is the deterministic protection for
			// interactive hand-off agents that carry no other "needs you" signal.
			if r.Held.Has(a.id) {
				continue
			}
			// Once an agent has entered a back-and-forth with you (asked a question,
			// pending permission, or a hold marker), it's yours to drive and close —
			// the reaper leaves it AND its workspace alone for life. Already-held
			// agents skip the inspect entirely.
			if r.held[a.id] {
				continue
			}
			needsUser, created, engaged := r.idleState(ctx, a.id, a.cwd)
			// Record an interaction first (keeps the sticky-hold correct even if the
			// Q&A happened during the startup grace below).
			if spared, first := r.markAndSpare(a.id, needsUser); spared {
				if first && r.Log != nil {
					r.Log("reaper: agent %s asked for you — keeping it + its workspace; archive it yourself when done", a.id)
				}
				continue
			}
			// Startup grace: never reap an agent still in its spin-up window (it
			// launches "idle" before the model engages; a tick there would kill it
			// before any work — see #4795, reaped 7s after launch with no usage). The
			// grace applies ONLY to agents that haven't engaged yet — one that has done
			// work and gone idle is finished and reaped now, not held for the full age
			// grace (which made a quick fixer sit around as "done" for minutes).
			if withinStartupGrace(engaged, created, time.Now(), r.minAge()) {
				continue
			}
			if wksID := worktrees[normCwd(a.cwd)]; wksID != "" {
				if err := r.paseoCmd(ctx, "workspace", "archive", wksID).Run(); err == nil && r.Log != nil {
					r.Log("reaper: archived idle agent %s + worktree %s", a.id, wksID)
				}
				continue
			}
			// No isolated worktree (e.g. checkout: none in a shared workspace): the
			// agent has nothing to reclaim beyond itself.
			if err := r.paseoCmd(ctx, "archive", a.id).Run(); err == nil && r.Log != nil {
				r.Log("reaper: archived idle agent %s", a.id)
			}
		}
	}

	// Forget held agents no longer listed (you archived them), keeping the set bounded.
	for id := range r.held {
		if !present[id] {
			delete(r.held, id)
		}
	}
	// Prune the explicit hand-off hold-set against the FULL agent list (a held
	// hand-off carries no archive=1, so it's absent from `present` above — pruning
	// against that would wrongly drop it). It's forgotten only once you archive it.
	r.Held.keepOnly(r.presentIDs(ctx))

	// Tidy the shared checkout:none scratch workspace when nothing is running in it.
	r.cullScratch(ctx)
}

// presentIDs is the set of all non-archived agent ids on the local daemon.
func (r *Reaper) presentIDs(ctx context.Context) map[string]bool {
	out, err := r.paseoCmd(ctx, "ls", "--json").Output()
	if err != nil {
		return nil
	}
	var a []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out, &a) != nil {
		return nil
	}
	ids := make(map[string]bool, len(a))
	for _, x := range a {
		if x.ID != "" {
			ids[x.ID] = true
		}
	}
	return ids
}

// cullScratch archives the shared checkout:none scratch workspace when no agent is
// running in it. It's recreated on demand (resolveScratchWorkspace re-resolves by
// title), so culling just keeps an idle conductor tidy. Skipped when any active
// agent's cwd matches the scratch cwd — archiving the workspace would take a
// running assess agent down with it.
func (r *Reaper) cullScratch(ctx context.Context) {
	id, cwd := r.findScratch(ctx)
	if id == "" {
		return
	}
	for _, ac := range r.activeAgentCwds(ctx) {
		if normCwd(ac) == normCwd(cwd) {
			return // in use — leave it
		}
	}
	if err := r.paseoCmd(ctx, "workspace", "archive", id).Run(); err == nil && r.Log != nil {
		r.Log("reaper: archived idle scratch workspace %s (recreated on demand)", id)
	}
}

// findScratch returns the shared scratch workspace's id and cwd, or ""s if absent.
func (r *Reaper) findScratch(ctx context.Context) (id, cwd string) {
	out, err := r.paseoCmd(ctx, "workspace", "ls", "--json").Output()
	if err != nil {
		return "", ""
	}
	var wl []struct {
		WorkspaceID string `json:"workspaceId"`
		Name        string `json:"name"`
		Isolation   string `json:"isolation"`
		Cwd         string `json:"cwd"`
	}
	if json.Unmarshal(out, &wl) != nil {
		return "", ""
	}
	for _, w := range wl {
		if w.Isolation == "local" && w.Name == scratchWorkspaceTitle && w.WorkspaceID != "" {
			return w.WorkspaceID, w.Cwd
		}
	}
	return "", ""
}

// activeAgentCwds lists the cwds of non-archived agents on the local daemon.
func (r *Reaper) activeAgentCwds(ctx context.Context) []string {
	out, err := r.paseoCmd(ctx, "ls", "--json").Output()
	if err != nil {
		return nil
	}
	var a []struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(out, &a) != nil {
		return nil
	}
	cwds := make([]string, 0, len(a))
	for _, x := range a {
		cwds = append(cwds, x.Cwd)
	}
	return cwds
}

// minAge is the startup grace, defaulting to reaperGraceDefault.
func (r *Reaper) minAge() time.Duration {
	if r.MinAge > 0 {
		return r.MinAge
	}
	return reaperGraceDefault
}

// idleState inspects an idle agent once: whether it's waiting on the user (a hold
// marker or a pending permission), when it was created (for the startup grace),
// and whether it has ENGAGED (done any model work — LastUsage set). A zero created
// time means the age is unknown — the grace is skipped.
func (r *Reaper) idleState(ctx context.Context, id, cwd string) (needsUser bool, created time.Time, engaged bool) {
	if r.holdMarkerPresent(cwd) {
		needsUser = true
	}
	out, err := r.paseoCmd(ctx, "inspect", id, "--json").Output()
	if err != nil {
		return needsUser, time.Time{}, false
	}
	var d struct {
		PendingPermissions []json.RawMessage `json:"PendingPermissions"`
		CreatedAt          string            `json:"CreatedAt"`
		LastUsage          string            `json:"LastUsage"`
	}
	if json.Unmarshal(out, &d) != nil {
		return needsUser, time.Time{}, false
	}
	if len(d.PendingPermissions) > 0 {
		needsUser = true
	}
	created, _ = time.Parse(time.RFC3339, d.CreatedAt)
	// LastUsage is set once the model has actually run — i.e. the agent got past
	// its "idle before it engages" spin-up phase and did work. An engaged agent
	// that's now idle is genuinely finished, not spinning up.
	engaged = d.LastUsage != ""
	return needsUser, created, engaged
}

// withinStartupGrace reports whether an idle agent should be spared this tick
// because it may still be spinning up: it hasn't engaged yet (no usage) AND is
// younger than the grace window. Once an agent has engaged it's never in spin-up,
// so a finished agent is reaped without waiting out the age grace — a quick fixer
// that had nothing to do doesn't linger as "done".
func withinStartupGrace(engaged bool, created, now time.Time, grace time.Duration) bool {
	return !engaged && !created.IsZero() && now.Sub(created) < grace
}

// holdMarkerPresent reports whether the agent set a .paseo-hold marker in cwd.
func (r *Reaper) holdMarkerPresent(cwd string) bool {
	if cwd == "" {
		return false
	}
	if r.Remote != nil {
		// The marker file lives on the remote box; the explicit HoldSet (and
		// the pending-permission signal from inspect) remain authoritative.
		return false
	}
	_, err := os.Stat(filepath.Join(normCwd(cwd), HoldMarker))
	return err == nil
}

// worktreeWorkspaces maps workspace cwd -> id for worktree-isolation workspaces
// only, so the reaper never archives a shared or base checkout.
func (r *Reaper) worktreeWorkspaces(ctx context.Context) map[string]string {
	out, err := r.paseoCmd(ctx, "workspace", "ls", "--json").Output()
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
