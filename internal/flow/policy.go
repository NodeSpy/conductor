package flow

import (
	"fmt"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// InQuietWindow reports whether now falls inside a quiet_hours window and,
// when it does, when the window ends (so held work can be re-queued then).
// The window may span midnight (from 22:00 to 07:00). A window missing from/
// to, or an unparsable time, is never quiet (fail-open: a typo must not
// silently hold all work).
func InQuietWindow(q *config.QuietHours, now time.Time) (bool, time.Time) {
	if q == nil || q.From == "" || q.To == "" {
		return false, time.Time{}
	}
	loc := time.Local
	if q.TZ != "" {
		if l, err := time.LoadLocation(q.TZ); err == nil {
			loc = l
		}
	}
	n := now.In(loc)
	from, ok1 := parseClock(q.From)
	to, ok2 := parseClock(q.To)
	if !ok1 || !ok2 {
		return false, time.Time{}
	}
	minutes := n.Hour()*60 + n.Minute()
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	if from <= to {
		// Same-day window (09:00–17:00).
		if minutes >= from && minutes < to {
			return true, day.Add(time.Duration(to) * time.Minute)
		}
		return false, time.Time{}
	}
	// Overnight window (22:00–07:00).
	if minutes >= from {
		return true, day.Add(24*time.Hour + time.Duration(to)*time.Minute)
	}
	if minutes < to {
		return true, day.Add(time.Duration(to) * time.Minute)
	}
	return false, time.Time{}
}

// parseClock parses "HH:MM" into minutes-since-midnight.
func parseClock(s string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
