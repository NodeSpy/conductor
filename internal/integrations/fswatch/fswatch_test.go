package fswatch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func TestMatches(t *testing.T) {
	w := Watch{Match: "*.m4b"} // default events: create+write+rename
	cases := []struct {
		name string
		op   fsnotify.Op
		want bool
	}{
		{"book.m4b", fsnotify.Create, true},
		{"book.m4b", fsnotify.Write, true},
		{"book.m4b", fsnotify.Rename, true},
		{"book.m4b", fsnotify.Chmod, false}, // not in the default op set
		{"cover.jpg", fsnotify.Create, false},
		{"book.M4B", fsnotify.Create, false}, // glob is case-sensitive
	}
	for _, c := range cases {
		if got := w.matches(c.name, c.op); got != c.want {
			t.Errorf("matches(%q, %v) = %v, want %v", c.name, c.op, got, c.want)
		}
	}

	all := Watch{} // no match pattern => everything (of the default ops)
	if !all.matches("anything.txt", fsnotify.Create) {
		t.Error("empty match should accept any basename")
	}

	only := Watch{Match: "*.m4b", Events: []string{"chmod"}}
	if !only.matches("x.m4b", fsnotify.Chmod) {
		t.Error("explicit events:[chmod] should accept a chmod")
	}
	if only.matches("x.m4b", fsnotify.Create) {
		t.Error("events:[chmod] should reject a create")
	}
}

// fakeTimer lets a test fire the debounce callback on demand instead of waiting
// out a real duration.
type fakeTimer struct {
	mu     sync.Mutex
	f      func()
	resets int
}

func (t *fakeTimer) Reset(time.Duration) bool {
	t.mu.Lock()
	t.resets++
	t.mu.Unlock()
	return true
}
func (t *fakeTimer) Stop() bool { return true }
func (t *fakeTimer) fire()      { t.f() }

func TestDebouncerCollapsesBurst(t *testing.T) {
	var ft *fakeTimer
	nt := func(_ time.Duration, f func()) timer {
		ft = &fakeTimer{f: f}
		return ft
	}
	var mu sync.Mutex
	var fired []string
	d := newDebouncer(
		[]Watch{{Name: "w"}},
		nt,
		func(_ int, path, _ string) { mu.Lock(); fired = append(fired, path); mu.Unlock() },
	)

	d.arm(0, "/a/1.m4b", "CREATE") // creates the timer
	d.arm(0, "/a/2.m4b", "WRITE")  // resets
	d.arm(0, "/a/3.m4b", "WRITE")  // resets

	if len(fired) != 0 {
		t.Fatalf("no fire should happen before the quiet window elapses; got %v", fired)
	}
	if ft == nil {
		t.Fatal("timer was never created")
	}
	if ft.resets != 2 {
		t.Errorf("expected 2 resets from the 2 follow-up events, got %d", ft.resets)
	}

	ft.fire() // simulate the quiet window elapsing

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 {
		t.Fatalf("a burst must collapse into exactly one fire, got %d: %v", len(fired), fired)
	}
	if fired[0] != "/a/3.m4b" {
		t.Errorf("the fire should carry the last event's path, got %q", fired[0])
	}
}

func TestStartEmitsOnRealFileSettle(t *testing.T) {
	dir := t.TempDir()
	ig := &Integration{
		name: "t",
		cfg: Config{Watches: []Watch{{
			Name:     "staged",
			Path:     dir,
			Match:    "*.m4b",
			Debounce: config.Duration(40 * time.Millisecond),
			Action:   config.Action{Type: "command"},
		}}},
		newTimer: realTimer,
	}
	emits := make(chan core.Trigger, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ig.Start(ctx, func(_ context.Context, tr core.Trigger) { emits <- tr })
	time.Sleep(100 * time.Millisecond) // let the watcher set up

	if err := os.WriteFile(filepath.Join(dir, "book.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case tr := <-emits:
		if tr.Source != "fswatch" || tr.Kind != "staged" {
			t.Fatalf("unexpected trigger identity: %+v", tr)
		}
		if tr.Context["file"] != "book.m4b" {
			t.Fatalf("expected file=book.m4b, got %v", tr.Context["file"])
		}
		if tr.Context["watch"] != "staged" {
			t.Fatalf("expected watch=staged, got %v", tr.Context["watch"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no trigger emitted for a matching file")
	}

	// A non-matching file must not emit.
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case tr := <-emits:
		t.Fatalf("a non-matching file must not emit, got %+v", tr.Context)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestStartWatchesNewSubdirRecursively(t *testing.T) {
	dir := t.TempDir()
	ig := &Integration{
		name: "t",
		cfg: Config{Watches: []Watch{{
			Name:     "staged",
			Path:     dir,
			Match:    "*.m4b",
			Debounce: config.Duration(40 * time.Millisecond),
			Action:   config.Action{Type: "command"},
		}}},
		newTimer: realTimer,
	}
	emits := make(chan core.Trigger, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ig.Start(ctx, func(_ context.Context, tr core.Trigger) { emits <- tr })
	time.Sleep(100 * time.Millisecond)

	// A per-book folder appears, then its audio lands inside it — the watch must
	// have followed the new directory to catch the file event.
	sub := filepath.Join(dir, "Author", "Book")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // let the new dirs get watched
	if err := os.WriteFile(filepath.Join(sub, "b.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case tr := <-emits:
			if tr.Context["file"] == "b.m4b" {
				return // saw the file inside the newly-created subtree
			}
		case <-deadline:
			t.Fatal("no trigger emitted for a file created in a new subdirectory")
		}
	}
}
