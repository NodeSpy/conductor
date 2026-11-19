package agentsandbox

import "github.com/NodeSpy/conductor/internal/keycache"

// CacheBinding is a workspace's step cache (internal/keycache), recorded
// when its dispatch registers it: the step's rendered keys.
type CacheBinding struct {
	// Key is the step's own key: restored from, and the only key a run here
	// publishes to. RestoreFrom are read-only fallbacks, in order.
	Key         keycache.Key   `json:"key"`
	RestoreFrom []keycache.Key `json:"restore_from,omitempty"`
	// Teach: a launch here learns the repository's git fetch dependencies
	// (sandbox/gitauth.go). Set only for a trusted checkout that started at
	// the repository's default branch head, by a step whose key another
	// step restores from — work whose output other runs already trust.
	Teach bool `json:"teach,omitempty"`
}
