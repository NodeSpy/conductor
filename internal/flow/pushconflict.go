package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitpush"
)

// A PR branch that moves while its agent works (the author pushes, another
// fixer lands) used to lose the agent's work: conductor pushes fast-forward
// only, so the end-of-step push was refused and the step failed with its
// commits left in a workspace about to be removed. Now:
//
//   - the push itself rebases the agent's commits onto the moved head when
//     they replay cleanly (gitpush, sandbox.push.rebase, default on);
//   - a replay that conflicts is handed back: a live agent that can take a
//     follow-up turn is asked to rebase onto origin/<branch> and resolve, and
//     the push is tried again; otherwise the step is dispatched once more,
//     on the branch's new head, told where the first attempt's commits are
//     kept (resolveConflictOnce, execWithRetry);
//   - whatever still fails, the commits stay under a named ref in the
//     trusted git dir (gitpush.KeptError) and the step fails as before.

// pushConflict is the push failure of a step whose agent's commits conflict
// with the branch's moved head: what the agent resolves.
type pushConflict struct {
	branch, remote, head, since, kept string
	files                             []string
}

// asPushConflict reads a conflicting rebase out of an end-of-step push
// error. ok=false for any other push failure.
func asPushConflict(perr error) (pushConflict, bool) {
	var de *gitpush.DivergedError
	if !errors.As(perr, &de) || !de.Conflicted() {
		return pushConflict{}, false
	}
	pc := pushConflict{branch: de.Branch, remote: de.Remote, head: de.Head, files: de.Files}
	var kept *gitpush.KeptError
	if errors.As(perr, &kept) {
		pc.kept = kept.Ref
	}
	var pe *PushError
	if errors.As(perr, &pe) {
		pc.since = pe.Since
	}
	return pc, true
}

// resolvePrompt is the follow-up turn asking the live agent to resolve.
func (pc pushConflict) resolvePrompt() string {
	return fmt.Sprintf("Conductor could not push your work: the PR branch %s moved on the remote while you worked (it is now %s, available here as origin/%s), "+
		"and your commits conflict with it in %s. Rebase your commits onto origin/%[3]s (`git rebase origin/%[3]s`), resolve the conflicts so the result keeps both "+
		"the new upstream changes and your fix, finish the rebase with every conflict resolved and committed, and then stop. Conductor pushes your branch when you "+
		"finish. Never force anything and never discard the upstream commits.",
		pc.branch, short12(pc.remote), pc.branch, strings.Join(pc.files, ", "))
}

// redispatchNote is appended to the step's prompt when it is dispatched
// again on the branch's new head.
func (pc pushConflict) redispatchNote() string {
	where := "commit " + pc.head
	if pc.kept != "" {
		where += " (ref " + pc.kept + " in conductor's checkout of this repository)"
	}
	rng := pc.head
	if pc.since != "" {
		rng = pc.since + ".." + pc.head
	}
	return fmt.Sprintf("\n\nNOTE FROM CONDUCTOR: a previous attempt at this task made commits that could not be pushed, because the PR branch moved meanwhile "+
		"and they conflict with its new head in %s. That work is kept at %s. You are on the branch's new head: re-apply it (`git cherry-pick %s`, "+
		"or by hand), resolve the conflicts so the result keeps both the new upstream changes and the fix, and commit. Never force anything.",
		strings.Join(pc.files, ", "), where, rng)
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// pushConflictError fails a step whose push conflicted and could not be
// resolved in place. It unwraps to the step's no_progress error (hooks,
// failure context); execWithRetry re-dispatches the step once on it.
type pushConflictError struct {
	err error
	pc  pushConflict
}

func (e *pushConflictError) Error() string { return e.err.Error() }
func (e *pushConflictError) Unwrap() error { return e.err }

// conflictNoteKey carries the re-dispatch note to the step's next attempt.
type conflictNoteKey struct{}

func withConflictNote(ctx context.Context, note string) context.Context {
	return context.WithValue(ctx, conflictNoteKey{}, note)
}

func conflictNote(ctx context.Context) string {
	s, _ := ctx.Value(conflictNoteKey{}).(string)
	return s
}

// resolveConflictInPlace asks the step's still-live agent to resolve a
// conflicting push and pushes again. ok=false when the runtime cannot take
// a follow-up turn (the caller re-dispatches instead); the push's outputs
// and error otherwise.
func (r *Runner) resolveConflictInPlace(ctx context.Context, t core.Trigger, step config.Step, id, slot string, ref dispatch.RunRef, req dispatch.Request, pc pushConflict) (map[string]any, error, bool) {
	if ref.AgentID == "" || r.PushWork == nil {
		return nil, nil, false
	}
	if _, ok, err := r.followUp(ctx, t, step, id, slot, ref, pc.resolvePrompt()); !ok || err != nil {
		if err != nil {
			r.Log("%s step %s: resolve turn: %v", flowTag(t), id, err)
		}
		return nil, nil, false
	}
	r.Log("%s step %s: the branch moved and the agent's commits conflicted (%s) — the agent took a resolve turn; pushing again", flowTag(t), id, strings.Join(pc.files, ", "))
	r.audit(map[string]any{"event": "push_conflict", "repo": t.Target.Repo, "number": t.Target.Number, "kind": t.Kind,
		"step": id, "resolve": "follow_up", "files": pc.files, "kept_ref": pc.kept})
	pushed, perr := r.PushWork(ctx, req.DispatchID)
	return pushed, perr, true
}
