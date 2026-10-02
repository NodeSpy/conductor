package connector

import (
	"context"

	"github.com/NodeSpy/conductor/internal/core"
)

// HeadReader is the optional face of a connector whose targets have a
// current head revision — a pull request's head commit. The flow runner reads
// it to give a run's hooks their run facts ({{.run.start_sha}},
// {{.run.head_sha}}, {{.run.pushed}}): the head when the run started, and the
// head when it ended. It reads only; what a hook does with the facts is the
// hook's business.
type HeadReader interface {
	// TargetHead returns the target's head as it is NOW (never a cached
	// copy: a caller compares heads across a push), with its state; a zero
	// value when the target has none this connector can name.
	TargetHead(ctx context.Context, t core.Trigger) (TargetHead, error)
}

// TargetHead is a target's current revision and state.
type TargetHead struct {
	SHA   string
	State string // TargetOpen | TargetClosed | TargetMerged | "" (unknown)
}

// Target states a HeadReader reports.
const (
	TargetOpen   = "open"
	TargetClosed = "closed"
	TargetMerged = "merged"
)

// TargetHead reads the trigger target's current head through this instance,
// when its connector has a head face. A zero value when it has none.
func (in *Instance) TargetHead(ctx context.Context, t core.Trigger) (TargetHead, error) {
	if in == nil || !in.Enabled {
		return TargetHead{}, nil
	}
	hr, ok := in.Impl.(HeadReader)
	if !ok {
		return TargetHead{}, nil
	}
	return hr.TargetHead(ctx, t)
}
