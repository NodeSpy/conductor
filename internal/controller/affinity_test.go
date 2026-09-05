package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// affRunner is a thread-safe fake dispatch runner: every Dispatch spawns a
// new fake agent id; Archive records reaped ids.
type affRunner struct {
	mu       sync.Mutex
	n        int
	reqs     []dispatch.Request
	archived []string
}

func (r *affRunner) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	r.reqs = append(r.reqs, req)
	return dispatch.RunRef{Backend: "paseo", AgentID: fmt.Sprintf("agent-%d", r.n)}, nil
}
func (r *affRunner) WaitForAgent(context.Context, string, time.Duration) {}
func (r *affRunner) HasLiveAgent(context.Context, string, string) bool   { return false }
func (r *affRunner) Archive(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.archived = append(r.archived, id)
	return nil
}
func (r *affRunner) dispatches() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// affSender is a thread-safe fake `paseo send`: records follow-ups, can
// block (serialization tests) or fail (dead-session tests).
type affSender struct {
	mu       sync.Mutex
	sent     []string
	err      error
	block    chan struct{} // when set, Send waits on it
	inFlight int
	maxSeen  int
}

func (s *affSender) Send(_ context.Context, id, prompt string) error {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.maxSeen {
		s.maxSeen = s.inFlight
	}
	block := s.block
	err := s.err
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	s.mu.Lock()
	s.inFlight--
	if err == nil {
		s.sent = append(s.sent, id+"\x00"+prompt)
	}
	s.mu.Unlock()
	return err
}
func (s *affSender) sends() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// memAffStore is an in-memory AffinityStore (the restart test shares one
// across two Affinity instances, like the real affinity.json would).
type memAffStore struct {
	mu   sync.Mutex
	recs map[string]AffinityRef
}

func newMemAffStore() *memAffStore { return &memAffStore{recs: map[string]AffinityRef{}} }
func (s *memAffStore) PutAffinity(r AffinityRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[r.Agent+"\x00"+r.Key] = r
	return nil
}
func (s *memAffStore) DeleteAffinity(agent, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs, agent+"\x00"+key)
	return nil
}
func (s *memAffStore) Affinities() []AffinityRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AffinityRef, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	return out
}

// affReq builds a session-profile dispatch request for a PR event.
func affReq(agent, kind, repo string, pr int, spec *config.SessionSpec) dispatch.Request {
	return dispatch.Request{
		Trigger: core.Trigger{
			Source: "github", Instance: "gh", Kind: kind,
			Target: core.Target{Repo: repo, PR: pr, Number: pr},
		},
		Action:  config.Action{Type: "agent", Agent: agent, Prompt: "handle {{.kind}} on {{.repo}}#{{.pr}}"},
		Profile: config.AgentProfile{Provider: "claude", Session: spec},
	}
}

func affSpec() *config.SessionSpec {
	return &config.SessionSpec{Key: "{{.repo}}#{{.pr}}", EndOn: []string{"gh.pr_closed", "gh.merged"}}
}

// affRig wires an Affinity over the real built-in paseo controller (fake
// runner + sender) — the production resolution path.
type affRig struct {
	aff    *Affinity
	runner *affRunner
	sender *affSender
	store  *memAffStore
	holds  map[string]bool
	mu     sync.Mutex
	cfg    *config.Config
}

func newAffRig(t *testing.T, st *memAffStore, spec *config.SessionSpec) *affRig {
	t.Helper()
	if st == nil {
		st = newMemAffStore()
	}
	rig := &affRig{runner: &affRunner{}, sender: &affSender{}, store: st, holds: map[string]bool{}}
	rig.cfg = &config.Config{Agents: map[string]config.AgentProfile{
		"reviewer": {Provider: "claude", Session: spec, ArchiveWhenDone: true},
	}}
	reg := NewRegistry(nil, "", rig.runner, rig.sender)
	hold := func(id string) { rig.mu.Lock(); rig.holds[id] = true; rig.mu.Unlock() }
	release := func(id string) { rig.mu.Lock(); delete(rig.holds, id); rig.mu.Unlock() }
	rig.aff = NewAffinity(reg, st, rig.cfg, hold, release, nil)
	return rig
}

func (rig *affRig) dispatch(t *testing.T, req dispatch.Request) dispatch.RunRef {
	t.Helper()
	ref, handled, err := rig.aff.Dispatch(context.Background(), rig.runner, req)
	if err != nil {
		t.Fatalf("affinity dispatch: %v", err)
	}
	if !handled {
		t.Fatalf("affinity should own this dispatch: %+v", req.Profile.Session)
	}
	return ref
}

func (rig *affRig) held(id string) bool {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return rig.holds[id]
}

// TestAffinitySameKeySameSession: the first event spawns; later events for
// the same key — across DIFFERENT event kinds/triggers — reach the same
// session as follow-ups, never a second spawn.
func TestAffinitySameKeySameSession(t *testing.T) {
	rig := newAffRig(t, nil, affSpec())
	spec := affSpec()

	first := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec))
	if first.AgentID != "agent-1" || first.Queued {
		t.Fatalf("first event must spawn fresh: %+v", first)
	}
	if !rig.held("agent-1") {
		t.Fatal("bound session must be held from the reaper")
	}

	// A different event type, same key → the one session, as a follow-up.
	second := rig.dispatch(t, affReq("reviewer", "failing_checks", "o/r", 7, spec))
	if !second.Queued || second.AgentID != "agent-1" {
		t.Fatalf("same-key event must follow up on the bound session: %+v", second)
	}
	third := rig.dispatch(t, affReq("reviewer", "changes_requested", "o/r", 7, spec))
	if !third.Queued || third.AgentID != "agent-1" {
		t.Fatalf("third event: %+v", third)
	}
	if rig.runner.dispatches() != 1 {
		t.Fatalf("want exactly 1 spawn, got %d", rig.runner.dispatches())
	}
	sends := rig.sender.sends()
	if len(sends) != 2 {
		t.Fatalf("want 2 follow-ups, got %v", sends)
	}
	// The follow-up prompt is rendered against the follow-up's own trigger.
	if !strings.Contains(sends[0], "agent-1\x00handle failing_checks on o/r#7") {
		t.Fatalf("follow-up prompt: %q", sends[0])
	}
	if !rig.aff.Owns("agent-1") {
		t.Fatal("Owns must report the bound session")
	}
}

// TestAffinityDifferentKeysDifferentSessions: distinct key values spawn
// distinct sessions.
func TestAffinityDifferentKeysDifferentSessions(t *testing.T) {
	rig := newAffRig(t, nil, affSpec())
	spec := affSpec()
	a := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec))
	b := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 8, spec))
	c := rig.dispatch(t, affReq("reviewer", "new_comment", "x/y", 7, spec))
	if a.AgentID == b.AgentID || a.AgentID == c.AgentID || b.AgentID == c.AgentID {
		t.Fatalf("distinct keys must get distinct sessions: %s %s %s", a.AgentID, b.AgentID, c.AgentID)
	}
	if rig.runner.dispatches() != 3 {
		t.Fatalf("want 3 spawns, got %d", rig.runner.dispatches())
	}
}

// TestAffinitySerialization: concurrent same-key events queue — at most one
// prompt in flight per session.
func TestAffinitySerialization(t *testing.T) {
	rig := newAffRig(t, nil, affSpec())
	spec := affSpec()
	rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec)) // bind

	gate := make(chan struct{})
	rig.sender.mu.Lock()
	rig.sender.block = gate
	rig.sender.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rig.dispatch(t, affReq("reviewer", fmt.Sprintf("evt%d", i), "o/r", 7, spec))
		}(i)
	}
	// Let the first follow-up enter Send, then release everyone.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	rig.sender.mu.Lock()
	maxSeen := rig.sender.maxSeen
	sent := len(rig.sender.sent)
	rig.sender.mu.Unlock()
	if maxSeen != 1 {
		t.Fatalf("same-key prompts must serialize: max in flight %d", maxSeen)
	}
	if sent != 5 {
		t.Fatalf("all queued follow-ups must deliver: %d", sent)
	}
	if rig.runner.dispatches() != 1 {
		t.Fatalf("no duplicate spawn under concurrency: %d", rig.runner.dispatches())
	}
}

// TestAffinityRestartResume: a new Affinity over the same store (a restart)
// resumes the persisted binding — the next event is a follow-up to the SAME
// session id, and the hold is re-registered.
func TestAffinityRestartResume(t *testing.T) {
	st := newMemAffStore()
	spec := affSpec()
	rig1 := newAffRig(t, st, spec)
	first := rig1.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec))

	// "Restart": fresh process state, same persisted store.
	rig2 := newAffRig(t, st, spec)
	if !rig2.held(first.AgentID) {
		t.Fatal("restored binding must re-hold its agent")
	}
	second := rig2.dispatch(t, affReq("reviewer", "failing_checks", "o/r", 7, spec))
	if !second.Queued || second.AgentID != first.AgentID {
		t.Fatalf("post-restart event must resume the persisted session: %+v", second)
	}
	if rig2.runner.dispatches() != 0 {
		t.Fatalf("no fresh spawn after restart resume: %d", rig2.runner.dispatches())
	}
}

// TestAffinityIdleAndLifetimeEviction: idle_ttl and max_lifetime each evict;
// the next event starts fresh, the old agent is released and archived.
func TestAffinityIdleAndLifetimeEviction(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		spec    *config.SessionSpec
		advance time.Duration
	}{
		"idle_ttl":     {&config.SessionSpec{Key: "{{.repo}}#{{.pr}}", IdleTTL: config.Duration(time.Hour)}, 2 * time.Hour},
		"max_lifetime": {&config.SessionSpec{Key: "{{.repo}}#{{.pr}}", MaxLifetime: config.Duration(24 * time.Hour)}, 25 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newAffRig(t, nil, tc.spec)
			now := base
			rig.aff.SetClock(func() time.Time { return now })
			first := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, tc.spec))

			// Still fresh: a follow-up, no eviction.
			now = now.Add(time.Minute)
			if ref := rig.dispatch(t, affReq("reviewer", "evt", "o/r", 7, tc.spec)); !ref.Queued {
				t.Fatalf("within ttl must follow up: %+v", ref)
			}

			now = now.Add(tc.advance)
			next := rig.dispatch(t, affReq("reviewer", "evt2", "o/r", 7, tc.spec))
			if next.Queued || next.AgentID == first.AgentID {
				t.Fatalf("expired session must be replaced: %+v", next)
			}
			if rig.held(first.AgentID) {
				t.Fatal("evicted agent must be released from the hold set")
			}
			rig.runner.mu.Lock()
			archived := append([]string(nil), rig.runner.archived...)
			rig.runner.mu.Unlock()
			if len(archived) != 1 || archived[0] != first.AgentID {
				t.Fatalf("archive_when_done profile: evicted agent must be archived: %v", archived)
			}
		})
	}
}

// TestAffinitySweepEvictsExpired: the background sweep path (EvictExpired)
// reaps without needing a next event.
func TestAffinitySweepEvictsExpired(t *testing.T) {
	spec := &config.SessionSpec{Key: "{{.repo}}#{{.pr}}", IdleTTL: config.Duration(time.Hour)}
	rig := newAffRig(t, nil, spec)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	rig.aff.SetClock(func() time.Time { return now })
	first := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec))
	if n := rig.aff.EvictExpired(context.Background()); n != 0 {
		t.Fatalf("nothing expired yet: %d", n)
	}
	now = now.Add(2 * time.Hour)
	if n := rig.aff.EvictExpired(context.Background()); n != 1 {
		t.Fatalf("want 1 eviction, got %d", n)
	}
	if rig.aff.Owns(first.AgentID) || rig.held(first.AgentID) {
		t.Fatal("sweep must fully unbind the session")
	}
	if len(rig.store.Affinities()) != 0 {
		t.Fatal("sweep must drop the persisted ref")
	}
}

// TestAffinityEndOnEviction: a lifecycle event named in end_on evicts exactly
// the binding whose key the event renders.
func TestAffinityEndOnEviction(t *testing.T) {
	spec := affSpec()
	rig := newAffRig(t, nil, spec)
	a := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec))
	b := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 8, spec))

	// pr_closed for PR 7 (matched via instance "gh") ends only that session.
	rig.aff.ObserveEvent(context.Background(), core.Trigger{
		Source: "github", Instance: "gh", Kind: "pr_closed",
		Target: core.Target{Repo: "o/r", PR: 7, Number: 7},
	})
	if rig.aff.Owns(a.AgentID) {
		t.Fatal("end_on event must evict the matching session")
	}
	if !rig.aff.Owns(b.AgentID) {
		t.Fatal("other keys must be untouched")
	}
	// An unrelated kind is ignored.
	rig.aff.ObserveEvent(context.Background(), core.Trigger{
		Source: "github", Instance: "gh", Kind: "new_comment",
		Target: core.Target{Repo: "o/r", PR: 8, Number: 8},
	})
	if !rig.aff.Owns(b.AgentID) {
		t.Fatal("non-end_on events must not evict")
	}
	// The next PR-7 event starts fresh.
	next := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec))
	if next.Queued || next.AgentID == a.AgentID {
		t.Fatalf("after end_on the next event must spawn fresh: %+v", next)
	}
}

// TestAffinityFollowupFailureFallsBack: a dead session (send fails) is
// evicted and the event spawns a fresh session instead of erroring.
func TestAffinityFollowupFailureFallsBack(t *testing.T) {
	spec := affSpec()
	rig := newAffRig(t, nil, spec)
	first := rig.dispatch(t, affReq("reviewer", "new_comment", "o/r", 7, spec))

	rig.sender.mu.Lock()
	rig.sender.err = fmt.Errorf("agent archived")
	rig.sender.mu.Unlock()
	second := rig.dispatch(t, affReq("reviewer", "evt", "o/r", 7, spec))
	if second.Queued || second.AgentID == first.AgentID {
		t.Fatalf("dead session must be replaced by a fresh spawn: %+v", second)
	}
	if rig.aff.Owns(first.AgentID) {
		t.Fatal("dead session must be unbound")
	}
	// The replacement works for follow-ups again.
	rig.sender.mu.Lock()
	rig.sender.err = nil
	rig.sender.mu.Unlock()
	third := rig.dispatch(t, affReq("reviewer", "evt2", "o/r", 7, spec))
	if !third.Queued || third.AgentID != second.AgentID {
		t.Fatalf("replacement session must serve follow-ups: %+v", third)
	}
}

// TestAffinityNotHandled: no session spec, a non-persistent runtime (paseo
// without a sender), and shadow dispatches all fall through to the plain
// path.
func TestAffinityNotHandled(t *testing.T) {
	spec := affSpec()

	// No session spec.
	rig := newAffRig(t, nil, spec)
	req := affReq("reviewer", "new_comment", "o/r", 7, nil)
	if _, handled, _ := rig.aff.Dispatch(context.Background(), rig.runner, req); handled {
		t.Fatal("no session spec must not be handled")
	}

	// Non-persistent runtime: built-in paseo with NO follow-up sender.
	runner := &affRunner{}
	reg := NewRegistry(nil, "", runner, nil)
	cfg := &config.Config{Agents: map[string]config.AgentProfile{"reviewer": {Session: spec}}}
	aff := NewAffinity(reg, newMemAffStore(), cfg, nil, nil, nil)
	req = affReq("reviewer", "new_comment", "o/r", 7, spec)
	if _, handled, _ := aff.Dispatch(context.Background(), runner, req); handled {
		t.Fatal("a runtime without session persistence must fall back to fresh dispatch")
	}

	// Shadow.
	req = affReq("reviewer", "new_comment", "o/r", 7, spec)
	req.Shadow = true
	if _, handled, _ := rig.aff.Dispatch(context.Background(), rig.runner, req); handled {
		t.Fatal("shadow must not touch the session registry")
	}

	// A key that renders empty is a clear dispatch error.
	req = affReq("reviewer", "new_comment", "", 0, &config.SessionSpec{Key: "{{.repo}}"})
	req.Profile.Session = &config.SessionSpec{Key: "{{.repo}}"}
	if _, handled, err := rig.aff.Dispatch(context.Background(), rig.runner, req); !handled || err == nil {
		t.Fatalf("empty key must error: handled=%v err=%v", handled, err)
	}
}
