//go:build linux || darwin

package agentsandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	// A directory its owner may search but not read.
	search := filepath.Join(h, "search")
	os.MkdirAll(filepath.Join(search, "in"), 0o700)
	os.WriteFile(filepath.Join(search, "in", "f"), []byte("x"), 0o400)
	os.Chmod(search, 0o100)
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
	// An entry recorded before sessions were bound, reloaded: its backend
	// cannot be asked, so it keeps its home (boundedly).
	r2.Registry.Register(Entry{Workspace: ws, Identity: "agent:x", Dispatch: "rOld:fix"})
	r2.Registry.BindSession(ws, "rOld:fix", SessionRef{Backend: backendUnknown})
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

// fakeBackend is a session backend: the sessions it lists, or an error.
type fakeBackend struct {
	mu   sync.Mutex
	live map[string]bool
	err  error
}

func (b *fakeBackend) set(err error, ids ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err, b.live = err, map[string]bool{}
	for _, id := range ids {
		b.live[id] = true
	}
}

func (b *fakeBackend) list(context.Context) (map[string]bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.live, b.err
}

func newFakeBackend(t *testing.T, ids ...string) (string, *fakeBackend) {
	name := "fake:" + t.Name()
	b := &fakeBackend{}
	b.set(nil, ids...)
	RegisterSessionLister(name, b.list)
	return name, b
}

// An agent can outlive its launcher and its run (paseo restarts the provider
// and relaunches it with --resume; a hand-off stays open). Its home is kept
// while its backend still lists the session — not while conductor's
// bookkeeping says so: an agent archived in the backend's own UI, a session
// a crash never closed, goes at the next sweep.
func TestAnOutOfProcessSessionsHomeFollowsItsBackend(t *testing.T) {
	g := newReclaimRig(t)
	backend, fb := newFakeBackend(t, "a1")
	g.live["rH"] = true
	reg := g.r.Registry
	reg.Register(Entry{Workspace: g.ws, Identity: "agent:fixer@acme/app", Dispatch: "rH:handoff"})
	h := g.home("rH:handoff")
	reg.BindSession(g.ws, "rH:handoff", SessionRef{Backend: backend, ID: "a1"})
	launched(h) // the launcher held the lock, then the backend restarted it
	g.live["rH"] = false
	g.r.ReclaimRun("rH")
	ageIndex(t, g.r)
	g.r.SweepDispatchHomes()
	if !exists(h) {
		t.Fatal("the home of a session its backend still lists was reclaimed")
	}
	fb.set(nil) // archived in the backend's UI; conductor was never told
	if n := g.r.SweepDispatchHomes(); n != 1 || exists(h) {
		t.Fatalf("after the backend stopped listing it: swept %d, home still there: %v", n, exists(h))
	}
}

// A backend that cannot answer keeps its sessions' homes, but not forever:
// SessionMaxUnknown after the launch, then they go (reported once). An
// entry recorded before sessions were bound counts from the restart.
func TestAnUnanswerableBackendKeepsHomesBoundedly(t *testing.T) {
	g := newReclaimRig(t)
	backend, fb := newFakeBackend(t)
	fb.set(errors.New("paseo daemon not running"))
	reg := g.r.Registry
	ws2 := t.TempDir()
	reg.Register(Entry{Workspace: g.ws, Identity: "agent:fixer@acme/app", Dispatch: "rN:fix"})
	reg.Register(Entry{Workspace: ws2, Identity: "agent:fixer@acme/app", Dispatch: "rO:fix"})
	g.live["rN"], g.live["rO"] = true, true
	recent, old := g.home("rN:fix"), g.home("rO:fix")
	launched(recent, old)
	reg.BindSession(g.ws, "rN:fix", SessionRef{Backend: backend, ID: "n"})
	reg.BindSession(ws2, "rO:fix", SessionRef{Backend: backend, ID: "o", Bound: time.Now().Add(-SessionMaxUnknown - time.Hour)})
	g.live["rN"], g.live["rO"] = false, false
	ageIndex(t, g.r)
	if n := g.r.SweepDispatchHomes(); n != 1 || exists(old) || !exists(recent) {
		t.Fatalf("swept %d; old kept %v, recent kept %v", n, exists(old), exists(recent))
	}
	g.r.SweepDispatchHomes()
	g.mu.Lock()
	expired := 0
	for _, l := range g.logs {
		if strings.Contains(l, "could not be confirmed live") {
			expired++
		}
	}
	g.mu.Unlock()
	if expired != 1 {
		t.Fatalf("the expiry was reported %d times, want once", expired)
	}

	// Recorded before sessions were bound: reloaded with a backend that
	// cannot be asked, from the restart on.
	path := filepath.Join(t.TempDir(), "registry.json")
	os.WriteFile(path, []byte(`[{"workspace":"`+ws2+`","identity":"agent:x","dispatch":"rL:fix"}]`), 0o600)
	reloaded := &Registry{}
	if err := reloaded.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if e, _ := reloaded.Lookup(ws2); e.Session.Backend != backendUnknown || time.Since(e.Session.Bound) > time.Minute {
		t.Fatalf("a pre-session entry reloaded as %+v", e.Session)
	}
}

// Each launch binds the session it actually started: a dispatch resumed
// in-process after a paseo launch is no longer kept by paseo's listing, a
// hand-off to paseo after an in-process launch is kept across a restart, and
// a newer session of the dispatch is never ended by an older one's going.
func TestTheLaunchPathRebindsTheSession(t *testing.T) {
	g := newReclaimRig(t)
	backend, fb := newFakeBackend(t, "a1", "a2")
	reg := g.r.Registry
	g.live["rR"] = true
	reg.Register(Entry{Workspace: g.ws, Identity: "agent:fixer@acme/app", Dispatch: "rR:fix"})
	h := g.home("rR:fix")
	launched(h)
	reg.BindSession(g.ws, "rR:fix", SessionRef{Backend: backend, ID: "a1"})
	reg.BindSession(g.ws, "rR:fix", SessionRef{Backend: BackendInProcess})
	g.live["rR"] = false
	if g.r.ReclaimRun("rR"); exists(h) {
		t.Fatal("an in-process relaunch's home was kept by the earlier paseo session's listing")
	}

	path := filepath.Join(t.TempDir(), "registry.json")
	saved := &Registry{}
	if err := saved.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	ws2 := t.TempDir()
	saved.Register(Entry{Workspace: ws2, Identity: "agent:fixer@acme/app", Dispatch: "rS:fix"})
	saved.BindSession(ws2, "rS:fix", SessionRef{Backend: BackendInProcess})
	saved.BindSession(ws2, "rS:fix", SessionRef{Backend: backend, ID: "a1"})
	saved.BindSession(ws2, "rS:fix", SessionRef{Backend: backend, ID: "a2"}) // resumed as a new agent
	g.r = g.resolver(g.r.StateRoot)
	g.r.Registry = &Registry{}
	if err := g.r.Registry.PersistTo(path); err != nil { // the daemon restarts
		t.Fatal(err)
	}
	g.live["rS"] = true
	h2 := g.home("rS:fix")
	launched(h2)
	g.live["rS"] = false
	fb.set(nil, "a2") // the older agent is gone, the newer one lives
	g.r.ReclaimRun("rS")
	ageIndex(t, g.r)
	g.r.SweepDispatchHomes()
	if !exists(h2) {
		t.Fatal("the hand-off's home went after a restart, or with its older session")
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

// A directory the agent locked its owner out of entirely (mode 000): Linux
// opens it O_PATH and chmods that fd, so it goes; macOS can only chmod it by
// name, so it is refused — kept aside, reported once — never chmodded.
func TestReclaimOfADirectoryWithNoPermissions(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rA"] = true
	h := g.home("rA:fix")
	launched(h)
	locked := filepath.Join(h, "locked")
	os.MkdirAll(filepath.Join(locked, "in"), 0o700)
	os.WriteFile(filepath.Join(locked, "in", "f"), []byte("x"), 0o400)
	os.Chmod(filepath.Join(locked, "in"), 0)
	os.Chmod(locked, 0)
	g.live["rA"] = false
	n := g.r.ReclaimRun("rA")
	aside, _ := filepath.Glob(filepath.Join(g.r.homesDir(), reclaimPrefix+"*"))
	if runtime.GOOS != "darwin" {
		if n != 1 || exists(h) || len(aside) != 0 {
			t.Fatalf("reclaimed %d; home %v; aside %v", n, exists(h), aside)
		}
		return
	}
	t.Cleanup(func() {
		for _, a := range aside {
			os.Chmod(filepath.Join(a, "locked"), 0o700)
			os.Chmod(filepath.Join(a, "locked", "in"), 0o700)
		}
	})
	if n != 0 || exists(h) || len(aside) != 1 {
		t.Fatalf("reclaimed %d; home %v; aside %v — want it refused and kept aside", n, exists(h), aside)
	}
	if fi, err := os.Lstat(filepath.Join(aside[0], "locked")); err != nil || fi.Mode().Perm() != 0 {
		t.Fatalf("the locked directory: %v %v, want left mode 000", fi.Mode(), err)
	}
	g.r.SweepDispatchHomes()
	g.mu.Lock()
	reported := 0
	for _, l := range g.logs {
		if strings.Contains(l, "remove dispatch home") {
			reported++
		}
	}
	g.mu.Unlock()
	if reported != 1 {
		t.Fatalf("the failed removal was reported %d times, want once", reported)
	}
}

// The window between a directory's stat and its chmod cannot be won: a
// directory swapped, after it was stat'ed, for a hard link to an outside
// file, a symlink or another directory is refused, and nothing is chmodded
// — whatever mode the stat'ed directory had (readable, search-only, none).
func TestRemovalRefusesAnEntrySwappedAfterItsStat(t *testing.T) {
	for _, mode := range []os.FileMode{0o500, 0o100, 0} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "outside")
			os.WriteFile(target, []byte("precious"), 0o400)
			home := filepath.Join(root, "home")
			os.Mkdir(home, 0o755)
			os.Mkdir(filepath.Join(home, "d"), 0o700)
			os.Mkdir(filepath.Join(home, "other"), 0o500)
			t.Cleanup(func() {
				os.Chmod(filepath.Join(home, "other"), 0o700)
				os.Chmod(filepath.Join(home, "orig"), 0o700)
				os.Chmod(filepath.Join(home, "d"), 0o700)
			})
			os.Chmod(filepath.Join(home, "d"), mode)
			hfd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(hfd)
			var st unix.Stat_t
			if err := unix.Fstatat(hfd, "d", &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				t.Fatal(err)
			}
			// The stat'ed directory stays alive, aside: no swapped-in entry
			// can take its inode number.
			if err := os.Rename(filepath.Join(home, "d"), filepath.Join(home, "orig")); err != nil {
				t.Fatal(err)
			}
			swaps := []struct {
				what string
				make func(string) error
			}{
				{"hard link", func(p string) error { return os.Link(target, p) }},
				{"symlink", func(p string) error { return os.Symlink(filepath.Join(home, "other"), p) }},
				{"directory", func(p string) error {
					if err := os.Mkdir(p, 0o700); err != nil {
						return err
					}
					return os.Chmod(p, mode)
				}},
			}
			for _, sw := range swaps {
				if fi, err := os.Lstat(filepath.Join(home, "d")); err == nil && fi.IsDir() {
					os.Chmod(filepath.Join(home, "d"), 0o700)
				}
				os.Remove(filepath.Join(home, "d"))
				if err := sw.make(filepath.Join(home, "d")); err != nil {
					t.Fatal(err)
				}
				fd, err := openDirForRemoval(hfd, "d", &st)
				if err == nil {
					unix.Close(fd)
					t.Fatalf("%s swapped in: opened", sw.what)
				}
				if sw.what == "directory" {
					if fi, _ := os.Lstat(filepath.Join(home, "d")); fi.Mode().Perm() != mode {
						t.Fatalf("the swapped-in directory was chmodded to %v", fi.Mode().Perm())
					}
				}
			}
			for p, want := range map[string]os.FileMode{target: 0o400, filepath.Join(home, "other"): 0o500} {
				if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
					t.Fatalf("%s: mode %v (%v), want %v untouched", p, fi.Mode().Perm(), err, want)
				}
			}
		})
	}
}

// A tree deeper than maxRemoveDepth is refused, not walked; one within it
// goes.
func TestRemovalIsDepthBounded(t *testing.T) {
	chain := func(root string, depth int) {
		p := root
		for i := 0; i < depth; i++ {
			p = filepath.Join(p, "d")
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(p, "leaf"), []byte("x"), 0o400)
	}
	base := t.TempDir()
	ok, deep := filepath.Join(base, "ok"), filepath.Join(base, "deep")
	chain(ok, maxRemoveDepth-1)
	chain(deep, maxRemoveDepth+5)
	if _, err := removeTree(ok); err != nil || exists(ok) {
		t.Fatalf("a tree within the bound: %v, still there %v", err, exists(ok))
	}
	if _, err := removeTree(deep); !errors.Is(err, errTooDeep) {
		t.Fatalf("a tree past the bound: %v, want refused as too deep", err)
	}
}

// A renewed hold is never released by the lapsing timer of the hold it
// replaced.
func TestALapsingHoldNeverReleasesARenewedOne(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rG"] = true
	h := g.home("rG:fix")
	lock := homeLockPath(h)
	daemonHolds.Lock()
	first := daemonHolds.m[lock].gen
	daemonHolds.Unlock()
	g.home("rG:fix") // a relaunch renews the hold
	if expireHold(lock, first) {
		t.Fatal("the first hold's timer released the renewed hold")
	}
	if _, free := tryLockHomeExclusive(lock); free {
		t.Fatal("the renewed hold is gone")
	}
	releaseDaemonHold(lock)
}

// Preparing a home never waits on its lock: another process holding it
// exclusively does not stall the launch (nor every launch behind homesMu).
func TestPreparingAHomeNeverBlocksOnItsLock(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rK"] = true
	key := IdentityKey("agent:fixer@acme/app@rK:fix")
	lock := filepath.Join(g.r.homesDir(), homeLocks, key)
	os.MkdirAll(filepath.Dir(lock), 0o700)
	os.WriteFile(lock, nil, 0o600)
	release, free := tryLockHomeExclusive(lock)
	if !free {
		t.Fatal("could not take the lock")
	}
	defer release()
	done := make(chan struct{})
	go func() { g.home("rK:fix"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("preparing the home blocked on its lock")
	}
}

// A step resumed after a restart runs under a new dispatch id (its run's new
// history id) in the home its session was created in: the transcript it
// resumes is there. That home is kept while the interrupted run awaits its
// resume, belongs to the resumed run once relaunched, and goes when that run
// ends.
func TestAResumedStepRelaunchesInItsHome(t *testing.T) {
	g := newReclaimRig(t)
	g.live["rOld"] = true
	launch := func(r *Resolver, e Entry) string {
		t.Helper()
		e.Workspace, e.Identity = g.ws, "agent:fixer@acme/app"
		p, err := r.PolicyForEntry(e, "claude", nil, skill.ProcessAnchor{})
		if err != nil {
			t.Fatal(err)
		}
		return p.Home
	}
	home := launch(g.r, Entry{Dispatch: "rOld:address"})
	transcript := filepath.Join(home, ".claude", "projects", "wt", "fcd6fab7.jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte("{}\n"), 0o600)
	launched(home)
	ageIndex(t, g.r)

	// The daemon restarts: the run was interrupted and can resume. The
	// startup sweep (before ResumeWorkflows) keeps its home.
	r2 := g.resolver(g.r.StateRoot)
	if n := r2.SweepDispatchHomes(); n != 0 || !exists(transcript) {
		t.Fatalf("startup sweep removed %d; transcript kept: %v", n, exists(transcript))
	}

	// The resumed run (history id rNew) relaunches the step.
	g.live["rNew"] = true
	if got := launch(r2, Entry{Dispatch: "rNew:address", Home: "rOld:address"}); got != home {
		t.Fatalf("resumed launch home = %s, want the session's %s", got, home)
	}
	other := launch(r2, Entry{Dispatch: "rNew:other"})
	if other == home {
		t.Fatal("another step of the resumed run shares the resumed step's home")
	}
	launched(other)
	if rec := loadHomeIndex(r2.homesDir())[filepath.Base(home)]; rec.Dispatch != "rNew:address" {
		t.Fatalf("index record = %+v, want it re-indexed under the resumed run", rec)
	}
	launched(home)

	// Interrupted again: the resumed run is resumable, its home stays even
	// once the old run is no longer.
	g.live["rOld"] = false
	ageIndex(t, r2)
	if n := r2.SweepDispatchHomes(); n != 0 || !exists(transcript) {
		t.Fatalf("sweep with the resumed run resumable removed %d; transcript kept: %v", n, exists(transcript))
	}

	// The resumed run ends: its homes go, the resumed step's included.
	g.live["rNew"] = false
	if n := r2.ReclaimRun("rNew"); n != 2 || exists(home) || exists(other) {
		t.Fatalf("resumed run ended: reclaimed %d, homes kept: %v %v", n, exists(home), exists(other))
	}
}

// An out-of-process session bound in a resumed step's workspace keeps the
// home the step runs in, not one under its new dispatch id.
func TestAResumedSessionKeepsItsOwnHome(t *testing.T) {
	g := newReclaimRig(t)
	backend, _ := newFakeBackend(t, "sess-1")
	e := Entry{Workspace: g.ws, Identity: "agent:fixer@acme/app", Dispatch: "rNew:address", Home: "rOld:address"}
	g.r.Registry.Register(e)
	g.r.Registry.BindSession(g.ws, e.Dispatch, SessionRef{Backend: backend, ID: "sess-1"})
	live := g.r.liveSessionHomes()
	if !live[IdentityKey("agent:fixer@acme/app@rOld:address")] || len(live) != 1 {
		t.Fatalf("live session homes = %v", live)
	}

	// The registry a restarted daemon reloads keeps the home: a relaunch
	// found by its directory (a profile-less resume, `sandbox exec` under
	// paseo) gets the session's home, not one under the dispatch id.
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := g.r.Registry.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	g.r.Registry.Register(e)
	reloaded := &Registry{}
	if err := reloaded.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Lookup(g.ws)
	if !ok || got.Home != "rOld:address" || got.Dispatch != "rNew:address" {
		t.Fatalf("reloaded entry = %+v", got)
	}
	if _, key := g.r.homeFor(got, PersistNone); key != "agent:fixer@acme/app@rOld:address" {
		t.Fatalf("reloaded entry's home key = %s", key)
	}
}
