package agentsandbox

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// gitWatch runs a detector's watcher loop over ws's git guard, with no
// process of its own (nothing to signal), counting what it reports.
func gitWatch(t *testing.T, ws string, list func() ([]procInfo, error)) (*detector, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	d := &detector{spec: DetectSpec{Action: DetectKill, Interval: 5 * time.Millisecond}, run: "r", allow: map[string]bool{},
		list:   list,
		signal: func(int, uint64) error { return nil },
		event: func(kind string, f map[string]any) {
			if kind == "detection" {
				mu.Lock()
				n++
				mu.Unlock()
			}
		},
		git: newGitGuard(ws)}
	if d.git == nil {
		t.Fatal("no git guard")
	}
	d.start()
	t.Cleanup(d.close)
	return d, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func noProcs() ([]procInfo, error) { return nil, nil }

func gitWorkspace(t *testing.T) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(filepath.Join(ws, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws
}

// A .git removed from a workspace that is still there is reported — once.
// The watcher reported it, and escalated it to the operator, on every tick
// for as long as it ran (hours, on a launch whose agent it had already
// killed).
func TestRemovedGitIsReportedOnce(t *testing.T) {
	ws := gitWorkspace(t)
	_, count := gitWatch(t, ws, noProcs)
	if err := os.RemoveAll(filepath.Join(ws, ".git")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // ~30 ticks
	if n := count(); n != 1 {
		t.Fatalf("a removed .git was reported %d times, want once", n)
	}
}

// The workspace itself gone is conductor's teardown — an agent cannot remove
// the directory its sandbox is mounted on — not an agent's tampering.
func TestRemovedWorkspaceIsNotTampering(t *testing.T) {
	ws := gitWorkspace(t)
	_, count := gitWatch(t, ws, noProcs)
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := count(); n != 0 {
		t.Fatalf("the workspace's teardown was reported as tampering %d time(s)", n)
	}
}

// A watcher that killed its launch stops watching: nothing of the launch is
// left to detect, and a watcher left running reported and escalated the
// same finding on every tick until the daemon restarted.
func TestWatcherStopsOnceItKilledTheLaunch(t *testing.T) {
	ws := gitWorkspace(t)
	var mu sync.Mutex
	scans := 0
	gitWatch(t, ws, func() ([]procInfo, error) { mu.Lock(); scans++; mu.Unlock(); return nil, nil })
	if err := os.RemoveAll(filepath.Join(ws, ".git")); err != nil { // tampering: killed
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	before := scans
	mu.Unlock()
	time.Sleep(100 * time.Millisecond) // ~20 ticks
	mu.Lock()
	after := scans
	mu.Unlock()
	if after != before {
		t.Fatalf("the watcher kept scanning a launch it killed (%d more scans)", after-before)
	}
}
