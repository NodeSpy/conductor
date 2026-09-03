package controller

import (
	"context"
	"sync"
	"testing"
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
	name    string
	model   SessionModel
	newN    int
	resumeN int
	last    *fakeSession
}

func (c *fakeController) Name() string         { return c.name }
func (c *fakeController) Model() SessionModel  { return c.model }
func (c *fakeController) Transport() Transport { return TransportACP }

func (c *fakeController) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{SessionModel: c.model, Transport: TransportACP}, nil
}

func (c *fakeController) NewSession(_ context.Context, _ Spec, _ Handler) (Session, error) {
	c.newN++
	c.last = &fakeSession{id: "sess-1"}
	return c.last, nil
}

func (c *fakeController) ResumeSession(_ context.Context, id string, _ Handler) (Session, error) {
	c.resumeN++
	c.last = &fakeSession{id: id}
	return c.last, nil
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
