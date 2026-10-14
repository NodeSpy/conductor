package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// markStore is flowGateStore with a real comment high-water mark.
type markStore struct {
	*flowGateStore
	mu    sync.Mutex
	marks map[string]int64
}

func (s *markStore) LastCommentID(key, kind string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marks[key+"|"+kind]
}

func (s *markStore) AdvanceCommentID(key, kind string, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id > s.marks[key+"|"+kind] {
		s.marks[key+"|"+kind] = id
	}
	return nil
}

func reviewTrigger(kind, head, dedup string, commentID int64) core.Trigger {
	return core.Trigger{
		Source: "enginegate", Instance: "eg", Kind: kind,
		Target:  core.Target{Repo: "acme/x", PR: 1, Number: 1, HeadSHA: head},
		Title:   kind,
		Dedup:   dedup,
		Context: map[string]any{"msg": dedup, "comment_id": commentID, "comment_kind": store.CommentKindReview},
		Action:  config.Action{FlowRef: "0:eg.ping"},
	}
}

// C: a review is dispatched ONCE. The changes_requested run for a review
// carries its highest inline comment id and raises the comment high-water
// mark; a later re-derivation of the same review — the sweep still finding
// its threads unresolved on a new head, or after the reviewer edited the
// review body — carries ids at or below the mark and is dropped, while a new
// review (higher ids) dispatches. The mark is changes_requested's own: a
// new_comment dispatch never starves it.
func TestChangesRequestedDispatchesAReviewOnce(t *testing.T) {
	registerGateConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(gateCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	st := &markStore{flowGateStore: newFlowGateStore(), marks: map[string]int64{}}
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	eng := New(Options{Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{}, Notifier: notif, Flow: runner, Connectors: reg})

	ctx := context.Background()
	runs := func(tr core.Trigger) int {
		before := gateCalls()
		eng.process(ctx, tr)
		time.Sleep(150 * time.Millisecond)
		return gateCalls() - before
	}
	if n := runs(reviewTrigger("changes_requested", "h1", "review:41@h1", 1002)); n != 1 {
		t.Fatalf("the review's changes_requested ran %d times, want 1", n)
	}
	// The fixer pushed (new head); the bot's threads are still unresolved
	// and its old review now reads "stale" — the sweep re-derives it.
	if n := runs(reviewTrigger("changes_requested", "h2", "threads:h2:3:abc", 1002)); n != 0 {
		t.Fatalf("a re-derivation of the dispatched review ran %d times, want 0", n)
	}
	// A new_comment far above, then a NEW review: the new review dispatches —
	// new_comment's mark is not changes_requested's.
	if n := runs(reviewTrigger("new_comment", "h3", "review:50", 5000)); n != 1 {
		t.Fatalf("new_comment ran %d times, want 1", n)
	}
	if n := runs(reviewTrigger("changes_requested", "h3", "review:60@h3", 3000)); n != 1 {
		t.Fatalf("a new review's changes_requested ran %d times, want 1 (not starved by new_comment's mark)", n)
	}
	if n := runs(reviewTrigger("changes_requested", "h4", "threads:h4:4:def", 3000)); n != 0 {
		t.Fatalf("re-derivation of the second review ran %d times, want 0", n)
	}
	// A changes_requested with no comment_id (a body-only changes-request)
	// is not mark-gated.
	tr := reviewTrigger("changes_requested", "h5", "review:70@h5", 0)
	if n := runs(tr); n != 1 {
		t.Fatalf("a body-only changes_requested ran %d times, want 1", n)
	}
}
