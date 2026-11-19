package dispatch

import (
	"context"

	"github.com/NodeSpy/conductor/internal/keycache"
)

// CacheKeys are a dispatch's cache keys, rendered from its step's
// `cache:` (config.StepCache) over the step's facts.
type CacheKeys struct {
	// Key is the step's own key: restored from, and the only key the run
	// publishes to.
	Key keycache.Key `json:"key"`
	// RestoreFrom are read-only fallbacks, in order.
	RestoreFrom []keycache.Key `json:"restore_from,omitempty"`
	// Teach: the step declared cache: { teach: true } — a default-branch
	// run of it may teach its repository's git fetch dependencies
	// (agentsandbox.CacheBinding.Teach).
	Teach bool `json:"teach,omitempty"`
}

// CacheFinisher ends a dispatch's caching at its step's end (keycache
// Manager.Finish): ok publishes what the run filled to its key; either way
// its layers go. nil: caches are off. Wired at boot.
var CacheFinisher func(ctx context.Context, dispatchID string, ok bool)

// FinishCache ends req's caching (CacheFinisher), if it has any.
func FinishCache(ctx context.Context, req Request, ok bool) {
	if f := CacheFinisher; f != nil && req.Cache != nil && req.DispatchID != "" {
		f(ctx, req.DispatchID, ok)
	}
}
