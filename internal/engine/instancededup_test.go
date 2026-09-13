package engine

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/store"
)

// TRIGGER INSTANCES MUST NOT COLLIDE (docs/design/config-surface-refinements.md
// §6). Two instances of one pack trigger are two separate armings — different
// repos, different gates — so they must keep separate dedup state. Sharing it
// would mean instance 0 handling an event SUPPRESSES instance 1's handling of
// the same event, silently, and only for events both instances match.
//
// The chain: a trigger spec's Name becomes core.Trigger.Variant, and the
// engine keys dedup on `kind#variant`. Distinct instance names therefore give
// distinct dedup state — this pins that the chain actually holds, rather than
// trusting that the names differ.
func TestTriggerInstancesKeepSeparateDedupState(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Options{StatePath: dir + "/s.json", AuditPath: dir + "/a.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	// The two instances as the instantiator names them.
	a := core.Trigger{
		Source: "github", Instance: "gh", Kind: "push",
		Variant:       config.InstanceName("review/deploy", "0"),
		TargetTrusted: true,
		Target:        core.Target{Repo: "team/app", Number: 7},
		Dedup:         "same-event-signature",
	}
	b := a
	b.Variant = config.InstanceName("review/deploy", "1")

	dkindOf := func(t core.Trigger) string {
		if t.Variant == "" {
			return t.Kind
		}
		return t.Kind + "#" + t.Variant
	}
	if dkindOf(a) == dkindOf(b) {
		t.Fatalf("two instances share a dedup kind (%q) — one's handling would "+
			"suppress the other's", dkindOf(a))
	}

	key := a.Key()
	// Instance 0 handles the event.
	if err := st.Record(key, dkindOf(a), a.Dedup, ""); err != nil {
		t.Fatal(err)
	}
	if got := st.LastSignature(key, dkindOf(a)); got != a.Dedup {
		t.Fatalf("instance 0's own state: %q", got)
	}
	// Instance 1 must NOT see it as already handled.
	if got := st.LastSignature(key, dkindOf(b)); got == b.Dedup {
		t.Fatal("instance 1 sees instance 0's dedup record — the two armings would " +
			"suppress each other on every event they both match")
	}
	// …and recording instance 1 does not disturb instance 0.
	if err := st.Record(key, dkindOf(b), b.Dedup, ""); err != nil {
		t.Fatal(err)
	}
	if got := st.LastSignature(key, dkindOf(a)); got != a.Dedup {
		t.Fatalf("instance 1's record clobbered instance 0's: %q", got)
	}
}
