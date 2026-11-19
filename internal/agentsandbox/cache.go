package agentsandbox

import "github.com/NodeSpy/conductor/internal/keycache"

// CacheBinding is a workspace's step cache (internal/keycache), recorded
// when its dispatch registers it: the step's rendered keys.
type CacheBinding struct {
	// Key is the step's own key: restored from, and the only key a run here
	// publishes to. RestoreFrom are read-only fallbacks, in order.
	Key         keycache.Key   `json:"key"`
	RestoreFrom []keycache.Key `json:"restore_from,omitempty"`
}
