package plugin

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// host.log is a plugin writing into the daemon's own log, so the host bounds
// it the way it bounds host.state and host.auth: a line is capped in size,
// its control characters are escaped (a plugin can't start a new log line,
// forge a daemon-looking one, or move the terminal cursor), and each
// instance gets a fixed budget of lines per window. Lines past the budget
// are dropped and counted; the count is logged once the window turns over.
const (
	hostLogMaxBytes = 2048
	hostLogBurst    = 60
)

var hostLogWindow = time.Minute

type hostLogLimiter struct {
	mu   sync.Mutex
	inst map[string]*hostLogBucket
}

type hostLogBucket struct {
	start   time.Time
	n       int
	dropped int
}

// admit reports whether instance may log one more line now, and how many
// lines it dropped in the window that just ended (to report once).
func (l *hostLogLimiter) admit(instance string, now time.Time) (ok bool, droppedBefore int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inst == nil {
		l.inst = map[string]*hostLogBucket{}
	}
	b := l.inst[instance]
	if b == nil {
		b = &hostLogBucket{start: now}
		l.inst[instance] = b
	}
	if now.Sub(b.start) >= hostLogWindow {
		droppedBefore = b.dropped
		*b = hostLogBucket{start: now}
	}
	if b.n >= hostLogBurst {
		b.dropped++
		return false, droppedBefore
	}
	b.n++
	return true, droppedBefore
}

// sanitizeHostLog caps a plugin's log message and escapes every control
// character (newlines, ESC, DEL, C1) so it stays one inert line.
func sanitizeHostLog(msg string) string {
	truncated := false
	if len(msg) > hostLogMaxBytes {
		msg = msg[:hostLogMaxBytes]
		for !utf8.ValidString(msg) && len(msg) > 0 {
			msg = msg[:len(msg)-1]
		}
		truncated = true
	}
	var b strings.Builder
	for _, r := range msg {
		switch {
		case r == utf8.RuneError:
			b.WriteString(`�`)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	if truncated {
		b.WriteString(" …(truncated)")
	}
	return b.String()
}
