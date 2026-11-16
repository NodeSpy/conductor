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
//     and a launcher that dies drops its lock with it.
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
var homesMu sync.Mutex

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
	homesMu.Lock()
	idx := loadHomeIndex(dir)
	var trash []string
	held := 0
	for name, rec := range idx {
		if RunOf(rec.Dispatch) != run {
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
		r.logf("agent sandbox: run %s ended; %d dispatch home(s) kept while their agent runs", run, held)
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
// no launch holding them) and any left half-removed. Returns the number
// removed.
func (r *Resolver) SweepDispatchHomes() int {
	dir := r.homesDir()
	homesMu.Lock()
	ents, err := os.ReadDir(dir)
	if err != nil {
		homesMu.Unlock()
		return 0
	}
	idx := loadHomeIndex(dir)
	var legacy map[string]bool
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
		if !indexed && legacy == nil {
			legacy = r.registeredHomes()
		}
		if !r.orphan(filepath.Join(dir, name), rec, indexed, legacy) {
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

// orphan reports whether a home no launch holds may go: its run is neither
// running nor resumable. A home with no index record (made before the index
// existed) goes unless a registered workspace that still exists names it.
func (r *Resolver) orphan(path string, rec homeRec, indexed bool, legacy map[string]bool) bool {
	if indexed {
		if homeNow().Sub(rec.Used) < LaunchGrace || r.RunLive == nil {
			return false
		}
		return !r.RunLive(RunOf(rec.Dispatch))
	}
	if legacy[filepath.Base(path)] {
		return false
	}
	fi, err := os.Lstat(path)
	return err == nil && homeNow().Sub(fi.ModTime()) >= LaunchGrace
}

// registeredHomes are the per-dispatch homes the registry's entries name
// whose workspace still exists.
func (r *Resolver) registeredHomes() map[string]bool {
	out := map[string]bool{}
	for _, e := range r.registry().entries() {
		if e.Dispatch == "" {
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
	n, failed := 0, 0
	var freed int64
	for _, t := range trash {
		b, err := removeTree(t)
		freed += b
		if err != nil {
			failed++
			r.logf("agent sandbox: remove dispatch home %s: %v", filepath.Base(t), err)
			continue
		}
		n++
	}
	if n > 0 || failed > 0 {
		r.logf("agent sandbox: reclaimed %d dispatch home(s) (%s), %s freed; %d failed", n, why, humanBytes(uint64(freed)), failed)
	}
	return n
}
