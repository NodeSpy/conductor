//go:build linux || darwin

package agentsandbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/skill"
)

// reclaimRig is a resolver whose runs are live while the test says so.
type reclaimRig struct {
	t    *testing.T
	r    *Resolver
	ws   string
	live map[string]bool
	logs []string
}

func newReclaimRig(t *testing.T) *reclaimRig {
	g := &reclaimRig{t: t, ws: t.TempDir(), live: map[string]bool{}}
	g.r = g.resolver(t.TempDir())
	return g
}

// resolver is a fresh resolver over root — a restarted daemon's.
func (g *reclaimRig) resolver(root string) *Resolver {
	return &Resolver{
		Registry: &Registry{}, StateRoot: root, GOOS: "linux",
		RunLive: func(run string) bool { return g.live[run] },
		Log:     func(f string, a ...any) { g.logs = append(g.logs, f) },
	}
}

// home prepares dispatch's home, as a launch does.
func (g *reclaimRig) home(dispatch string) string {
	g.t.Helper()
	p, err := g.r.PolicyForEntry(Entry{Workspace: g.ws, Identity: "agent:fixer@acme/app", Dispatch: dispatch}, "claude", nil, skill.ProcessAnchor{})
	if err != nil {
		g.t.Fatal(err)
	}
	return p.Home
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// ageIndex makes every indexed launch older than the launch grace.
func ageIndex(t *testing.T, r *Resolver) {
	t.Helper()
	dir := r.homesDir()
	idx := loadHomeIndex(dir)
	for k, rec := range idx {
		rec.Used = rec.Used.Add(-2 * LaunchGrace)
		idx[k] = rec
	}
	r.saveHomeIndex(dir, idx)
}

// A run's homes go the moment it ends — no TTL — and only that run's: a
// live run's (running, or interrupted and resumable) stay.
func TestReclaimRunRemovesItsHomesAtOnce(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rA"], g.live["rB"] = true, true
	a1, a2, b := g.home("rA:fix"), g.home("rA:review[0]"), g.home("rB:fix")
	g.live["rA"] = false
	if n := g.r.ReclaimRun("rA"); n != 2 {
		t.Fatalf("reclaimed %d, want 2", n)
	}
	if exists(a1) || exists(a2) {
		t.Fatal("an ended run's home was kept")
	}
	if !exists(b) {
		t.Fatal("a live run's home was reclaimed")
	}
	if idx := loadHomeIndex(g.r.homesDir()); len(idx) != 1 {
		t.Fatalf("index after reclaim: %v", idx)
	}
	// Even past the launch grace, the sweep keeps a live run's home.
	ageIndex(t, g.r)
	if n := g.r.SweepDispatchHomes(); n != 0 || !exists(b) {
		t.Fatalf("sweep removed %d; live home kept: %v", n, exists(b))
	}
}

// A home an agent made read-only (Go's module cache: 0555 dirs, 0444
// files) is removed, not left behind on a swallowed error.
func TestReclaimRemovesReadOnlyTrees(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rA"] = true
	h := g.home("rA:fix")
	mod := filepath.Join(h, "go", "pkg", "mod", "example.com", "m@v1.0.0", "sub")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "x.go"), []byte("package sub"), 0o444); err != nil {
		t.Fatal(err)
	}
	for d := mod; d != h; d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	// A directory the agent locked its owner out of entirely.
	locked := filepath.Join(h, "locked")
	os.MkdirAll(filepath.Join(locked, "in"), 0o700)
	os.WriteFile(filepath.Join(locked, "in", "f"), []byte("x"), 0o400)
	os.Chmod(filepath.Join(locked, "in"), 0)
	os.Chmod(locked, 0)
	g.live["rA"] = false
	if n := g.r.ReclaimRun("rA"); n != 1 || exists(h) {
		t.Fatalf("reclaimed %d; home still there: %v", n, exists(h))
	}
	if left, _ := filepath.Glob(filepath.Join(g.r.homesDir(), reclaimPrefix+"*")); len(left) != 0 {
		t.Fatalf("left aside: %v", left)
	}
}

// A home is the agent's: a symlink in it is removed, never followed — what
// it points at outside the home keeps its content and its modes.
func TestReclaimNeverFollowsSymlinksOut(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rA"] = true
	h := g.home("rA:fix")
	out := t.TempDir()
	victim := filepath.Join(out, "keep")
	os.MkdirAll(filepath.Join(victim, "deep"), 0o755)
	os.WriteFile(filepath.Join(victim, "deep", "f"), []byte("precious"), 0o444)
	os.Chmod(filepath.Join(victim, "deep"), 0o555)
	os.Chmod(victim, 0o555)
	t.Cleanup(func() { os.Chmod(victim, 0o755); os.Chmod(filepath.Join(victim, "deep"), 0o755) })
	os.Symlink(victim, filepath.Join(h, "dirlink"))
	os.Symlink(filepath.Join(victim, "deep", "f"), filepath.Join(h, "filelink"))
	ro := filepath.Join(h, "ro")
	os.MkdirAll(ro, 0o755)
	os.Symlink(victim, filepath.Join(ro, "inner"))
	os.Chmod(ro, 0o555)

	g.live["rA"] = false
	if n := g.r.ReclaimRun("rA"); n != 1 || exists(h) {
		t.Fatalf("reclaimed %d; home still there: %v", n, exists(h))
	}
	b, err := os.ReadFile(filepath.Join(victim, "deep", "f"))
	if err != nil || string(b) != "precious" {
		t.Fatalf("the link's target: %q %v", b, err)
	}
	for p, want := range map[string]os.FileMode{victim: 0o555, filepath.Join(victim, "deep"): 0o555, filepath.Join(victim, "deep", "f"): 0o444} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: %v %v, want mode %v untouched", p, fi.Mode(), err, want)
		}
	}
}

// After a restart the sweep removes what no live run or launch can use:
// an ended run's home, a pre-index home no registered workspace names, a
// removal left half done. It keeps a resumable run's home, one a launch
// still holds, a home launched into a moment ago, and a pre-index home
// whose registered workspace is still there.
func TestSweepAfterRestartRemovesOnlyOrphans(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rEnded"], g.live["rResumable"], g.live["rHeld"] = true, true, true
	ended, resumable, held := g.home("rEnded:fix"), g.home("rResumable:fix"), g.home("rHeld:fix")
	dir := g.r.homesDir()
	old := time.Now().Add(-2 * LaunchGrace)
	legacy := filepath.Join(dir, "agent_legacy-0123456789ab")
	os.MkdirAll(filepath.Join(legacy, ".claude"), 0o700)
	os.Chtimes(legacy, old, old)
	ws := t.TempDir()
	registered := filepath.Join(dir, IdentityKey("agent:x@rOld:fix"))
	os.MkdirAll(registered, 0o700)
	os.Chtimes(registered, old, old)
	half := filepath.Join(dir, reclaimPrefix+"agent_y-abc")
	os.MkdirAll(filepath.Join(half, "go"), 0o555)
	ageIndex(t, g.r)

	// The daemon restarts: the run that ended while it was down is no longer
	// live; a launch (an agent the run left running) holds its home.
	unlock := lockHomeShared(homeLockPath(held))
	defer unlock()
	g.live["rEnded"], g.live["rHeld"] = false, false
	r2 := g.resolver(g.r.StateRoot)
	r2.Registry.Register(Entry{Workspace: ws, Identity: "agent:x", Dispatch: "rOld:fix"})
	g.r = r2
	fresh := g.home("rFresh:fix") // launched just now; RunLive says no
	if n := r2.SweepDispatchHomes(); n != 3 {
		t.Fatalf("swept %d, want 3 (ended, legacy, half-removed)", n)
	}
	for p, want := range map[string]bool{ended: false, legacy: false, half: false,
		resumable: true, held: true, fresh: true, registered: true} {
		if exists(p) != want {
			t.Errorf("%s: exists %v, want %v", filepath.Base(p), exists(p), want)
		}
	}
	// Once the launch ends, the held home goes on the next sweep.
	unlock()
	if n := r2.SweepDispatchHomes(); n != 1 || exists(held) {
		t.Fatalf("after the launch ended: swept %d, held home still there: %v", n, exists(held))
	}
}

// A run's end leaves a home a launch still holds; the sweep takes it once
// the launch is gone. Nothing indexed goes while RunLive is not wired.
func TestReclaimRunKeepsAHeldHome(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rA"] = true
	h := g.home("rA:bg")
	unlock := lockHomeShared(homeLockPath(h))
	g.live["rA"] = false
	if n := g.r.ReclaimRun("rA"); n != 0 || !exists(h) {
		t.Fatalf("reclaimed %d a home a launch holds", n)
	}
	unlock()
	ageIndex(t, g.r)
	g.r.RunLive = nil
	if n := g.r.SweepDispatchHomes(); n != 0 || !exists(h) {
		t.Fatal("swept an indexed home with no way to tell its run is over")
	}
	g.r.RunLive = func(string) bool { return false }
	if n := g.r.SweepDispatchHomes(); n != 1 || exists(h) {
		t.Fatalf("swept %d; the released home is still there: %v", n, exists(h))
	}
}
