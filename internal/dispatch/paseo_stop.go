package dispatch

import (
	"context"
	"errors"
	"time"
)

// A run bound to its target is cancelled, with cause ErrTargetClosed, when
// the target closes. For a foreground `paseo run` that ends the launch
// conductor waits on, not the agent: it lives in the paseo daemon and would
// go on working on a dead target, in a workspace nothing reclaims (a
// cancelled launch reads as a shutdown, which keeps both for the restart).
// So the agent is archived here, with the workspace conductor made for it.
//
// `paseo run` reports the agent's id only when its turn ends, so the agent is
// named at launch by a label only this dispatch knows, and found by it.

// boundLabel names the agent a foreground dispatch launched for a run bound
// to its target.
const boundLabel = "conductor-bound"

// boundPoll is how long a stopped dispatch waits between looks for its agent
// (a launch cut short may register it a moment later), and boundLooks how
// many times it looks; vars so tests can shorten them.
var (
	boundPoll  = time.Second
	boundLooks = 3
)

// targetClosed reports whether ctx's run was stopped because its target
// closed.
func targetClosed(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrTargetClosed)
}

// archiveStopped archives the agents of a dispatch stopped because its target
// closed: those labels find, and ids. It reports whether it found any.
//
// The agents are conductor's own: the label is a token this dispatch minted
// for its launch (or the session token the run recorded for the agent it
// resumes), and ids are what the launch itself reported. They enter the
// ownership ledger here, since the launch that would have recorded them was
// cut short, and are archived through Archive like any other. It runs past
// the dispatch's cancellation.
func (d *Dispatcher) archiveStopped(ctx context.Context, labels map[string]string, ids ...string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	seen := map[string]bool{}
	found := false
	for look := 0; look < boundLooks; look++ {
		if look > 0 {
			if found {
				break
			}
			select {
			case <-ctx.Done():
				return found
			case <-time.After(boundPoll):
			}
		}
		if agents, err := d.backend().ListAgents(ctx, labels); err == nil {
			for _, a := range agents {
				ids = append(ids, a.ID)
			}
		}
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			seen[id], found = true, true
			d.Owned.AddAgent(id)
			if err := d.Archive(ctx, id); err != nil {
				d.logf("stopping agent %s (its target closed): %v", id, err)
			}
		}
	}
	return found
}
