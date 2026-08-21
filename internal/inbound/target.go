package inbound

import (
	"hash/fnv"
	"sync"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// SyntheticTarget builds a Target for a source that has no real GitHub repo. The
// engine's dedup key is Target.Repo+"#"+Number (core.Trigger.Key); everything under
// one instance collapses into a single dedup Record when Repo is empty, so give each
// logical entity its own key: a synthetic logicalRepo (e.g. "rss:changelog",
// "slack:C123") plus a stable per-item Number derived from dedup. Cross-restart
// idempotency then comes from the engine store via Trigger.Dedup.
func SyntheticTarget(logicalRepo, dedup string) core.Target {
	return core.Target{Repo: logicalRepo, Number: numID(dedup)}
}

// numID hashes s to a stable non-negative int for use as a synthetic item number.
func numID(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() & 0x7fffffff)
}

// ForceNoCheckout returns a copy of a with Checkout defaulted to "none". Dispatch
// treats any non-empty Target.Repo as clonable (defaults to branch-off), so an
// action bound to a synthetic repo must run without a git checkout. An explicit
// Checkout on the action is respected.
func ForceNoCheckout(a config.Action) config.Action {
	if a.Checkout == "" {
		a.Checkout = "none"
	}
	return a
}

// DeliveryDedup is a bounded set of recently-seen delivery ids, for suppressing
// duplicate deliveries (smee reconnect redelivery, retried POSTs) before emitting.
// Copied from the github integration's deliveryDedup.
type DeliveryDedup struct {
	mu   sync.Mutex
	max  int
	set  map[string]struct{}
	ring []string
}

func NewDeliveryDedup(max int) *DeliveryDedup {
	if max <= 0 {
		max = 2048
	}
	return &DeliveryDedup{max: max, set: map[string]struct{}{}}
}

// Add records id and reports true if it was new.
func (d *DeliveryDedup) Add(id string) bool {
	if id == "" {
		return true // no id to dedup on — treat as new
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.set[id]; ok {
		return false
	}
	d.set[id] = struct{}{}
	d.ring = append(d.ring, id)
	if len(d.ring) > d.max {
		old := d.ring[0]
		d.ring = d.ring[1:]
		delete(d.set, old)
	}
	return true
}
