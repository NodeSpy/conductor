package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/store"
)

type fakeDispatcher struct {
	reqs []dispatch.Request
	ref  dispatch.RunRef
	err  error
}

func (f *fakeDispatcher) Dispatch(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
	f.reqs = append(f.reqs, r)
	return f.ref, f.err
}

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
	e, _ := newEng(t, cfg, d, n, nil)
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 5, "h", "s", config.Action{Type: "agent", Agent: "fixer"}))
	if len(d.reqs) != 1 || !d.reqs[0].Shadow {
		t.Fatalf("shadow not propagated: %+v", d.reqs)
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
