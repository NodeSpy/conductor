package inbound

import (
	"hash/fnv"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
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

// numID hashes s to a stable non-negative int for use as a synthetic item
// number.
//
// COLLISION RESISTANCE IS THE POINT. Number is not cosmetic: it goes into
// core.Trigger.Key(), which keys dedup, session reuse, and outcome/engagement
// records. For a synthetic source the input to this hash is derived from the
// PAYLOAD — a webhook body, an RSS item id, a Slack message — so the sender
// chooses it.
//
// This was FNV-1a/32 masked to 31 bits. A targeted second preimage against a
// 31-bit hash is ~2^31 trials: seconds of laptop CPU. A sender could pick a
// payload whose Number equalled another event's and land on its key — dedup
// it away before it ran, or join its session.
//
// FNV is not a cryptographic hash and a determined attacker with the string
// construction in hand can do better than brute force against it. The width
// is what makes the easy attack infeasible; the real guarantee for
// cross-restart idempotency stays Trigger.Dedup, which carries the raw string
// and never collapses it.
//
// 52 bits, not 63: Number travels through map[string]any audit entries that
// are marshalled to JSON, where any consumer decoding into float64 (including
// every JavaScript reader) silently rounds above 2^53. Staying under that
// keeps the value EXACT everywhere it is written, and 2^52 trials for a
// targeted collision is already out of reach. Widening past 2^53 would buy
// unreachable margin and pay for it in values that do not survive a
// round-trip.
func numID(s string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum64() & maxSyntheticNumber)
}

// maxSyntheticNumber bounds a synthetic Number to 52 bits — see numID.
const maxSyntheticNumber = 1<<52 - 1

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
