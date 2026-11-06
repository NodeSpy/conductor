package attention

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/journal"
)

func newJournal(t *testing.T) *journal.Journal {
	t.Helper()
	j, err := journal.Open(journal.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func transitions(t *testing.T, j *journal.Journal, from string) []string {
	t.Helper()
	evs, _, _, err := j.Read(from, []string{Topic}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Type+":"+e.Subject)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSourceReconcileOpensUpdatesResolves(t *testing.T) {
	j := newJournal(t)
	start := j.Cursor()
	var current []Item
	s := New(Options{Journal: j})
	s.AddSource(Source{Name: "approvals", List: func(context.Context) ([]Item, error) { return current, nil }})

	current = []Item{{Key: "p1", Title: "one"}, {Key: "p2", Title: "two"}}
	s.Reconcile(context.Background())
	s.Reconcile(context.Background()) // no change: no events
	current = []Item{{Key: "p2", Title: "two, edited"}}
	s.Reconcile(context.Background())

	want := []string{"opened:p1", "opened:p2", "updated:p2", "resolved:p1"}
	if got := transitions(t, j, start); !eq(got, want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	items, _ := s.Snapshot()
	if len(items) != 1 || items[0].Title != "two, edited" || items[0].Source != "approvals" {
		t.Fatalf("snapshot = %+v", items)
	}
}

func TestSourceErrorKeepsItsItems(t *testing.T) {
	j := newJournal(t)
	fail := false
	s := New(Options{Journal: j})
	s.AddSource(Source{Name: "x", List: func(context.Context) ([]Item, error) {
		if fail {
			return nil, errors.New("down")
		}
		return []Item{{Key: "k"}}, nil
	}})
	s.Reconcile(context.Background())
	fail = true
	s.Reconcile(context.Background())
	if items, _ := s.Snapshot(); len(items) != 1 {
		t.Fatalf("a failing source dropped its items: %+v", items)
	}
}

func TestRaisedItemsResolveAndDismiss(t *testing.T) {
	j := newJournal(t)
	s := New(Options{Journal: j})
	s.AddSource(Source{Name: "src", List: func(context.Context) ([]Item, error) { return []Item{{Key: "sourced"}}, nil }})
	s.Reconcile(context.Background())

	s.Raise(Item{Key: "a", Subject: "run:1"})
	s.Raise(Item{Key: "b", Subject: "run:1"})
	s.Raise(Item{Key: "c"})
	s.ResolveSubject("run:1")
	if err := s.Dismiss("c"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dismiss("sourced"); !errors.Is(err, ErrNotDismissable) {
		t.Fatalf("dismissing a sourced item: %v", err)
	}
	if err := s.Dismiss("nope"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("dismissing an unknown item: %v", err)
	}
	// A source reconcile never touches raised items, and Resolve never touches
	// sourced ones.
	s.Raise(Item{Key: "d"})
	s.Resolve("sourced")
	s.Reconcile(context.Background())
	items, _ := s.Snapshot()
	if len(items) != 2 {
		t.Fatalf("open = %+v", items)
	}
}

func TestSnapshotCursorFollowsWithoutGap(t *testing.T) {
	j := newJournal(t)
	s := New(Options{Journal: j})
	s.Raise(Item{Key: "a"})
	items, cur := s.Snapshot()
	if len(items) != 1 {
		t.Fatal("snapshot missing a")
	}
	s.Raise(Item{Key: "b"})
	if got := transitions(t, j, cur); !eq(got, []string{"opened:b"}) {
		t.Fatalf("after snapshot cursor: %v", got)
	}
}

func TestOpenSetSurvivesRestart(t *testing.T) {
	j := newJournal(t)
	path := filepath.Join(t.TempDir(), "attention.json")
	s := New(Options{Journal: j, StatePath: path})
	s.Raise(Item{Key: "a", Title: "A", Severity: Urgent})
	s.Raise(Item{Key: "b"})
	s.Resolve("b")

	s2 := New(Options{Journal: j, StatePath: path})
	items, _ := s2.Snapshot()
	if len(items) != 1 || items[0].Key != "a" || items[0].Severity != Urgent || items[0].Opened.IsZero() {
		t.Fatalf("reloaded = %+v", items)
	}
	// Re-raising an open item after a restart is not a new opening.
	start := j.Cursor()
	s2.Raise(Item{Key: "a", Title: "A", Severity: Urgent})
	if got := transitions(t, j, start); len(got) != 0 {
		t.Fatalf("re-raise recorded %v", got)
	}
}
