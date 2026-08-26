package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/store"
)

type fakeDispatcher struct {
	reqs       []dispatch.Request
	ref        dispatch.RunRef
	err        error
	liveAgent  bool                                            // HasLiveAgent return value
	onDispatch func(dispatch.Request) (dispatch.RunRef, error) // per-call override (e.g. varying output)
}

func (f *fakeDispatcher) Dispatch(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
	f.reqs = append(f.reqs, r)
	if f.onDispatch != nil {
		return f.onDispatch(r)
	}
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

func TestRuntimePauseSkips(t *testing.T) {
	dir := t.TempDir()
	pauseFile := filepath.Join(dir, "paused")
	if err := os.WriteFile(pauseFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e := New(Options{Config: baseCfg(), Store: tempStore(t), Dispatch: d, Notifier: n,
		UserToken: func() (string, error) { return "u", nil }, PausePath: pauseFile})
	tr := agentTrigger("merge_conflict", "a/w", 1, "h", "sig", config.Action{Type: "agent", Agent: "fixer"})

	e.process(context.Background(), tr)
	if len(d.reqs) != 0 {
		t.Fatalf("paused: expected 0 dispatches, got %d", len(d.reqs))
	}
	if err := os.Remove(pauseFile); err != nil {
		t.Fatal(err)
	}
	e.process(context.Background(), tr)
	if len(d.reqs) != 1 {
		t.Fatalf("after resume: expected 1 dispatch, got %d", len(d.reqs))
	}
}

func TestPauseLabelSkips(t *testing.T) {
	cfg := baseCfg()
	cfg.Control.PauseLabel = "conductor:off"
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, cfg, d, n, nil)

	off := agentTrigger("merge_conflict", "a/w", 1, "h", "sig", config.Action{Type: "agent", Agent: "fixer"})
	off.Context["labels"] = []string{"needs-work", "conductor:off"}
	e.process(context.Background(), off)
	if len(d.reqs) != 0 {
		t.Fatalf("a pause-labeled object should be skipped, got %d", len(d.reqs))
	}

	on := agentTrigger("merge_conflict", "a/w", 2, "h2", "sig2", config.Action{Type: "agent", Agent: "fixer"})
	on.Context["labels"] = []string{"needs-work"}
	e.process(context.Background(), on)
	if len(d.reqs) != 1 {
		t.Fatalf("no pause label should dispatch, got %d", len(d.reqs))
	}
}

func TestAgentBudgetShedsOverCap(t *testing.T) {
	cfg := baseCfg()
	cfg.Control.MaxAgentsPerHour = 2
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, cfg, d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer"}

	// First two agent dispatches go through; the third is shed (over the hourly cap).
	for i := 0; i < 3; i++ {
		e.process(context.Background(), agentTrigger("new_comment", "a/w", i+1, "h", "sig", act))
	}
	if len(d.reqs) != 2 {
		t.Fatalf("budget of 2/hr should allow 2 dispatches, got %d", len(d.reqs))
	}
	// A command action is never budget-gated.
	e.process(context.Background(), agentTrigger("pr_behind", "a/w", 9, "h", "sig",
		config.Action{Type: "command", Command: []string{"echo"}}))
	if len(d.reqs) != 3 {
		t.Fatalf("command should not be budget-gated, got %d", len(d.reqs))
	}
}

func TestDispatchAndRecord(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"}
	tr := agentTrigger("new_comment", "a/w", 1, "h1", "sig1", act)

	e.process(context.Background(), tr)

	if len(d.reqs) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(d.reqs))
	}
	if st.LastSignature("a/w#1", "new_comment") != "sig1" {
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
	tr := agentTrigger("new_comment", "a/w", 2, "h", "same", act)
	e.process(context.Background(), tr)
	e.process(context.Background(), tr)
	if len(d.reqs) != 1 {
		t.Fatalf("dedup failed: want 1 dispatch, got %d", len(d.reqs))
	}
}

// commentTrigger is a new_comment trigger carrying a source comment id — the
// signal the sweep's missed-comment recovery leans on.
func commentTrigger(repo string, num int, id int64, sig string, act config.Action) core.Trigger {
	tr := agentTrigger("new_comment", repo, num, "h", sig, act)
	tr.Context["comment_id"] = id
	return tr
}

func TestCommentHWMAdvancesOnDispatch(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer"}

	// A fresh comment dispatches and raises the high-water mark to its id.
	e.process(context.Background(), commentTrigger("a/w", 1, 100, "c100", act))
	if len(d.reqs) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(d.reqs))
	}
	if got := st.LastCommentID("a/w#1"); got != 100 {
		t.Fatalf("HWM should be 100, got %d", got)
	}
}

func TestCommentHWMSkipsAlreadyHandled(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer"}
	if err := st.AdvanceCommentID("a/w#1", 200); err != nil {
		t.Fatal(err)
	}

	// A re-listed older comment (id <= mark) is dropped by the engine gate...
	e.process(context.Background(), commentTrigger("a/w", 1, 150, "c150", act))
	e.process(context.Background(), commentTrigger("a/w", 1, 200, "c200", act))
	if len(d.reqs) != 0 {
		t.Fatalf("comments at/under the HWM should be skipped, got %d dispatches", len(d.reqs))
	}
	// ...but a genuinely-newer one dispatches and advances the mark.
	e.process(context.Background(), commentTrigger("a/w", 1, 250, "c250", act))
	if len(d.reqs) != 1 {
		t.Fatalf("newer comment should dispatch, got %d", len(d.reqs))
	}
	if got := st.LastCommentID("a/w#1"); got != 250 {
		t.Fatalf("HWM should advance to 250, got %d", got)
	}
}

func TestBackoffThenRetry(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	st := tempStore(t)
	clock := time.Unix(1_700_000_000, 0)
	st.SetNow(func() time.Time { return clock })
	e := New(Options{Config: baseCfg(), Store: st, Dispatch: d, Notifier: n,
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})

	act := config.Action{Type: "agent", Agent: "fixer", MaxAttemptsPerHead: 1}
	// 1st: below soft → dispatches, records an attempt (attemptAt = clock).
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 3, "h", "s1", act))
	if len(d.reqs) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(d.reqs))
	}
	// 2nd, only 5m later: n>=soft and within the 10m cooldown → backoff skip, no escalate.
	clock = clock.Add(5 * time.Minute)
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 3, "h", "s2", act))
	if len(d.reqs) != 1 {
		t.Fatalf("2nd within cooldown should be skipped, got %d dispatches", len(d.reqs))
	}
	if n.has("escalate") {
		t.Fatalf("no escalation while still in backoff, events=%v", n.events)
	}
	// After the 10m cooldown elapses → eligible again: escalates once (n==soft) and dispatches.
	clock = clock.Add(10 * time.Minute)
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 3, "h", "s3", act))
	if len(d.reqs) != 2 {
		t.Fatalf("want a retry after the cooldown, got %d dispatches", len(d.reqs))
	}
	if !n.has("escalate") {
		t.Fatalf("expected a one-time escalation crossing the threshold, events=%v", n.events)
	}
}

func TestReviewWorkflowSkippedWhenAgentParked(t *testing.T) {
	// The review workflow shouldn't re-run while its interactive agent is parked on
	// the SAME head. (The parked agent implies a prior dispatch at head "h", which
	// records the attempt — simulated here.)
	dLive := &fakeDispatcher{liveAgent: true}
	e, st := newEng(t, baseCfg(), dLive, &fakeNotifier{}, nil)
	_ = st.RecordAttempt("a/w#10", "review_requested", "h")
	wf := config.Action{Steps: []config.Action{{ID: "assess", Type: "agent", Agent: "fixer", Prompt: "x"}}}
	e.process(context.Background(), agentTrigger("review_requested", "a/w", 10, "h", "reviewreq@h", wf))
	time.Sleep(30 * time.Millisecond) // the workflow would spawn async; assert it didn't
	if len(dLive.reqs) != 0 {
		t.Fatalf("review workflow must not re-run while an agent is parked on the same head, got %d", len(dLive.reqs))
	}
}

func TestReviewRequestReengagesOnNewHead(t *testing.T) {
	// A parked review agent from an OLD head must not block a re-request on a NEW
	// head — the parked agent is reviewing stale code. A same-head duplicate stays
	// suppressed. The re-engaged workflow records its attempt at the new head
	// synchronously (before spawning the async step goroutine), so we assert on that
	// attempt count rather than racing the goroutine's dispatch.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dLive := &fakeDispatcher{liveAgent: true}
	e, st := newEng(t, baseCfg(), dLive, &fakeNotifier{}, nil)
	_ = st.RecordAttempt("a/w#12", "review_requested", "h1") // agent parked on old head h1
	wf := config.Action{Steps: []config.Action{{ID: "assess", Type: "agent", Agent: "fixer", Prompt: "x"}}}

	// Re-request on new head h2 → re-engage despite the parked agent (records h2).
	e.process(ctx, agentTrigger("review_requested", "a/w", 12, "h2", "reviewreq@h2", wf))
	if got := st.Attempts("a/w#12", "review_requested", "h2"); got != 1 {
		t.Fatalf("re-request on a new head should re-engage (attempt recorded at h2), got %d", got)
	}
	// Duplicate delivery on the same head h2 → suppressed, no new attempt.
	e.process(ctx, agentTrigger("review_requested", "a/w", 12, "h2", "reviewreq@h2", wf))
	if got := st.Attempts("a/w#12", "review_requested", "h2"); got != 1 {
		t.Fatalf("duplicate re-request on the same head should be suppressed, got %d attempts", got)
	}
}

func TestReviewRequestedLivenessGate(t *testing.T) {
	act := config.Action{Type: "command", Command: []string{"critique"}}

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

func TestRerequestReviewGuidance(t *testing.T) {
	// Off by default: no re-request guidance in the prompt.
	d := &fakeDispatcher{}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	e.process(context.Background(), agentTrigger("changes_requested", "a/w", 30, "h", "s",
		config.Action{Type: "agent", Agent: "fixer", Prompt: "fix it"}))
	if strings.Contains(d.reqs[0].Action.Prompt, "Re-request review ONLY") {
		t.Fatal("re-request guidance must be opt-in")
	}

	// With rerequest_review: guidance appended so the agent closes the loop.
	d2 := &fakeDispatcher{}
	e2, _ := newEng(t, baseCfg(), d2, &fakeNotifier{}, nil)
	e2.process(context.Background(), agentTrigger("changes_requested", "a/w", 31, "h", "s",
		config.Action{Type: "agent", Agent: "fixer", Prompt: "fix it", RerequestReview: true}))
	if !strings.Contains(d2.reqs[0].Action.Prompt, "Re-request review ONLY") {
		t.Fatalf("expected re-request guidance, got: %q", d2.reqs[0].Action.Prompt)
	}
}

func TestFixersDoNotGetAskGuidance(t *testing.T) {
	// A top-level single-action fixer is autonomous: it must NOT be told to ask
	// interactive questions, even when its profile is archive_when_done. Interactive
	// "ask me" behavior is reserved for review hand-offs (background workflow steps).
	// It still gets the write-token wrapper.
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		"archived": {Provider: "claude", ArchiveWhenDone: true},
		"kept":     {Provider: "claude"},
	}}
	cfg.Control.Enabled = ptrBool(true)

	for _, agent := range []string{"archived", "kept"} {
		d := &fakeDispatcher{}
		e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
		e.process(context.Background(), agentTrigger("changes_requested", "a/w", 40, "h", "s",
			config.Action{Type: "agent", Agent: agent, Prompt: "fix"}))
		if strings.Contains(d.reqs[0].Action.Prompt, "AskUserQuestion") {
			t.Fatalf("autonomous fixer (agent=%s) must not get ask/hold guidance, got: %q", agent, d.reqs[0].Action.Prompt)
		}
		if !strings.Contains(d.reqs[0].Action.Prompt, "act as ME") {
			t.Fatalf("fixer (agent=%s) should still get the identity/write wrapper", agent)
		}
		if !strings.Contains(d.reqs[0].Action.Prompt, "be concise and human") {
			t.Fatalf("fixer (agent=%s) should get the concision guidance", agent)
		}
	}
}

func TestAgentGuidanceConfigOverride(t *testing.T) {
	run := func(cfg *config.Config) string {
		cfg.Control.Enabled = ptrBool(true)
		cfg.Agents = map[string]config.AgentProfile{"fixer": {Provider: "claude"}}
		d := &fakeDispatcher{}
		e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
		e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h", "s",
			config.Action{Type: "agent", Agent: "fixer", Prompt: "do it"}))
		return d.reqs[0].Action.Prompt
	}

	// Unset → built-in concise default.
	if p := run(&config.Config{}); !strings.Contains(p, "be concise and human") {
		t.Fatalf("unset agent_guidance should use the built-in default, got: %q", p)
	}
	// Custom → replaces the default.
	custom := "House style: reply in one short sentence."
	if p := run(&config.Config{AgentGuidance: &custom}); !strings.Contains(p, custom) || strings.Contains(p, "be concise and human") {
		t.Fatalf("custom agent_guidance should replace the default, got: %q", p)
	}
	// Empty → no guidance block (write wrapper still present).
	empty := ""
	if p := run(&config.Config{AgentGuidance: &empty}); strings.Contains(p, "be concise and human") {
		t.Fatalf("empty agent_guidance should disable it, got: %q", p)
	}
}

func TestForceBypassesDedup(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer"}
	tr := agentTrigger("new_comment", "a/w", 1, "h", "sig", act)

	e.process(context.Background(), tr) // 1: dispatches, records the signature
	e.process(context.Background(), tr) // 2: same signature → deduped
	if len(d.reqs) != 1 {
		t.Fatalf("2nd identical trigger should dedup, got %d dispatches", len(d.reqs))
	}
	forced := tr
	forced.Force = true
	e.process(context.Background(), forced) // forced → dispatches despite the dedup
	if len(d.reqs) != 2 {
		t.Fatalf("force should bypass dedup and dispatch, got %d", len(d.reqs))
	}
}

func TestRetryWhileDeferred(t *testing.T) {
	// Step defers ("status: retry") twice, then succeeds — conductor must loop until
	// the output no longer matches, not complete the deferred step.
	calls := 0
	d := &fakeDispatcher{onDispatch: func(dispatch.Request) (dispatch.RunRef, error) {
		calls++
		if calls < 3 {
			return dispatch.RunRef{Output: "reviewing #1 (gate=checks)… status: retry"}, nil
		}
		return dispatch.RunRef{Output: "reviewed and posted"}, nil
	}}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	rp := &config.StepRetry{WhileOutputMatches: "status: retry",
		Interval: config.Duration(2 * time.Millisecond), Timeout: config.Duration(2 * time.Second)}
	req := dispatch.Request{Trigger: core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "a/w", Number: 1}}}
	deferred := dispatch.RunRef{Output: "status: retry"}

	// The method releases/re-acquires the concurrency slot around each wait, so the
	// caller must be holding one (as the workflow goroutine is in production).
	e.acquire(context.Background())
	out := e.retryWhileDeferred(context.Background(), req, deferred, rp)
	if !strings.Contains(out.Output, "reviewed and posted") {
		t.Fatalf("retry should loop until ready, got %q after %d calls", out.Output, calls)
	}

	// Times out while still deferred → returns the last (still-deferred) ref, no hang.
	dAlways := &fakeDispatcher{onDispatch: func(dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Output: "status: retry"}, nil
	}}
	e2, _ := newEng(t, baseCfg(), dAlways, &fakeNotifier{}, nil)
	rp2 := &config.StepRetry{WhileOutputMatches: "status: retry",
		Interval: config.Duration(2 * time.Millisecond), Timeout: config.Duration(20 * time.Millisecond)}
	e2.acquire(context.Background())
	out2 := e2.retryWhileDeferred(context.Background(), req, deferred, rp2)
	if !strings.Contains(out2.Output, "status: retry") {
		t.Fatalf("on timeout should return the last deferred ref, got %q", out2.Output)
	}
}

func TestPerAgentGuidancePrecedence(t *testing.T) {
	global := "GLOBAL house style."
	perAgent := "FIXER-only style."
	empty := ""

	run := func(profile config.AgentProfile, globalGuidance *string) string {
		cfg := &config.Config{AgentGuidance: globalGuidance,
			Agents: map[string]config.AgentProfile{"fixer": profile}}
		cfg.Control.Enabled = ptrBool(true)
		d := &fakeDispatcher{}
		e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
		e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h", "s",
			config.Action{Type: "agent", Agent: "fixer", Prompt: "do it"}))
		return d.reqs[0].Action.Prompt
	}
	base := config.AgentProfile{Provider: "claude"}

	// Per-agent guidance wins over the global.
	if p := run(config.AgentProfile{Provider: "claude", Guidance: &perAgent}, &global); !strings.Contains(p, perAgent) || strings.Contains(p, global) {
		t.Fatalf("per-agent guidance should override global, got: %q", p)
	}
	// No per-agent → falls through to global.
	if p := run(base, &global); !strings.Contains(p, global) {
		t.Fatalf("no per-agent guidance should fall through to global, got: %q", p)
	}
	// No per-agent, no global → built-in default.
	if p := run(base, nil); !strings.Contains(p, "be concise and human") {
		t.Fatalf("no guidance anywhere should use the built-in default, got: %q", p)
	}
	// Per-agent "" disables even when a global is set.
	if p := run(config.AgentProfile{Provider: "claude", Guidance: &empty}, &global); strings.Contains(p, global) || strings.Contains(p, "be concise and human") {
		t.Fatalf("per-agent empty should disable guidance for that agent, got: %q", p)
	}
}

func TestLiveGatedKindNotAbandonedOnDispatch(t *testing.T) {
	// changes_requested is live-gated: a "successful" dispatch (agent launched)
	// must NOT record a done/dedup flag — otherwise a culled/incomplete agent
	// leaves the work marked done and the sweep never retries it. It should
	// re-fire until the underlying condition clears (or an agent is working it).
	act := config.Action{Type: "agent", Agent: "fixer"}
	d := &fakeDispatcher{} // dispatch succeeds, no lingering agent
	e, st := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	tr := agentTrigger("changes_requested", "a/w", 50, "h", "threads:h:2:abc", act)
	e.process(context.Background(), tr)
	if st.LastSignature("a/w#50", "changes_requested") != "" {
		t.Fatal("changes_requested must not mark 'done' on dispatch")
	}
	e.process(context.Background(), tr) // same unresolved state, no live agent → retry
	if len(d.reqs) != 2 {
		t.Fatalf("should retry (work not abandoned), got %d dispatches", len(d.reqs))
	}

	// A catch-up whose PR already has a working agent: the dispatcher reports
	// Skipped, and the engine must NOT count it as an attempt (toward the cap).
	dSkip := &fakeDispatcher{ref: dispatch.RunRef{Skipped: true, Output: "skipped: agent on PR"}}
	e2, st2 := newEng(t, baseCfg(), dSkip, &fakeNotifier{}, nil)
	ctr := agentTrigger("changes_requested", "a/w", 51, "h", "threads:h:2:abc", act)
	ctr.CatchUp = true
	e2.process(context.Background(), ctr)
	if st2.Attempts("a/w#51", "changes_requested", "h") != 0 {
		t.Fatal("a skipped catch-up must not count as an attempt")
	}
	if dSkip.reqs[0].CatchUp != true {
		t.Fatal("CatchUp should propagate to the dispatch request")
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
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 6, "h", "s", config.Action{Type: "agent", Agent: "fixer"}))
	if st.LastSignature("a/w#6", "new_comment") == "" {
		t.Fatal("precondition: expected recorded state")
	}
	e.process(context.Background(), core.Trigger{Source: "github", Kind: core.KindClosed,
		Target: core.Target{Repo: "a/w", PR: 6, Number: 6}})
	if st.LastSignature("a/w#6", "new_comment") != "" {
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

func TestVariantDedupIsolation(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, baseCfg(), d, n, nil)
	act := config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"}

	// Variant "a" acts on a comment; its dedup is keyed new_comment#a.
	ta := agentTrigger("new_comment", "a/w", 5, "h", "sig", act)
	ta.Variant = "a"
	e.process(context.Background(), ta)
	// Variant "b" with the SAME dedup sig still dispatches — separate key, no collision.
	tb := agentTrigger("new_comment", "a/w", 5, "h", "sig", act)
	tb.Variant = "b"
	e.process(context.Background(), tb)
	if len(d.reqs) != 2 {
		t.Fatalf("two variants should each dispatch, got %d", len(d.reqs))
	}
	if st.LastSignature("a/w#5", "new_comment#a") != "sig" || st.LastSignature("a/w#5", "new_comment#b") != "sig" {
		t.Fatal("each variant should record under its own kind#variant key")
	}
	// Regression: an UNNAMED action keys on bare kind (existing state honored).
	tu := agentTrigger("new_comment", "a/w", 6, "h", "sig2", act)
	e.process(context.Background(), tu)
	if st.LastSignature("a/w#6", "new_comment") != "sig2" {
		t.Fatal("unnamed action must key on bare kind")
	}
}

func TestLogTag(t *testing.T) {
	pr := core.Trigger{Instance: "ednition", Kind: "merge_conflict",
		Target: core.Target{Repo: "acme/w", PR: 5, Number: 5}}
	if got := tag(pr); got != "engine[ednition acme/w#5 merge_conflict]" {
		t.Fatalf("pr tag: %q", got)
	}
	variant := core.Trigger{Instance: "ednition", Kind: "review_requested", Variant: "backend",
		Target: core.Target{Repo: "acme/w", PR: 6, Number: 6}}
	if got := tag(variant); got != "engine[ednition acme/w#6 review_requested#backend]" {
		t.Fatalf("variant tag: %q", got)
	}
	issue := core.Trigger{Instance: "ednition", Kind: "issue_matched",
		Target: core.Target{Repo: "acme/w", Issue: 42, Number: 42}}
	if got := tag(issue); got != "engine[ednition acme/w#42 issue_matched]" {
		t.Fatalf("issue tag: %q", got)
	}
}
