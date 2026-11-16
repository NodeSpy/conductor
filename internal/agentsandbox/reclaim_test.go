//go:build linux || darwin

package agentsandbox

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/NodeSpy/conductor/internal/skill"
)

// reclaimRig is a resolver whose runs are live while the test says so.
type reclaimRig struct {
	t    *testing.T
	r    *Resolver
	ws   string
	live map[string]bool
	mu   sync.Mutex
	logs []string
}

func newReclaimRig(t *testing.T) *reclaimRig {
	g := &reclaimRig{t: t, ws: t.TempDir(), live: map[string]bool{}}
	g.r = g.resolver(t.TempDir())
	// No background sweep from a launch: each test sweeps when it says.
	lastSweep.Store(time.Now().Unix())
	return g
}

// resolver is a fresh resolver over root — a restarted daemon's.
func (g *reclaimRig) resolver(root string) *Resolver {
	return &Resolver{
		Registry: &Registry{}, StateRoot: root, GOOS: "linux",
		RunLive: func(run string) bool { return g.live[run] },
		Log: func(f string, a ...any) {
			g.mu.Lock()
			g.logs = append(g.logs, f)
			g.mu.Unlock()
		},
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

// launched is a launch in each home that has come and gone: it took the
// home's lock over from the daemon, and released it when it ended.
func launched(homes ...string) {
	for _, h := range homes {
		unlock := lockHomeShared(homeLockPath(h))
		releaseDaemonHold(homeLockPath(h))
		unlock()
	}
}

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
	launched(a1, a2, b)
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
	launched(h)
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
	launched(h)
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
	launched(ended, resumable, held)
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
	releaseDaemonHold(homeLockPath(h))
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

// An agent can outlive its launcher: paseo restarts kill the provider and
// `conductor sandbox exec` (the lock goes), then relaunch the agent with
// --resume. While its session is registered — not ended, its workspace
// there — its home stays, through its run's end and any sweep; once the
// session ends it goes. A daemon restart ends an in-process session.
func TestALiveRegisteredSessionKeepsItsHome(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rH"] = true
	reg := g.r.Registry
	reg.Register(Entry{Workspace: g.ws, Identity: "agent:fixer@acme/app", Dispatch: "rH:handoff"})
	h := g.home("rH:handoff")
	launched(h) // the launcher held the lock, then paseo restarted
	g.live["rH"] = false
	g.r.ReclaimRun("rH")
	ageIndex(t, g.r)
	g.r.ReclaimRun("rOther")
	if !exists(h) {
		t.Fatal("a registered live session's home was reclaimed once its launcher's lock was gone")
	}
	reg.EndSession("rH:handoff")
	if n := g.r.SweepDispatchHomes(); n != 1 || exists(h) {
		t.Fatalf("after the session ended: swept %d, home still there: %v", n, exists(h))
	}

	// The registry, reloaded by a restarted daemon: an in-process session
	// is over, one that outlives the daemon (paseo's) is not.
	path := filepath.Join(t.TempDir(), "registry.json")
	saved := &Registry{}
	if err := saved.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	inproc, outlives := t.TempDir(), t.TempDir()
	saved.Register(Entry{Workspace: inproc, Identity: "agent:a", Dispatch: "rI:fix"})
	saved.MarkInProcess("rI:fix")
	saved.Register(Entry{Workspace: outlives, Identity: "agent:b", Dispatch: "rP:fix"})
	reloaded := &Registry{}
	if err := reloaded.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if e, _ := reloaded.Lookup(inproc); !e.SessionEnded {
		t.Fatal("an in-process session survived a daemon restart")
	}
	if e, _ := reloaded.Lookup(outlives); e.SessionEnded {
		t.Fatal("a session that outlives the daemon was ended by a restart")
	}
}

// The daemon holds a home from the moment it prepares it: a run that ends
// before the launch (an async background or hand-off agent) takes the lock
// does not lose that launch's home.
func TestARunEndingBeforeItsLaunchLocksKeepsTheHome(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rB"] = true
	h := g.home("rB:bg") // prepared and indexed; the launcher not started yet
	g.live["rB"] = false
	if n := g.r.ReclaimRun("rB"); n != 0 || !exists(h) {
		t.Fatalf("the run's end reclaimed %d — a home whose launch had not locked it yet", n)
	}
	unlock := lockHomeShared(homeLockPath(h)) // the launch takes it over
	releaseDaemonHold(homeLockPath(h))
	ageIndex(t, g.r)
	if n := g.r.SweepDispatchHomes(); n != 0 || !exists(h) {
		t.Fatal("a running launch's home was swept")
	}
	unlock()
	if n := g.r.SweepDispatchHomes(); n != 1 || exists(h) {
		t.Fatalf("after the launch ended: swept %d, home still there: %v", n, exists(h))
	}
}

// A hard link to a file outside the home is unlinked, never chmodded: its
// target keeps its mode and content.
func TestRemovalNeverChmodsAHardLinkedOutsideFile(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rA"] = true
	h := g.home("rA:fix")
	launched(h)
	out := filepath.Join(filepath.Dir(g.r.StateRoot), "outside-"+filepath.Base(g.r.StateRoot))
	os.MkdirAll(out, 0o755)
	t.Cleanup(func() { os.RemoveAll(out) })
	target := filepath.Join(out, "secret")
	os.WriteFile(target, []byte("precious"), 0o400)
	ro := filepath.Join(h, "ro")
	os.MkdirAll(ro, 0o755)
	if err := os.Link(target, filepath.Join(ro, "hard")); err != nil {
		t.Skipf("no hard link across these dirs: %v", err)
	}
	os.Link(target, filepath.Join(h, "hard"))
	os.Chmod(ro, 0o555)
	g.live["rA"] = false
	if n := g.r.ReclaimRun("rA"); n != 1 || exists(h) {
		t.Fatalf("reclaimed %d; home still there: %v", n, exists(h))
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("the hard link's target: %v %v, want mode 0400 untouched", fi.Mode(), err)
	}
	if b, _ := os.ReadFile(target); string(b) != "precious" {
		t.Fatalf("the hard link's target: %q", b)
	}
}

// The window between the stat and the chmod of a directory cannot be won:
// a directory swapped for a hard link to an outside file (a symlink, or
// another directory) after it was stat'ed is refused, and nothing is
// chmodded.
func TestRemovalRefusesAnEntrySwappedAfterItsStat(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside")
	os.WriteFile(target, []byte("precious"), 0o400)
	home := filepath.Join(root, "home")
	os.Mkdir(home, 0o755)
	os.Mkdir(filepath.Join(home, "d"), 0o500)
	os.Mkdir(filepath.Join(home, "other"), 0o500)
	defer os.Chmod(filepath.Join(home, "other"), 0o700)
	hfd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(hfd)
	var st unix.Stat_t
	if err := unix.Fstatat(hfd, "d", &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	swaps := map[string]func(string) error{
		"hard link": func(p string) error { return os.Link(target, p) },
		"symlink":   func(p string) error { return os.Symlink(filepath.Join(home, "other"), p) },
		"directory": func(p string) error { return os.Mkdir(p, 0o500) },
	}
	// The stat'ed directory stays alive, aside: no swapped-in entry can
	// take its inode number.
	if err := os.Rename(filepath.Join(home, "d"), filepath.Join(home, "orig")); err != nil {
		t.Fatal(err)
	}
	for what, swap := range swaps {
		os.Remove(filepath.Join(home, "d"))
		if err := swap(filepath.Join(home, "d")); err != nil {
			t.Fatal(err)
		}
		fd, err := openDirForRemoval(hfd, "d", &st)
		if err == nil {
			unix.Close(fd)
		}
		if !errors.Is(err, errChanged) && !errors.Is(err, unix.ELOOP) && !errors.Is(err, unix.ENOTDIR) {
			t.Fatalf("%s swapped in: %v, want refused", what, err)
		}
	}
	for p, want := range map[string]os.FileMode{target: 0o400, filepath.Join(home, "other"): 0o500} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: mode %v (%v), want %v untouched", p, fi.Mode().Perm(), err, want)
		}
	}
}
