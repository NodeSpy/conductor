package dispatch

import (
	"testing"
	"time"
)

func TestWithinStartupGrace(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	grace := 3 * time.Minute
	young := now.Add(-30 * time.Second) // 30s old
	old := now.Add(-10 * time.Minute)   // 10m old

	cases := []struct {
		name    string
		engaged bool
		created time.Time
		spare   bool
	}{
		// The bug: a fixer that engaged and finished quickly must NOT be held by the
		// age grace — it's done, reap it now.
		{"engaged + young → reap", true, young, false},
		{"engaged + old → reap", true, old, false},
		// A not-yet-engaged fresh agent is still spinning up → spare it.
		{"unengaged + young → spare", false, young, true},
		// An unengaged agent past the grace (hung at launch) → reap.
		{"unengaged + old → reap", false, old, false},
		// Unknown age (zero created) → don't spare (can't tell it's spinning up).
		{"zero created → reap", false, time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withinStartupGrace(c.engaged, c.created, now, grace); got != c.spare {
				t.Fatalf("withinStartupGrace(engaged=%v) = %v, want %v", c.engaged, got, c.spare)
			}
		})
	}
}
