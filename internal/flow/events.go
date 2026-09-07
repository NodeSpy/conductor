package flow

import (
	"sync"
	"time"
)

// Live run observability (#36 §17): every recorded run publishes lightweight
// events — run started/done, step started/done, gate rounds — to an in-
// process hub the daemon's control socket streams from (`conductor watch`).
// Events piggyback on the history recorder, so what you can watch live is
// exactly what the run record persists; shadow/dry runs emit nothing.

// RunEvent is one observability event.
type RunEvent struct {
	TS   time.Time `json:"ts"`
	Type string    `json:"type"` // run_started | step_started | step_done | gate | run_done
	// Run is the history id (`conductor runs <id>`); RunID the workflow-run
	// id (kind:key).
	Run    string `json:"run"`
	RunID  string `json:"run_id,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Repo   string `json:"repo,omitempty"`
	Number int    `json:"number,omitempty"`
	Step   string `json:"step,omitempty"`
	// Status: running | ok | failed | skipped (steps/runs); pass | fail |
	// revise | escalated (gate rounds).
	Status     string `json:"status,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// EventHub broadcasts run events to subscribers. Slow subscribers lose
// events rather than stalling runs (each subscription is a bounded buffer;
// the run record is the lossless source of truth).
type EventHub struct {
	mu   sync.Mutex
	subs map[int]*eventSub
	next int
}

type eventSub struct {
	ch  chan RunEvent
	run string // filter: history id or run id ("" = everything)
}

// NewEventHub builds an empty hub.
func NewEventHub() *EventHub {
	return &EventHub{subs: map[int]*eventSub{}}
}

// Publish fans an event out to matching subscribers (never blocks).
func (h *EventHub) Publish(ev RunEvent) {
	if h == nil {
		return
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		if s.run != "" && s.run != ev.Run && s.run != ev.RunID {
			continue
		}
		select {
		case s.ch <- ev:
		default: // full — drop for this subscriber
		}
	}
}

// Subscribe returns a channel of events matching runFilter (a history id, a
// workflow-run id, or "" for everything) and a cancel that closes it.
func (h *EventHub) Subscribe(runFilter string) (<-chan RunEvent, func()) {
	if h == nil {
		ch := make(chan RunEvent)
		close(ch)
		return ch, func() {}
	}
	h.mu.Lock()
	id := h.next
	h.next++
	s := &eventSub{ch: make(chan RunEvent, 256), run: runFilter}
	h.subs[id] = s
	h.mu.Unlock()
	return s.ch, func() {
		h.mu.Lock()
		if cur, ok := h.subs[id]; ok && cur == s {
			delete(h.subs, id)
			close(s.ch)
		}
		h.mu.Unlock()
	}
}

// emit publishes one event stamped with this run's identity.
func (h *histRec) emit(typ, step, status, detail string, durMS int64) {
	if h == nil || h.r.Events == nil {
		return
	}
	h.mu.Lock()
	ev := RunEvent{
		Type: typ, Run: h.rec.ID, RunID: h.rec.RunID, Kind: h.rec.Kind,
		Repo: h.rec.Repo, Number: h.rec.Number,
		Step: step, Status: status, Detail: detail, DurationMS: durMS,
	}
	h.mu.Unlock()
	h.r.Events.Publish(ev)
}
