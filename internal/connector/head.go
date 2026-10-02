package connector

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/core"
)

// TargetHead is a target's current revision and state, read through the verb
// its connector declares `reads_revision` (plugin-contract.md §2.3). The flow
// runner reads it for a run's hooks ({{.run.start_sha}}, {{.run.head_sha}},
// {{.run.pushed}}) and for a stop's reason. It reads only; what a hook does
// with the facts is the hook's business.
type TargetHead struct {
	SHA   string
	State string // TargetOpen | TargetClosed | TargetAccepted | "" (unknown)
	// StopReason is what a run stopped because the target went away
	// reports, in the connector's own words for this state.
	StopReason string
}

// Target states, in the contract's generic terms.
const (
	TargetOpen     = "open"
	TargetClosed   = "closed"
	TargetAccepted = "accepted"
)

// TargetHead reads the trigger target's current revision through this
// instance's reads_revision verb: never a cached copy (a caller compares
// heads across a push). A zero value when the connector declares no such
// verb, when the target is not one this instance emitted, or when the
// platform did not assign it (the read spends the instance's credentials on
// whatever target it names).
func (in *Instance) TargetHead(ctx context.Context, t core.Trigger) (TargetHead, error) {
	if in == nil || !in.Enabled || in.Impl == nil || t.Instance != in.Name || !t.TargetTrusted {
		return TargetHead{}, nil
	}
	for _, v := range in.Decl.Verbs {
		if v.Semantics == nil || v.Semantics.ReadsRevision == nil {
			continue
		}
		rr := v.Semantics.ReadsRevision
		out, err := in.InvokeFinal(ctx, v.Name, core.DeclaredArgs(rr.Args, t.Facts()))
		if err != nil {
			return TargetHead{}, err
		}
		h := TargetHead{SHA: fmt.Sprint(out[rr.Revision])}
		if h.SHA == "<nil>" {
			h.SHA = ""
		}
		if rr.State != "" {
			got := fmt.Sprint(out[rr.State])
			for generic, spellings := range rr.States {
				for _, sp := range spellings {
					if sp == got {
						h.State = generic
					}
				}
			}
		}
		h.StopReason = rr.Reasons[h.State]
		if h.StopReason == "" && h.State != TargetAccepted {
			h.StopReason = rr.Reasons[TargetClosed]
		}
		return h, nil
	}
	return TargetHead{}, nil
}
