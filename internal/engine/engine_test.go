package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/store"
)

type fakeDispatcher struct {
	reqs      []dispatch.Request
	ref       dispatch.RunRef
	err       error
	liveAgent bool // HasLiveAgent return value
}

func (f *fakeDispatcher) Dispatch(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
	f.reqs = append(f.reqs, r)
	return f.ref, f.err
}

func (f *fakeDispatcher) WaitForAgent(context.Context, string, time.Duration) {}
func (f *fakeDispatcher) HasLiveAgent(context.Context, string, string) bool   { return f.liveAgent }

type fakeNotifier struct{ events []string }

func (f *fakeNotifier) Emit(_ context.Context, ev string, _ core.Trigger, _ string) {
	f.events = append(f.events, ev)
}

func (f *fakeNotifier) has(ev string) bool {
	for _, e := range f.events {
		if e == ev {
			return true
		}
	}
	return false
}

func ptrBool(b bool) *bool { return &b }

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(store.Options{
		StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a.jsonl"),
		TTL: 0, MaxPRs: 100, AuditMaxSize: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func baseCfg() *config.Config {
	c := &config.Config{Agents: map[string]config.AgentProfile{"fixer": {Provider: "claude"}}}
	c.Control.Enabled = ptrBool(true)
	return c
}

func newEng(t *testing.T, cfg *config.Config, d *fakeDispatcher, n *fakeNotifier, rerun func(context.Context, core.Trigger, int64)) (*Engine, *store.Store) {
	st := tempStore(t)
	e := New(Options{
		Config: cfg, Store: st, Dispatch: d, Notifier: n,
		Author:    dispatch.Author{Name: "Me"},
		UserToken: func() (string, error) { return "utok", nil },
		Rerun:     rerun,
	})
	return e, st
}

func agentTrigger(kind, repo string, num int, head, sig string, act config.Action) core.Trigger {
	return core.Trigger{
		Source: "github", Instance: "i", Kind: kind, Dedup: sig,
		Target:  core.Target{Repo: repo, PR: num, Number: num, HeadSHA: head},
		Context: map[string]any{"app_token": "atok"},
		Action:  act,
	}
}

func TestDispatchAndRecord(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"}
	tr := agentTrigger("changes_requested", "a/w", 1, "h1", "sig1", act)

	e.process(context.Background(), tr)

	if len(d.reqs) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(d.reqs))
	}
	if st.LastSignature("a/w#1", "changes_requested") != "sig1" {
		t.Fatal("signature not recorded")
	}
	if !n.has("dispatch") || !n.has("complete") {
		t.Fatalf("missing notifications: %v", n.events)
	}
	// Tokens threaded through.
	if d.reqs[0].Tokens.App != "atok" || d.reqs[0].Tokens.User != "utok" {
		t.Fatalf("tokens not threaded: %+v", d.reqs[0].Tokens)
	}
}

func TestDedupSkipsRepeat(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer"}
	tr := agentTrigger("merge_conflict", "a/w", 2, "h", "same", act)
	e.process(context.Background(), tr)
	e.process(context.Background(), tr)
	if len(d.reqs) != 1 {
		t.Fatalf("dedup failed: want 1 dispatch, got %d", len(d.reqs))
	}
}

func TestAttemptCapEscalates(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer", MaxAttemptsPerHead: 1}
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 3, "h", "s1", act))
	// Second distinct signature at same head → over the cap → escalate, no dispatch.
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 3, "h", "s2", act))
	if len(d.reqs) != 1 {
		t.Fatalf("want 1 dispatch (2nd escalated), got %d", len(d.reqs))
	}
	if !n.has("escalate") {
		t.Fatalf("expected escalation, events=%v", n.events)
	}
}

func TestReviewRequestedLivenessGate(t *testing.T) {
	act := config.Action{Type: "command", Command: []string{"critique"}}

	// A review agent is already parked/working for this PR → don't spawn another.
	dLive := &fakeDispatcher{liveAgent: true}
	eLive, _ := newEng(t, baseCfg(), dLive, &fakeNotifier{}, nil)
	eLive.process(context.Background(), agentTrigger("review_requested", "a/w", 10, "h", "reviewreq@h", act))
	if len(dLive.reqs) != 0 {
		t.Fatalf("live review agent should suppress re-dispatch, got %d", len(dLive.reqs))
	}

	// No live agent → dispatch, and (unlike other kinds) it must NOT record a
	// permanent dedup, so a still-pending review keeps coming back until done.
	d := &fakeDispatcher{}
	e, st := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	tr := agentTrigger("review_requested", "a/w", 11, "h", "reviewreq@h", act)
	e.process(context.Background(), tr)
	e.process(context.Background(), tr) // still pending → still dispatches
	if len(d.reqs) != 2 {
		t.Fatalf("review_requested should re-dispatch while pending, got %d", len(d.reqs))
	}
	if st.LastSignature("a/w#11", "review_requested") != "" {
		t.Fatal("review_requested must not consume permanent dedup")
	}
}

func TestFailedDispatchRetriesUntilCap(t *testing.T) {
	d := &fakeDispatcher{err: fmt.Errorf("WORKSPACE_CREATE_FAILED")}
	n := &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer", MaxAttemptsPerHead: 2}
	tr := agentTrigger("merge_conflict", "a/w", 20, "h", "sig", act)

	e.process(context.Background(), tr) // attempt 1 fails
	if st.LastSignature("a/w#20", "merge_conflict") != "" {
		t.Fatal("a failed dispatch must NOT consume the dedup signature")
	}
	e.process(context.Background(), tr) // attempt 2 fails (retried, not suppressed)
	if len(d.reqs) != 2 {
		t.Fatalf("failed dispatch should retry, got %d dispatches", len(d.reqs))
	}
	e.process(context.Background(), tr) // at cap now → escalate, no dispatch
	if len(d.reqs) != 2 {
		t.Fatalf("should stop dispatching at the attempt cap, got %d", len(d.reqs))
	}
	if !n.has("escalate") {
		t.Fatal("should escalate at the cap")
	}
}

func TestKillSwitch(t *testing.T) {
	cfg := baseCfg()
	cfg.Control.Enabled = ptrBool(false)
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, cfg, d, n, nil)
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 4, "h", "s", config.Action{Type: "agent", Agent: "fixer"}))
	if len(d.reqs) != 0 {
		t.Fatal("kill switch should block dispatch")
	}
}

func TestShadowPropagates(t *testing.T) {
	cfg := baseCfg()
	cfg.Control.Shadow = true
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, cfg, d, n, nil)
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 5, "h", "s", config.Action{Type: "agent", Agent: "fixer"}))
	if len(d.reqs) != 1 || !d.reqs[0].Shadow {
		t.Fatalf("shadow not propagated: %+v", d.reqs)
	}
	// Shadow is a preview: it must NOT consume the dedup signature, so a later
	// live run still acts.
	if st.LastSignature("a/w#5", "merge_conflict") != "" {
		t.Fatal("shadow should not record dedup")
	}
}

func TestClosedDeletesState(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 6, "h", "s", config.Action{Type: "agent", Agent: "fixer"}))
	if st.LastSignature("a/w#6", "merge_conflict") == "" {
		t.Fatal("precondition: expected recorded state")
	}
	e.process(context.Background(), core.Trigger{Source: "github", Kind: core.KindClosed,
		Target: core.Target{Repo: "a/w", PR: 6, Number: 6}})
	if st.LastSignature("a/w#6", "merge_conflict") != "" {
		t.Fatal("closed trigger should delete state")
	}
}

func TestDisabledActionSkipped(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer", Enabled: ptrBool(false)}
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 7, "h", "s", act))
	if len(d.reqs) != 0 {
		t.Fatal("disabled action should not dispatch")
	}
}

// gateFake blocks in WaitForAgent so a launched agent keeps holding its slot,
// letting a test observe the concurrency cap.
type gateFake struct {
	mu     sync.Mutex
	reqs   []dispatch.Request
	waitCh chan struct{}
}

func (g *gateFake) Dispatch(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, r)
	g.mu.Unlock()
	return dispatch.RunRef{Backend: "paseo", AgentID: "a-" + r.Trigger.Target.HeadSHA}, nil
}

func (g *gateFake) WaitForAgent(ctx context.Context, _ string, _ time.Duration) {
	select {
	case <-g.waitCh:
	case <-ctx.Done():
	}
}

func (g *gateFake) HasLiveAgent(context.Context, string, string) bool { return false }

func (g *gateFake) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reqs)
}

func TestConcurrencyCapBlocksSecondAgent(t *testing.T) {
	cfg := baseCfg()
	one := 1
	cfg.Control.MaxConcurrentAgents = &one // only one agent at a time
	g := &gateFake{waitCh: make(chan struct{})}
	e := New(Options{Config: cfg, Store: tempStore(t), Dispatch: g, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})
	act := config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"}

	// First agent takes the only slot; its WaitForAgent blocks, holding it.
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 1, "h1", "s1", act))
	if g.count() != 1 {
		t.Fatalf("first agent should dispatch, got %d", g.count())
	}

	// A second agent for a different PR must block on the cap, not dispatch.
	done := make(chan struct{})
	go func() {
		e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 2, "h2", "s2", act))
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	if g.count() != 1 {
		t.Fatalf("second agent should be blocked by the cap, got %d dispatches", g.count())
	}

	// Freeing the first agent's slot lets the second proceed.
	close(g.waitCh)
	<-done
	if g.count() != 2 {
		t.Fatalf("second agent should dispatch once a slot frees, got %d", g.count())
	}
}

func TestFlakyRerunBeforeDispatch(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	var reran []int64
	rerun := func(_ context.Context, _ core.Trigger, id int64) { reran = append(reran, id) }
	e, _ := newEng(t, baseCfg(), d, n, rerun)

	act := config.Action{Type: "agent", Agent: "fixer", FlakyRerun: config.FlakyRerun{Enabled: true, Max: 1}}
	tr := agentTrigger("failing_checks", "a/w", 8, "h", "fail@h", act)
	tr.Context["run_id"] = int64(555)

	// First failure: rerun once, no dispatch.
	e.process(context.Background(), tr)
	if len(reran) != 1 || reran[0] != 555 {
		t.Fatalf("expected one rerun of 555, got %v", reran)
	}
	if len(d.reqs) != 0 {
		t.Fatal("should not dispatch before rerun exhausted")
	}
	// Still failing after rerun: now dispatch the fix agent.
	e.process(context.Background(), tr)
	if len(d.reqs) != 1 {
		t.Fatalf("expected dispatch after rerun, got %d", len(d.reqs))
	}
}
