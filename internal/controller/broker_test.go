package controller

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// fakeSession records the turns it received and returns a single terminal update
// echoing the prompt, so a test can assert follow-ups reach the live session.
type fakeSession struct {
	id      string
	mu      sync.Mutex
	prompts []string
	closed  bool
}

func (s *fakeSession) ID() string { return s.id }

func (s *fakeSession) Prompt(_ context.Context, msg Message) (<-chan Update, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, msg.Text)
	s.mu.Unlock()
	ch := make(chan Update, 1)
	ch <- Update{Kind: UpdateDone, AgentID: s.id, Output: "echo:" + msg.Text}
	close(ch)
	return ch, nil
}

func (s *fakeSession) Cancel(context.Context) error { return nil }

func (s *fakeSession) Close(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

// fakeController hands out sessions and counts new-vs-resume, so a test can prove
// a follow-up after a restart RESUMES by id rather than opening a fresh session.
type fakeController struct {
	name                 string
	model                SessionModel
	cmu                  sync.Mutex // guards the counters under concurrent resume
	newN                 int
	resumeN              int
	resumedAgentAuthored bool
	last                 *fakeSession
	onResume             func() // optional hook to widen the resume window in tests
}

func (c *fakeController) Name() string         { return c.name }
func (c *fakeController) Model() SessionModel  { return c.model }
func (c *fakeController) Transport() Transport { return TransportACP }

func (c *fakeController) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{SessionModel: c.model, Transport: TransportACP}, nil
}

func (c *fakeController) NewSession(_ context.Context, _ Spec, _ Handler) (Session, error) {
	c.cmu.Lock()
	c.newN++
	c.cmu.Unlock()
	c.last = &fakeSession{id: "sess-1"}
	return c.last, nil
}

func (c *fakeController) ResumeSession(_ context.Context, id string, agentAuthored bool, _ Handler) (Session, error) {
	if c.onResume != nil {
		c.onResume()
	}
	sess := &fakeSession{id: id}
	c.cmu.Lock()
	c.resumeN++
	c.resumedAgentAuthored = agentAuthored
	c.last = sess
	c.cmu.Unlock()
	return sess, nil
}

func (c *fakeController) Runner() (Runner, error) { return nil, ErrNotRunnable }

// fakeStore is an in-memory SessionStore that also survives being handed to a
// second broker (the restart), so restart-survival can be exercised without disk.
type fakeStore struct {
	mu   sync.Mutex
	recs map[string]SessionRef
}

func newFakeStore() *fakeStore { return &fakeStore{recs: map[string]SessionRef{}} }

func (s *fakeStore) PutSession(ref SessionRef) error {
	s.mu.Lock()
	s.recs[ref.PRKey] = ref
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) DeleteSession(prKey string) error {
	s.mu.Lock()
	delete(s.recs, prKey)
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) Sessions() []SessionRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionRef, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	return out
}

func fakeRegistry(fc *fakeController) *Registry {
	return &Registry{
		controllers: map[string]Controller{fc.name: fc},
		builtin:     fc,
	}
}

func TestBrokerOpenFollowupClose(t *testing.T) {
	fc := &fakeController{name: "fake", model: ModelResumable}
	st := newFakeStore()
	b := NewBroker(fakeRegistry(fc), st, nil)
	ctx := context.Background()
	const pr = "o/r#1"

	sess, err := b.Open(ctx, pr, "fake", Spec{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fc.newN != 1 {
		t.Fatalf("Open should open exactly one session, got %d", fc.newN)
	}
	if _, ok := st.recs[pr]; !ok {
		t.Fatal("Open must persist the PR→session ref")
	}

	// A follow-up funnels to the SAME live session, not a new one.
	handled, err := b.Followup(ctx, pr, "please tighten it", nil)
	if err != nil || !handled {
		t.Fatalf("Followup handled=%v err=%v; want handled=true", handled, err)
	}
	if fc.newN != 1 || fc.resumeN != 0 {
		t.Fatalf("Followup must reuse the live session (new=%d resume=%d)", fc.newN, fc.resumeN)
	}
	fs := sess.(*fakeSession)
	fs.mu.Lock()
	got := append([]string(nil), fs.prompts...)
	fs.mu.Unlock()
	if len(got) != 1 || got[0] != "please tighten it" {
		t.Fatalf("follow-up not delivered to the live session: %v", got)
	}

	// A follow-up for an unknown PR reports not-handled (caller dispatches fresh).
	if handled, err := b.Followup(ctx, "o/r#999", "x", nil); err != nil || handled {
		t.Fatalf("unknown PR should be unhandled, got handled=%v err=%v", handled, err)
	}

	b.Close(ctx, pr)
	if !fs.closed {
		t.Fatal("Close must close the live session")
	}
	if _, ok := st.recs[pr]; ok {
		t.Fatal("Close must delete the persisted ref")
	}
}

// TestBrokerRestartSurvival proves a hand-off parked for you is re-attachable
// after a conductor restart: a fresh broker over the same store resumes the PR's
// session BY ID (not a new agent) and delivers the follow-up to it.
func TestBrokerRestartSurvival(t *testing.T) {
	fc := &fakeController{name: "fake", model: ModelResumable}
	st := newFakeStore()
	ctx := context.Background()
	const pr = "o/r#7"

	// First process: open a session for the PR.
	b1 := NewBroker(fakeRegistry(fc), st, nil)
	if _, err := b1.Open(ctx, pr, "fake", Spec{}, nil); err != nil {
		t.Fatal(err)
	}

	// Restart: a new broker loads the persisted ref (its in-memory live map is
	// empty). A follow-up must resume by id via the controller, not open anew.
	b2 := NewBroker(fakeRegistry(fc), st, nil)
	handled, err := b2.Followup(ctx, pr, "resumed follow-up", nil)
	if err != nil || !handled {
		t.Fatalf("post-restart follow-up handled=%v err=%v; want handled=true", handled, err)
	}
	if fc.resumeN != 1 {
		t.Fatalf("post-restart follow-up must resume by id exactly once, got resume=%d", fc.resumeN)
	}
	if fc.newN != 1 {
		t.Fatalf("restart must not open a fresh session (new=%d)", fc.newN)
	}
	if fc.last == nil || fc.last.id != "sess-1" {
		t.Fatalf("resumed session should carry the persisted id, got %+v", fc.last)
	}
	fc.last.mu.Lock()
	got := append([]string(nil), fc.last.prompts...)
	fc.last.mu.Unlock()
	if len(got) != 1 || got[0] != "resumed follow-up" {
		t.Fatalf("follow-up not delivered to the resumed session: %v", got)
	}
}

// TestBrokerSessionResumeIsSingleFlight (#36 §146 F9): concurrent follow-ups for
// one PR with a persisted ref but no live session must resume it EXACTLY ONCE.
// Pre-fix, Session() read live under the lock, released it, then resumed outside
// any lock — so N racers all saw no live session and each spawned an agent, with
// every loser silently overwritten in b.live and leaked (never Closed). The
// onResume hook widens the resume window so the old code deterministically
// resumes >1; the fix's per-PR claim collapses it to one.
func TestBrokerSessionResumeIsSingleFlight(t *testing.T) {
	var wg sync.WaitGroup
	resumeStarted := make(chan struct{})
	var once sync.Once
	fc := &fakeController{name: "fake", model: ModelResumable, onResume: func() {
		// Signal the first resumer has entered, then dwell so any peer that also
		// slipped past the live check would resume concurrently (old code races).
		once.Do(func() { close(resumeStarted) })
		<-resumeStarted
		for i := 0; i < 1e6; i++ { // busy dwell (no wall-clock deps in tests)
			_ = i
		}
	}}
	st := newFakeStore()
	const pr = "o/r#42"

	// Persist a ref via one broker, then hand the store to a fresh broker whose
	// in-memory live map is empty — the post-restart resume path.
	b1 := NewBroker(fakeRegistry(fc), st, nil)
	if _, err := b1.Open(context.Background(), pr, "fake", Spec{}, nil); err != nil {
		t.Fatal(err)
	}
	b := NewBroker(fakeRegistry(fc), st, nil)

	const racers = 16
	sessions := make([]Session, racers)
	errs := make([]error, racers)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			sessions[i], errs[i] = b.Session(context.Background(), pr, nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if fc.resumeN != 1 {
		t.Fatalf("concurrent Session() must resume once, got resume=%d", fc.resumeN)
	}
	// Every racer got the same single resumed session — no leaked duplicates.
	for i, s := range sessions {
		if s == nil {
			t.Fatalf("racer %d got a nil session", i)
		}
		if s != sessions[0] {
			t.Fatalf("racer %d got a different session than racer 0 (duplicate resume leaked)", i)
		}
	}
}

// Regression (#57 M5): a Close() racing an in-flight ResumeSession must not
// leave a resurrected, never-closed session behind. Close now holds the per-PR
// claim, and Session re-checks the ref after resuming, so the end state is
// deterministic: no live session for the PR, and the session that was resumed
// mid-race is closed rather than leaked.
func TestBrokerCloseRacesResumeNoLeak(t *testing.T) {
	resumeEntered := make(chan struct{})
	var once sync.Once
	fc := &fakeController{name: "fake", model: ModelResumable, onResume: func() {
		// Announce we've entered resume, then dwell so the concurrent Close()
		// is contending for the claim while the session is being resumed.
		once.Do(func() { close(resumeEntered) })
		for i := 0; i < 1e6; i++ { // busy dwell, no wall-clock dep
			_ = i
		}
	}}
	st := newFakeStore()
	const pr = "o/r#77"

	// Persist a ref via one broker, then resume through a fresh broker whose
	// live map is empty (the restart path that ResumeSession serves).
	b1 := NewBroker(fakeRegistry(fc), st, nil)
	if _, err := b1.Open(context.Background(), pr, "fake", Spec{}, nil); err != nil {
		t.Fatal(err)
	}
	b := NewBroker(fakeRegistry(fc), st, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = b.Session(context.Background(), pr, nil)
	}()
	go func() {
		defer wg.Done()
		<-resumeEntered // only race Close once a resume is genuinely in flight
		b.Close(context.Background(), pr)
	}()
	wg.Wait()

	// No live session survives for the PR…
	b.mu.Lock()
	live := b.live[pr]
	b.mu.Unlock()
	if live != nil {
		t.Fatalf("Close racing resume left a live session behind: %v", live)
	}
	// …the persisted ref is gone…
	if _, ok := st.recs[pr]; ok {
		t.Fatal("Close must delete the persisted ref")
	}
	// …and the session that got resumed mid-race is closed, not leaked.
	if fc.last == nil || !fc.last.closed {
		t.Fatalf("the resumed session must be closed, not leaked (last=%v)", fc.last)
	}
}

// Regression (#36 iso-review H5): a resumed agent-authored session must
// relaunch under the SAME deny-by-default posture it started with — the
// provenance is persisted in the session ref and replayed to ResumeSession
// across a restart, and the launch layer derives the deny-all proxy from it.
func TestBrokerResumeKeepsAgentAuthoredDenyDefault(t *testing.T) {
	fc := &fakeController{name: "fake", model: ModelResumable}
	st := newFakeStore()
	ctx := context.Background()
	const pr = "o/r#12"

	// The original dispatch was agent-authored.
	b1 := NewBroker(fakeRegistry(fc), st, nil)
	if _, err := b1.Open(ctx, pr, "fake", Spec{Request: dispatch.Request{AgentAuthored: true}}, nil); err != nil {
		t.Fatal(err)
	}
	if !st.recs[pr].AgentAuthored {
		t.Fatal("the ref must persist agent-authored provenance")
	}

	// Restart → resume must replay the flag, not hardcode false.
	b2 := NewBroker(fakeRegistry(fc), st, nil)
	if handled, err := b2.Followup(ctx, pr, "carry on", nil); err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if fc.resumeN != 1 || !fc.resumedAgentAuthored {
		t.Fatalf("resume must carry agentAuthored=true (resume=%d, flag=%v)", fc.resumeN, fc.resumedAgentAuthored)
	}

	// And the launch layer turns that resumed flag into the deny-all proxy.
	stubPlatform(t)
	calls := stubProxy(t, "127.0.0.1:5599")
	_, _, env, _, err := prepareLaunch("", "/wt", nil, []string{"tool"}, resumeOpts(nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(env, "\n"), "127.0.0.1:5599") {
		t.Fatalf("agent-authored resume must route through the deny-all proxy: %v", env)
	}
	if len(*calls) != 1 || len((*calls)[0]) != 0 {
		t.Fatalf("deny-all = empty allowlist: %v", *calls)
	}

	// A config-authored session resumes without the deny default.
	if fc2 := (&fakeController{name: "fake", model: ModelResumable}); true {
		st2 := newFakeStore()
		b3 := NewBroker(fakeRegistry(fc2), st2, nil)
		if _, err := b3.Open(ctx, pr, "fake", Spec{}, nil); err != nil {
			t.Fatal(err)
		}
		b4 := NewBroker(fakeRegistry(fc2), st2, nil)
		if _, err := b4.Followup(ctx, pr, "x", nil); err != nil {
			t.Fatal(err)
		}
		if fc2.resumedAgentAuthored {
			t.Fatal("config-authored resume must not inherit the deny default")
		}
	}
}
