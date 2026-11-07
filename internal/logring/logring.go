// Package logring is a small bounded in-memory ring of recent log lines — a
// generic capability (not tied to any tool, service or workflow), so a
// surface like the dashboard can show "what the process just logged" without
// depending on how this box runs it (journald, launchd, a redirected file).
// It holds text only: whoever writes to it (conductor's own logf) has
// already redacted secrets before the line gets here.
package logring

import (
	"sync"
	"time"
)

// Line is one captured log line with the time it was written.
type Line struct {
	Time time.Time
	Text string
}

// Ring is a fixed-capacity circular buffer of Lines, safe for concurrent use.
type Ring struct {
	mu   sync.Mutex
	buf  []Line
	next int
	n    int
}

// New returns a Ring holding at most capacity lines (capacity < 1 means 1).
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]Line, capacity)}
}

// Write appends one line, evicting the oldest once the ring is full.
func (r *Ring) Write(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = Line{Time: time.Now(), Text: text}
	r.next = (r.next + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// Lines returns every held line, oldest first.
func (r *Ring) Lines() []Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Line, 0, r.n)
	start := (r.next - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}
