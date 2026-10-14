package sourcekit

import (
	"context"
	"time"
)

// Poller runs a source's catch-up pass on a cadence and on demand: the
// scheduler a plugin's plugin.poll {mode: now} nudges. It runs once at start,
// then every Min — or, Adaptive, doubling from Min up to Max while passes
// find nothing, back to Min when one finds something or on Nudge (a
// delivery stream that dropped, an operator's `conductor sweep --now`).
type Poller struct {
	Min, Max time.Duration
	Adaptive bool
	nudge    chan struct{}
}

// NewPoller is a poller; max is only read when adaptive.
func NewPoller(min, max time.Duration, adaptive bool) *Poller {
	if max < min {
		max = min
	}
	return &Poller{Min: min, Max: max, Adaptive: adaptive, nudge: make(chan struct{}, 1)}
}

// Nudge asks for a pass now and resets an adaptive cadence. It never blocks;
// a nudge while one is pending is folded into it.
func (p *Poller) Nudge() {
	select {
	case p.nudge <- struct{}{}:
	default:
	}
}

// Run calls pass until ctx ends. pass reports whether it found anything.
func (p *Poller) Run(ctx context.Context, pass func(context.Context) (found bool)) {
	next := p.Min
	for {
		found := pass(ctx)
		if p.Adaptive {
			if found {
				next = p.Min
			} else if next *= 2; next > p.Max {
				next = p.Max
			}
		}
		t := time.NewTimer(next)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-p.nudge:
			t.Stop()
			next = p.Min
		case <-t.C:
		}
	}
}
