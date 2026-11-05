package flow

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// A finished foreground agent step releases its agent — and with it the
// worktree conductor made for it — at the step boundary, unless the step
// says archive_when_done: false. Without that, every lens of a for_each
// review stayed live until the daemon restarted, and so did its worktree
// (the reaper never removes a live session's).
//
// The default applies to agent steps the run executes as steps (execStep),
// not to the sessions a team, a decide or a gate runs through execAgent
// themselves: a team's reconciler reads its workers' worktrees after they
// return. And a step whose workdir or agent_id something later in the run
// reads keeps its agent, as before: `git -C {{.fix.workdir}} push` after an
// approval needs the worktree still there.

// stepReleaseKey marks the ctx execStep hands execAgent for a step of the
// run itself; execAgent clears it before anything it runs in turn.
type stepReleaseKey struct{}

// laterReadsKey carries what the run's templates read of a step's agent
// (laterReads).
type laterReadsKey struct{}

// laterReads is which step ids the run's templates read the workdir or
// agent_id of; all: some read cannot be tied to one step (a range over a
// for_each's items, say), so no step's agent is released by default.
type laterReads struct {
	ids map[string]bool
	all bool
}

// agentFieldRef finds a template path ending in .workdir or .agent_id.
var agentFieldRef = regexp.MustCompile(`((?:\.[A-Za-z_][\w-]*)*)\.(workdir|agent_id)\b`)

// scanLaterReads adds what text reads of steps' workdir/agent_id to into.
func scanLaterReads(into *laterReads, text string) {
	for _, m := range agentFieldRef.FindAllStringSubmatch(text, -1) {
		segs := strings.Split(strings.TrimPrefix(m[1], "."), ".")
		if m[1] == "" {
			segs = nil
		}
		if len(segs) > 0 && segs[0] == "steps" {
			segs = segs[1:]
		}
		if len(segs) == 2 && segs[1] == "outputs" {
			segs = segs[:1]
		}
		switch {
		case len(segs) == 1 && segs[0] == "gate":
			// The gate's own scope: the change under check, still live.
		case len(segs) == 1:
			into.ids[segs[0]] = true
		default:
			into.all = true
		}
	}
}

// withLaterReads adds what v (steps, hooks: anything carrying templates)
// reads of its steps' agents to ctx's set.
func withLaterReads(ctx context.Context, v any) context.Context {
	reads := laterReads{ids: map[string]bool{}}
	if prev, ok := ctx.Value(laterReadsKey{}).(laterReads); ok {
		reads.all = prev.all
		for id := range prev.ids {
			reads.ids[id] = true
		}
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		reads.all = true
	} else {
		scanLaterReads(&reads, string(b))
	}
	return context.WithValue(ctx, laterReadsKey{}, reads)
}

// releasesByDefault reports whether execAgent releases this step's agent at
// a clean step end though the step left archive_when_done unset. id is the
// step's output key ("review[3]" for a for_each item reads as "review").
func releasesByDefault(ctx context.Context, step config.Step, id string) bool {
	if step.ArchiveWhenDone != nil || step.Background || step.Handoff != "" ||
		step.Session != nil || step.Watch != nil || step.IdleTimeout > 0 {
		return false
	}
	if on, _ := ctx.Value(stepReleaseKey{}).(bool); !on {
		return false
	}
	reads, _ := ctx.Value(laterReadsKey{}).(laterReads)
	if reads.all {
		return false
	}
	base, _, _ := strings.Cut(id, "[")
	return !reads.ids[base] && !reads.ids[step.ID]
}

// A step whose agent a later step reads (its workdir, its agent_id) keeps
// that agent live past the step — and, when the step leaves
// archive_when_done unset, no longer than the run: it is released when the
// run ends, however it ends. A failed step keeps its agent as before, unless
// the run continued past it (continue_on_error) for later steps to read its
// workspace: then it too goes when the run ends.

// runEndReleaseKey carries the run's agents to release when it ends.
type runEndReleaseKey struct{}

type runEndRelease struct {
	mu  sync.Mutex
	ids []string
}

func withRunEndRelease(ctx context.Context) context.Context {
	return context.WithValue(ctx, runEndReleaseKey{}, &runEndRelease{})
}

// releasesAtRunEnd reports whether a step's agent, kept live past the step
// only because a later step reads it, is released when the run ends.
func releasesAtRunEnd(ctx context.Context, step config.Step, id string) bool {
	if step.ArchiveWhenDone != nil || step.Background || step.Handoff != "" ||
		step.Session != nil || step.Watch != nil || step.IdleTimeout > 0 {
		return false
	}
	if on, _ := ctx.Value(stepReleaseKey{}).(bool); !on {
		return false
	}
	return !releasesByDefault(ctx, step, id)
}

// deferRelease releases agentID when the run ends.
func deferRelease(ctx context.Context, agentID string) {
	if rr, ok := ctx.Value(runEndReleaseKey{}).(*runEndRelease); ok {
		rr.mu.Lock()
		rr.ids = append(rr.ids, agentID)
		rr.mu.Unlock()
	}
}

// releaseAtRunEnd archives the agents deferRelease kept for the run.
func (r *Runner) releaseAtRunEnd(ctx context.Context) {
	rr, ok := ctx.Value(runEndReleaseKey{}).(*runEndRelease)
	if !ok || r.Agents.Archive == nil {
		return
	}
	rr.mu.Lock()
	ids := rr.ids
	rr.ids = nil
	rr.mu.Unlock()
	for _, id := range ids {
		r.Agents.Archive(id)
	}
}
