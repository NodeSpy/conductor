package dashboard

import (
	"context"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
)

// ringSize is how many recent events the hub keeps for Last-Event-ID resume
// (design §5.2: "a ring of 10k events"). A reconnect whose last id predates the
// ring gets a reset instead of a partial replay.
const ringSize = 10000

// hub fans conductor's live run events out to SSE subscribers. It bridges the
// daemon's flow.EventHub (lossy, id-less, run-filtered) to the dashboard's
// contract: every event gets a monotonically increasing id and a topic, recent
// events are retained in a ring so a dropped stream can resume, and each
// subscriber receives only the topics it asked for.
//
// It is the one place run events are mapped to api.Event, so the SSE handler
// stays a transport shell.
type hub struct {
	mu     sync.Mutex
	nextID int64
	ring   []api.Event // newest at the end; capped at ringSize
	subs   map[int]*subscriber
	nextNo int
}

type subscriber struct {
	ch     chan api.Event
	topics map[api.EventTopic]bool
}

func newHub() *hub {
	return &hub{subs: map[int]*subscriber{}}
}

// run bridges the flow hub into this hub until ctx is cancelled. Each
// flow.RunEvent becomes a TopicRun event; a failed run_done also becomes a
// TopicAttention event (the slice's stand-in for the attention aggregator).
func (h *hub) run(ctx context.Context, fh *flow.EventHub) {
	ch, cancel := fh.Subscribe("") // "" = every run
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data := api.RunEventData{
				Type: ev.Type, Run: ev.Run, RunID: ev.RunID, Kind: ev.Kind,
				Repo: ev.Repo, Number: ev.Number, Step: ev.Step,
				Status: ev.Status, Detail: ev.Detail, DurationMS: ev.DurationMS,
			}
			ts := ev.TS
			if ts.IsZero() {
				ts = time.Now()
			}
			h.publish(api.TopicRun, ev.Type, ts, data)
			if ev.Type == "run_done" && ev.Status == "failed" {
				h.publish(api.TopicAttention, "attention", ts, data)
			}
		}
	}
}

// publish stamps an event with the next id, retains it in the ring, and fans it
// out to every subscriber of its topic (never blocking: a full subscriber drops
// the event, exactly as the underlying flow hub does).
func (h *hub) publish(topic api.EventTopic, typ string, ts time.Time, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	ev := api.Event{ID: h.nextID, Topic: topic, Type: typ, TS: ts.UTC().Format(time.RFC3339Nano), Data: data}
	h.ring = append(h.ring, ev)
	if len(h.ring) > ringSize {
		h.ring = h.ring[len(h.ring)-ringSize:]
	}
	for _, s := range h.subs {
		if !s.topics[topic] {
			continue
		}
		select {
		case s.ch <- ev:
		default:
		}
	}
}

// subscribe registers a subscriber for the given topics and returns its channel,
// the events it missed since lastID (replayed in order), whether the gap was too
// large to replay (the caller should emit a reset), and a cancel func.
//
// Holding the lock across replay + registration guarantees no event is both
// replayed and delivered live (no duplicate, no gap at the seam).
func (h *hub) subscribe(topics []api.EventTopic, lastID int64) (<-chan api.Event, []api.Event, bool, func()) {
	want := map[api.EventTopic]bool{}
	for _, t := range topics {
		want[t] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	var missed []api.Event
	reset := false
	if lastID > 0 {
		oldest := int64(0)
		if len(h.ring) > 0 {
			oldest = h.ring[0].ID
		}
		// The client last saw lastID. If the ring no longer reaches back to the
		// event right after it, we can't prove continuity → reset.
		if lastID+1 < oldest {
			reset = true
		} else {
			for _, ev := range h.ring {
				if ev.ID > lastID && want[ev.Topic] {
					missed = append(missed, ev)
				}
			}
		}
	}

	s := &subscriber{ch: make(chan api.Event, 256), topics: want}
	no := h.nextNo
	h.nextNo++
	h.subs[no] = s
	cancel := func() {
		h.mu.Lock()
		if cur, ok := h.subs[no]; ok && cur == s {
			delete(h.subs, no)
			close(s.ch)
		}
		h.mu.Unlock()
	}
	return s.ch, missed, reset, cancel
}

// lastID returns the id of the most recent event (0 when none), for tests and
// diagnostics.
func (h *hub) lastID() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nextID
}
