package handoff

import (
	"context"
	"sync"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/controller"
)

// fakeSession is a live session standing in for any controller's agent: it
// records the turns it receives and replies with a scripted draft per turn, so
// the review loop can be exercised without a real runtime.
type fakeSession struct {
	mu        sync.Mutex
	prompts   []string
	replies   []string // reply[i] is the streamed output of the i-th Prompt
	cancelled bool
}

func (s *fakeSession) ID() string { return "fake-sess" }

func (s *fakeSession) Prompt(_ context.Context, msg controller.Message) (<-chan controller.Update, error) {
	s.mu.Lock()
	i := len(s.prompts)
	s.prompts = append(s.prompts, msg.Text)
	out := ""
	if i < len(s.replies) {
		out = s.replies[i]
	}
	s.mu.Unlock()
	ch := make(chan controller.Update, 1)
	ch <- controller.Update{Kind: controller.UpdateDone, Output: out}
	close(ch)
	return ch, nil
}

func (s *fakeSession) Cancel(context.Context) error {
	s.mu.Lock()
	s.cancelled = true
	s.mu.Unlock()
	return nil
}

func (s *fakeSession) Close(context.Context) error { return nil }

// scriptChannel yields a pre-scripted decision for each Present call, so a test
// can drive the loop through a revise-then-approve (or discard) sequence.
type scriptChannel struct {
	mu        sync.Mutex
	decisions []Decision
	presented []Draft // the draft shown at each step (to assert the body refreshed)
}

func (c *scriptChannel) Present(_ context.Context, d Draft) (Presentation, error) {
	c.mu.Lock()
	c.presented = append(c.presented, d)
	var dec Decision
	if len(c.decisions) > 0 {
		dec = c.decisions[0]
		c.decisions = c.decisions[1:]
	}
	c.mu.Unlock()
	return &scriptPresentation{dec: dec}, nil
}

type scriptPresentation struct{ dec Decision }

func (p *scriptPresentation) Ref() string                             { return "script://draft" }
func (p *scriptPresentation) Await(context.Context) (Decision, error) { return p.dec, nil }
func (p *scriptPresentation) Close()                                  {}

// TestReviewReviseThenApprove is the ship-gate review loop over a fake controller
// session: revise once (the revision reaches the agent and its output becomes the
// next draft), then approve (the agent is told to submit).
func TestReviewReviseThenApprove(t *testing.T) {
	sess := &fakeSession{replies: []string{"revised draft v2"}}
	ch := &scriptChannel{decisions: []Decision{
		{Action: ActionRevise, Text: "make it shorter"},
		{Action: ActionApprove},
	}}

	dec, err := Review(context.Background(), sess, ch, Draft{Title: "Review", Body: "draft v1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Action != ActionApprove {
		t.Fatalf("terminal decision should be approve, got %+v", dec)
	}

	sess.mu.Lock()
	prompts := append([]string(nil), sess.prompts...)
	sess.mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("expected 2 turns (revision + submit), got %v", prompts)
	}
	if prompts[0] != "make it shorter" {
		t.Fatalf("first turn should be the revision, got %q", prompts[0])
	}
	if prompts[1] != submitPrompt {
		t.Fatalf("approve should send the submit turn, got %q", prompts[1])
	}

	// The second presentation must show the refreshed draft body.
	ch.mu.Lock()
	presented := append([]Draft(nil), ch.presented...)
	ch.mu.Unlock()
	if len(presented) != 2 || presented[1].Body != "revised draft v2" {
		t.Fatalf("second draft body should be the revision output, got %+v", presented)
	}
}

func TestReviewDiscardCancels(t *testing.T) {
	sess := &fakeSession{}
	ch := &scriptChannel{decisions: []Decision{{Action: ActionDiscard}}}
	dec, err := Review(context.Background(), sess, ch, Draft{Body: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Action != ActionDiscard {
		t.Fatalf("want discard, got %+v", dec)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.cancelled {
		t.Fatal("discard must cancel the session")
	}
	if len(sess.prompts) != 0 {
		t.Fatalf("discard must not send any turn, got %v", sess.prompts)
	}
}
