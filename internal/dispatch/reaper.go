package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/hosts"
)

// Reaper archives conductor agents that requested archive-when-done once they
// go idle. It polls the local daemon only (`paseo ls`) — no GitHub API.
//
// Scope: paseo agents, and only those. `paseo ls` is the idle signal, and no
// other transport has an equivalent — ACP exposes no roster, opencode's lives
// behind its server API, and a CLI recipe is just a process. On those
// transports `archive_when_done` is closed deterministically instead, by the
// engine archiving through the runner that opened the session the moment the
// step finishes (see engine.archiveAgent); the backstop the reaper provides
// for paseo is the controller's own session lifetime. A cross-transport reaper
// would need a per-transport idle probe and is deliberately not built here.
// reaperGraceDefault is the startup grace: an agent younger than this is never
// reaped. A freshly launched agent reports "idle" before the model engages, so a
// reaper tick landing in that window would kill it before it does any work.
const reaperGraceDefault = 3 * time.Minute

// orphanGraceDefault is how long a conductor-owned workspace must sit agent-less
// before the orphan sweep archives it (Fix C). It is FAR longer than the startup
// grace because the sweep has no agent to anchor on: the only thing separating "a
// leaked workspace whose agent never launched" from "a workspace being created
// right now for an agent about to launch" is age, so the window must comfortably
// exceed how long create→launch ever takes.
const orphanGraceDefault = 30 * time.Minute

// wedgedGraceDefault is how long a conductor-owned agent may sit with NO model
// activity (stale LastUsage) before the wedged sweep reclaims it — the backstop
// for an agent that NEVER goes idle, so the archive=1 idle walk can never reap
// it. The case that motivated it: a hand-off released by handoff.done but stuck
// `running` for hours (hand-offs carry no archive=1 label, so only this sweep or
// handoff.done's own reclaim can reach them). Long, because the signal is "no
// model work in this window" — a healthy agent updates usage far more often, and
// one legitimately waiting on the user is spared by the Held / needs-user /
// pending-permission checks first.
const wedgedGraceDefault = 60 * time.Minute

type Reaper struct {
	PaseoBin string
	// Remote runs the reaper's paseo invocations on an SSH host — one reaper
	// per remote paseo runtime (its agents live on that box). nil = local.
	Remote *hosts.Target
	// Home is the paseo daemon home this reaper targets (emitted as `--home` on
	// paseo >= 0.9; omitted on older paseo). Mirrors Dispatcher.Home — a reaper's
	// agents live in the same daemon its dispatcher launches into.
	Home string
	// verCache lazily probes+caches `paseo --version` for this reaper's bin/host
	// (see paseoversion.go), so the --home gate matches the dispatcher's.
	verCache     paseoVersionCache
	Interval     time.Duration
	MinAge       time.Duration // don't reap agents younger than this (default reaperGraceDefault)
	OrphanMinAge time.Duration // don't sweep an agent-less workspace younger than this (default orphanGraceDefault)
	WedgedMinAge time.Duration // don't reap a live-but-inactive agent until usage is stale this long (default wedgedGraceDefault)
	Log          func(string, ...any)

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

	// backendImpl is the paseo-daemon Backend this Reaper queries and archives
	// through. nil (the default, and every existing construction path) uses
	// cliBackend over this Reaper's own PaseoBin/Remote — byte-identical argv
	// to the pre-Backend-interface reaper. Set to an rpcBackend to reap through
	// a conductor-paseo plugin instead. Unexported so every call site goes
	// through backend(), which supplies the default.
	//
	// Only the paseo-CLI HOW lives behind it: the reap POLICY (which agents are
	// idle, the hold-marker/HoldSet checks, the startup grace, worktree-vs-agent
	// archive choice) stays in this file.
	backendImpl Backend
}

// SetBackend configures the Backend this Reaper drives paseo through. nil (or
// never calling SetBackend) keeps the default cliBackend — the bundled,
// CLI-shelling path. Not safe to call concurrently with a reap in progress.
func (r *Reaper) SetBackend(b Backend) { r.backendImpl = b }

// backend returns the configured Backend, defaulting to a cliBackend built
// from this Reaper's own PaseoBin/Remote (the same exec seam paseoCmd used).
func (r *Reaper) backend() Backend {
	if r.backendImpl != nil {
		return r.backendImpl
	}
	return newReaperCLIBackend(r)
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
	// Filter on archive=1 only — one label, so this renders the single
	// `--label archive=1` it always did. `paseo ls` treats repeated --label as
	// LAST-WINS (not AND), so a second one would just override the first — and
	// archive=1 is set exclusively by the conductor, and only for
	// archive_when_done agents, so it already implies conductor=1 and is exactly
	// the reap set. Interactive
	// hand-off agents shouldn't carry this label — but that's protection by absence;
	// the authoritative guard is the engine-registered Held set, checked per agent
	// below, so a hand-off survives even if it somehow lands in this list.
	agents, err := r.backend().ListAgents(ctx, map[string]string{"archive": "1"})
	if err != nil {
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
		// Map agent cwd -> the workspace conductor created for its run, so we can
		// archive the *workspace* (which reclaims the directory AND the agent it
		// owns). Covers both an isolated worktree and an un-pinned checkout:none
		// run's ephemeral workspace — never a pinned or base checkout.
		reclaimable := r.reclaimableWorkspaces(ctx)
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
			if wksID := reclaimable[normCwd(a.cwd)]; wksID != "" {
				if err := r.backend().ArchiveWorkspace(ctx, wksID); err == nil && r.Log != nil {
					r.Log("reaper: archived idle agent %s + workspace %s", a.id, wksID)
				}
				continue
			}
			// Nothing of ours to reclaim (a PINNED workspace, or a base checkout):
			// the workspace outlives the run by design, so archive just the agent.
			if err := r.backend().ArchiveAgent(ctx, a.id); err == nil && r.Log != nil {
				r.Log("reaper: archived idle agent %s", a.id)
			}
		}
	}

	// Sweep conductor workspaces no agent ever bound — the orphan case the
	// agent-anchored walk above structurally cannot see (Fix C).
	r.reapOrphanWorkspaces(ctx)

	// Backstop: reclaim a conductor-owned agent that is present but WEDGED — no
	// model activity for a long window — which neither walk above catches (it's
	// not archive=1, and its workspace still has a live agent). This is what
	// finally reaps a hand-off that called handoff.done but stayed `running`.
	r.reapWedgedAgents(ctx)

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
}

// presentIDs is the set of all non-archived agent ids on the local daemon.
func (r *Reaper) presentIDs(ctx context.Context) map[string]bool {
	a, err := r.backend().ListAgents(ctx, nil)
	if err != nil {
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

// reapOrphanWorkspaces archives conductor-owned workspaces no agent ever bound.
// The agent-anchored walk above reclaims a run's workspace via its still-listed
// AGENT, which covers the normal and crashed-mid-run cases — but NOT a workspace
// whose agent never launched at all (a dispatch that errored before RunAgent
// returned an id: MISSING_PROVIDER, or a crash between create and Fix A's
// in-dispatch teardown). Such a workspace is agent-less forever, so the walk
// can't see it; without this sweep it lingers and (for a branch-off worktree)
// its deterministic branch collides with every retry. This is the backstop that
// makes the failure path self-healing — and the one that clears a pile that
// accumulated before Fix A shipped.
//
// The one hazard is racing a workspace being created RIGHT NOW for an agent
// about to launch. Age is what separates the two: an orphan's directory mtime is
// frozen at creation, while a just-created one is fresh — so only workspaces idle
// past orphanGrace are swept. Local only: the mtime probe is a filesystem stat,
// and a remote runtime's worktree lives on its own box (holdMarkerPresent takes
// the same local-only stance); the agent-anchored walk still runs there.
func (r *Reaper) reapOrphanWorkspaces(ctx context.Context) {
	if r.Remote != nil {
		return
	}
	wl, err := r.backend().ListWorkspaces(ctx)
	if err != nil {
		return
	}
	agents, err := r.backend().ListAgents(ctx, nil)
	if err != nil {
		return
	}
	live := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.Cwd != "" {
			live[normCwd(a.Cwd)] = true
		}
	}
	grace := r.orphanMinAge()
	now := time.Now()
	for _, w := range wl {
		if w.WorkspaceID == "" || w.Cwd == "" || !isConductorOwnedWorkspace(w) {
			continue
		}
		if live[normCwd(w.Cwd)] {
			continue // an agent is in it — the agent-anchored walk owns its lifetime
		}
		fi, err := os.Stat(normCwd(w.Cwd))
		if err != nil || now.Sub(fi.ModTime()) < grace {
			continue // already gone, or too fresh to be sure it isn't mid-create
		}
		if err := r.backend().ArchiveWorkspace(ctx, w.WorkspaceID); err == nil && r.Log != nil {
			r.Log("reaper: archived orphan workspace %s (%s) — no agent, idle %s",
				w.WorkspaceID, w.Name, now.Sub(fi.ModTime()).Round(time.Minute))
		}
	}
}

// reapWedgedAgents reclaims a conductor-owned workspace whose live agent has
// gone WEDGED: present, not held, not waiting on the user, and with no model
// activity for wedgedMinAge. It's the backstop for an agent that never returns
// to idle — most importantly a hand-off released by handoff.done that stayed
// `running` (hand-offs carry no archive=1 label, so the idle walk never lists
// them, and the orphan sweep skips a workspace that still has a live agent).
//
// Safety is by STALE USAGE, not status: a healthy agent — even a legitimately
// long-running one — updates LastUsage as it works, so only one with no model
// call in the whole grace window is reaped. Held hand-offs (still active), agents
// that asked for the user (r.held), and any with a pending permission are spared
// first, so this only ever takes an abandoned/wedged agent.
func (r *Reaper) reapWedgedAgents(ctx context.Context) {
	wl, err := r.backend().ListWorkspaces(ctx)
	if err != nil {
		return
	}
	agents, err := r.backend().ListAgents(ctx, nil)
	if err != nil {
		return
	}
	byCwd := make(map[string]AgentInfo, len(agents))
	for _, a := range agents {
		if a.ID != "" && a.Cwd != "" {
			byCwd[normCwd(a.Cwd)] = a
		}
	}
	grace := r.wedgedMinAge()
	now := time.Now()
	for _, w := range wl {
		// ONLY workspaces CONDUCTOR itself created — matched by its own name
		// prefix (conductor-run-* / conductor/*), the same ownership signal the
		// orphan sweep uses. This gate is load-bearing: an earlier version keyed
		// on "any worktree" (reclaimableWorkspaces) and archived a pile of the
		// USER's own paseo worktrees whose agents had long gone idle. The reaper
		// must never touch a workspace conductor did not launch.
		if w.WorkspaceID == "" || w.Cwd == "" || !isConductorOwnedWorkspace(w) {
			continue
		}
		a, ok := byCwd[normCwd(w.Cwd)]
		if !ok {
			continue // agent-less → the orphan sweep owns it
		}
		if r.Held.Has(a.ID) || r.held[a.ID] {
			continue // an active hand-off, or one that asked for you
		}
		d, err := r.backend().Inspect(ctx, a.ID)
		if err != nil {
			continue
		}
		if len(d.PendingPermissions) > 0 {
			continue // waiting on a permission — yours to answer
		}
		last := lastActivity(d)
		if last.IsZero() || now.Sub(last) < grace {
			continue // fresh, or actively using the model
		}
		if err := r.backend().ArchiveWorkspace(ctx, w.WorkspaceID); err == nil && r.Log != nil {
			r.Log("reaper: archived WEDGED agent %s + workspace %s (%s) — no model activity in %s, status %q",
				a.ID, w.WorkspaceID, w.Name, now.Sub(last).Round(time.Minute), a.Status)
		}
	}
}

// lastActivity is the most recent sign of life for an agent: its last model use,
// else its creation time (a never-engaged agent isn't wedged until it's old).
func lastActivity(d AgentDetail) time.Time {
	var last time.Time
	for _, s := range []string{d.LastUsage, d.CreatedAt} {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(last) {
			last = t
		}
	}
	return last
}

// wedgedMinAge is the no-activity grace before a live agent is treated as wedged.
func (r *Reaper) wedgedMinAge() time.Duration {
	if r.WedgedMinAge > 0 {
		return r.WedgedMinAge
	}
	return wedgedGraceDefault
}

// orphanMinAge is the agent-less-workspace grace, defaulting to orphanGraceDefault.
func (r *Reaper) orphanMinAge() time.Duration {
	if r.OrphanMinAge > 0 {
		return r.OrphanMinAge
	}
	return orphanGraceDefault
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
	d, err := r.backend().Inspect(ctx, id)
	if err != nil {
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

// reclaimableWorkspaces maps workspace cwd -> id for the workspaces conductor
// created for a run and may therefore archive — isolated worktrees and the
// ephemeral per-run workspaces of un-pinned checkout:none dispatches (see
// reclaimableWorkspaceMap). A pinned workspace, a base checkout, and anything
// you made yourself are excluded, so the reaper can never take one down.
func (r *Reaper) reclaimableWorkspaces(ctx context.Context) map[string]string {
	wl, err := r.backend().ListWorkspaces(ctx)
	if err != nil {
		return nil
	}
	return reclaimableWorkspaceMap(wl)
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
