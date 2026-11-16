package agentsandbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Reclaiming per-dispatch homes (sandbox.persist: none). A home is kept for
// exactly as long as something can still use it:
//
//   - its run is live: running now, or interrupted by a restart and still
//     resumable (Resolver.RunLive answers for a run id);
//   - a launch is running in it: every launch holds a shared lock on the
//     home's lock file (lockHomeShared) until it ends — an agent a run left
//     running past its end (a background or hand-off step) keeps its home,
//     and a launcher that dies drops its lock with it. The daemon itself
//     holds the lock from the moment it prepares the home until the launch
//     takes it over (or LaunchGrace passes): a run that ends while its last
//     launch is starting does not lose that launch's home;
//   - a session registered in it is live: the workspace registry names the
//     dispatch, the session has not ended, and its workspace still exists —
//     an agent that outlives its launcher (paseo restarts the provider and
//     relaunches it with --resume) finds its home.
//
// When a run ends for good its homes go at once (ReclaimRun, from the run's
// end). The sweep (SweepDispatchHomes, at the daemon's start, at every run
// end and at most hourly on launches) is the backstop for an orphan — a
// home whose run ended while no daemon was there to reclaim it, or whose
// removal failed: no live run, no launch holding it.
//
// Which run a home belongs to is recorded at launch in an index conductor
// owns (.index.json beside the homes): a home is the agent's to write, so
// nothing in it is believed.

const (
	homeIndexFile = ".index.json"
	homeLocks     = ".locks"
	// reclaimPrefix names a home moved aside for removal: a removal that
	// failed (or a daemon that died during one) is retried by the sweep.
	reclaimPrefix = ".reclaim-"
)

// LaunchGrace covers the moment between the daemon preparing a home and the
// launcher taking its lock: a home launched into this recently is never an
// orphan. ReclaimRun does not wait for it — its run has ended.
var LaunchGrace = 10 * time.Minute

// homesMu serializes a per-dispatch home's preparation, its index and its
// reclaim: a home is never moved aside while a launch is being prepared in it.
// Nothing slow (a registry workspace's stat, a run's history) runs under it.
var homesMu sync.Mutex

// sweepMu runs one sweep at a time.
var sweepMu sync.Mutex

// daemonHolds are the locks the daemon holds on homes it prepared, until the
// launch takes them over (releaseDaemonHold) or LaunchGrace passes.
var daemonHolds = struct {
	sync.Mutex
	m map[string]*daemonHold
}{m: map[string]*daemonHold{}}

type daemonHold struct {
	release func()
	timer   *time.Timer
}

// holdForLaunch holds home's lock for the launch being prepared in it. When
// the hold lapses unclaimed, a sweep runs: a home its run left behind while
// the hold kept it goes then.
func (r *Resolver) holdForLaunch(home string) {
	lock := homeLockPath(home)
	if lock == "" {
		return
	}
	daemonHolds.Lock()
	defer daemonHolds.Unlock()
	if h, ok := daemonHolds.m[lock]; ok {
		h.timer.Reset(LaunchGrace)
		return
	}
	h := &daemonHold{release: lockHomeShared(lock)}
	h.timer = time.AfterFunc(LaunchGrace, func() {
		releaseDaemonHold(lock)
		r.SweepDispatchHomes()
	})
	daemonHolds.m[lock] = h
}

// releaseDaemonHold drops the daemon's hold on a home's lock (the launch
// holds its own now).
func releaseDaemonHold(lock string) {
	daemonHolds.Lock()
	h, ok := daemonHolds.m[lock]
	delete(daemonHolds.m, lock)
	daemonHolds.Unlock()
	if ok {
		h.timer.Stop()
		h.release()
	}
}

// failedOnce names the homes whose removal failed and was reported: a
// removal that keeps failing is reported once, not at every sweep.
var failedOnce sync.Map

var homeNow = time.Now

// homeRec is a per-dispatch home's index record.
type homeRec struct {
	Dispatch string    `json:"dispatch"`
	Used     time.Time `json:"used"`
}

// RunOf is the run a dispatch id belongs to: its history id, the part
// before the first ':' (flow.Runner.dispatchID).
func RunOf(dispatch string) string {
	run, _, _ := strings.Cut(dispatch, ":")
	return run
}

func (r *Resolver) homesDir() string { return filepath.Join(r.StateRoot, dispatchHomes) }

func (r *Resolver) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// homeLockPath is a per-dispatch home's lock file ("" for any other home).
func homeLockPath(home string) string {
	parent := filepath.Dir(home)
	if filepath.Base(parent) != dispatchHomes {
		return ""
	}
	return filepath.Join(parent, homeLocks, filepath.Base(home))
}

func loadHomeIndex(dir string) map[string]homeRec {
	idx := map[string]homeRec{}
	if b, err := os.ReadFile(filepath.Join(dir, homeIndexFile)); err == nil {
		_ = json.Unmarshal(b, &idx)
	}
	return idx
}

func (r *Resolver) saveHomeIndex(dir string, idx map[string]homeRec) {
	b, err := json.Marshal(idx)
	if err != nil {
		return
	}
	p := filepath.Join(dir, homeIndexFile)
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		r.logf("agent sandbox: dispatch home index: %v", err)
		return
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		r.logf("agent sandbox: dispatch home index: %v", err)
	}
}

// prepareDispatchHome runs prepare (the home's creation) under homesMu and
// records the home's dispatch, used now.
func (r *Resolver) prepareDispatchHome(key, dispatch string, prepare func() error) error {
	homesMu.Lock()
	defer homesMu.Unlock()
	if err := prepare(); err != nil {
		return err
	}
	dir := r.homesDir()
	r.holdForLaunch(filepath.Join(dir, IdentityKey(key)))
	idx := loadHomeIndex(dir)
	idx[IdentityKey(key)] = homeRec{Dispatch: dispatch, Used: homeNow()}
	r.saveHomeIndex(dir, idx)
	return nil
}

// detachHome moves dir/name aside for removal unless a launch holds it.
// Called with homesMu held; drops the home's index record and lock file.
func (r *Resolver) detachHome(dir, name string, idx map[string]homeRec) (trash string, busy bool) {
	lock := filepath.Join(dir, homeLocks, name)
	release, free := tryLockHomeExclusive(lock)
	if !free {
		return "", true
	}
	defer release()
	var b [6]byte
	_, _ = rand.Read(b[:])
	trash = filepath.Join(dir, reclaimPrefix+name+"-"+hex.EncodeToString(b[:]))
	if err := os.Rename(filepath.Join(dir, name), trash); err != nil {
		if !os.IsNotExist(err) {
			r.logf("agent sandbox: reclaim dispatch home %s: %v", name, err)
			return "", false
		}
		trash = ""
	}
	delete(idx, name)
	_ = os.Remove(lock)
	return trash, false
}

// ReclaimRun removes the homes of run's dispatches now: the run has ended
// for good (done, failed or stopped). A home a launch still holds (an agent
// the run left running) stays until the sweep finds it free. It then sweeps
// for orphans — among them the homes of the run's earlier, interrupted
// attempts. Returns the number of homes removed.
func (r *Resolver) ReclaimRun(run string) int {
	if run == "" {
		return r.SweepDispatchHomes()
	}
	dir := r.homesDir()
	registered := r.registeredHomes()
	homesMu.Lock()
	idx := loadHomeIndex(dir)
	var trash []string
	held := 0
	for name, rec := range idx {
		if RunOf(rec.Dispatch) != run {
			continue
		}
		if registered[name] {
			held++
			continue
		}
		t, busy := r.detachHome(dir, name, idx)
		if busy {
			held++
		}
		if t != "" {
			trash = append(trash, t)
		}
	}
	r.saveHomeIndex(dir, idx)
	homesMu.Unlock()
	if held > 0 {
		r.logf("agent sandbox: run %s ended; %d dispatch home(s) kept while their agent or session is live", run, held)
	}
	return r.removeHomes(trash, "run "+run+" ended") + r.SweepDispatchHomes()
}

var lastSweep atomic.Int64

// maybeSweep runs SweepDispatchHomes at most hourly, in the background.
func (r *Resolver) maybeSweep() {
	now := time.Now().Unix()
	if last := lastSweep.Load(); now-last < 3600 || !lastSweep.CompareAndSwap(last, now) {
		return
	}
	go r.SweepDispatchHomes()
}

// SweepDispatchHomes removes the orphaned per-dispatch homes (no live run,
// no launch holding them, no live session registered in them) and any left
// half-removed. Returns the number removed.
func (r *Resolver) SweepDispatchHomes() int {
	sweepMu.Lock()
	defer sweepMu.Unlock()
	dir := r.homesDir()
	if _, err := os.Stat(dir); err != nil {
		return 0
	}
	// What can be slow is asked before homesMu is taken: a launch never
	// waits on a registry workspace's filesystem or a run's history.
	registered := r.registeredHomes()
	runLive := map[string]bool{}
	if r.RunLive != nil {
		for _, rec := range loadHomeIndex(dir) {
			run := RunOf(rec.Dispatch)
			if _, ok := runLive[run]; !ok {
				runLive[run] = r.RunLive(run)
			}
		}
	}
	homesMu.Lock()
	ents, err := os.ReadDir(dir)
	if err != nil {
		homesMu.Unlock()
		return 0
	}
	idx := loadHomeIndex(dir)
	var trash []string
	present := map[string]bool{}
	for _, ent := range ents {
		name := ent.Name()
		if strings.HasPrefix(name, reclaimPrefix) {
			trash = append(trash, filepath.Join(dir, name))
			continue
		}
		if strings.HasPrefix(name, ".") || !ent.IsDir() {
			continue
		}
		present[name] = true
		rec, indexed := idx[name]
		if registered[name] || !r.orphan(filepath.Join(dir, name), rec, indexed, runLive) {
			continue
		}
		if t, _ := r.detachHome(dir, name, idx); t != "" {
			trash = append(trash, t)
		}
	}
	for name := range idx {
		if !present[name] {
			delete(idx, name)
		}
	}
	r.saveHomeIndex(dir, idx)
	homesMu.Unlock()
	return r.removeHomes(trash, "orphaned")
}

// orphan reports whether a home no launch holds and no live session names
// may go: its run is neither running nor resumable. A run whose answer was
// not asked for (indexed since) is live. A home with no index record (made
// before the index existed) goes once it is past the launch grace.
func (r *Resolver) orphan(path string, rec homeRec, indexed bool, runLive map[string]bool) bool {
	if indexed {
		if homeNow().Sub(rec.Used) < LaunchGrace || r.RunLive == nil {
			return false
		}
		live, asked := runLive[RunOf(rec.Dispatch)]
		return asked && !live
	}
	fi, err := os.Lstat(path)
	return err == nil && homeNow().Sub(fi.ModTime()) >= LaunchGrace
}

// registeredHomes are the per-dispatch homes of the registry's live
// sessions: entries with a dispatch whose session has not ended and whose
// workspace still exists.
func (r *Resolver) registeredHomes() map[string]bool {
	out := map[string]bool{}
	for _, e := range r.registry().entries() {
		if e.Dispatch == "" || e.SessionEnded {
			continue
		}
		if fi, err := os.Stat(e.Workspace); err == nil && fi.IsDir() {
			out[IdentityKey(e.Identity+"@"+e.Dispatch)] = true
		}
	}
	return out
}

// removeHomes removes homes already moved aside, and logs what it freed and
// what it could not remove (a failed one stays aside, for the next sweep).
func (r *Resolver) removeHomes(trash []string, why string) int {
	n, failed, newFail := 0, 0, false
	var freed int64
	for _, t := range trash {
		b, err := removeTree(t)
		freed += b
		if err != nil {
			failed++
			if _, told := failedOnce.LoadOrStore(filepath.Base(t), true); !told {
				newFail = true
				r.logf("agent sandbox: remove dispatch home %s: %v (kept aside; retried at each sweep, not reported again)", filepath.Base(t), err)
			}
			continue
		}
		failedOnce.Delete(filepath.Base(t))
		n++
	}
	if n > 0 || newFail {
		r.logf("agent sandbox: reclaimed %d dispatch home(s) (%s), %s freed; %d failed", n, why, humanBytes(uint64(freed)), failed)
	}
	return n
}
