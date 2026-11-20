package keycache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Manager restores keyed caches into agent launches and publishes what
// their successful runs filled.
type Manager struct {
	Store *Store
	// Protected are further home paths no cache may hold, hide or sit
	// inside (the agent tools' state, agentsandbox.ToolStatePaths), joined
	// to alwaysProtected.
	Protected []string
	// MaxSize caps the store (0 = no cap).
	MaxSize int64
	// CopyMax caps a restore that copies (no overlay, no clone): a larger
	// cache is a miss.
	CopyMax int64
	// MinSize / MinRuns are the learning threshold: a directory becomes one
	// of a key's paths once MinRuns successful runs of the key each wrote at
	// least MinSize there (DefaultMinSize, DefaultMinRuns when zero).
	MinSize int64
	MinRuns int
	// FinishWait is how long Finish waits for a launch of the dispatch to
	// stop (0: 10s).
	FinishWait time.Duration
	// Overlay overrides the overlay probe (tests).
	Overlay func() (bool, string)
	// Logf logs.
	Logf func(format string, args ...any)

	mu   sync.Mutex
	pins map[string]int
	// testHook is called at points tests interleave with.
	testHook func(point string)
}

// Learning defaults.
const (
	DefaultMinSize = 50 << 20
	DefaultMinRuns = 2
)

// Request is one agent launch asking for its caches.
type Request struct {
	Workspace string
	Home      string
	// Run names the launch in logs; Dispatch is the dispatch whose step's
	// end publishes (Finish).
	Run      string
	Dispatch string
	// Key is the step's own key: restored from, and the only key the
	// launch's run publishes to. RestoreFrom are read-only fallbacks, in
	// order, restored only when Key has no published copy.
	Key         Key
	RestoreFrom []Key
}

// Mount is one overlay a launch's jail mounts: Target shows Lower
// read-only with the run's writes in Upper.
type Mount struct {
	Target string `json:"target"`
	Lower  string `json:"lower"`
	Upper  string `json:"upper"`
	Work   string `json:"work"`
}

// Grant is what a launch gets: overlays to mount, and its ledger lock. The
// launch holds the lock shared while it runs — the caches it overlays are
// in use so long, and no longer — and marks it ended (EndLaunch) when it
// stops.
type Grant struct {
	Mounts []Mount
	Lock   string
}

// slot is one overlaid path, recorded in the launch's entry for its
// publish: what it showed (Entry, the version; Path, home-relative) and
// where the run's writes went.
type slot struct {
	Path  string `json:"path"`
	Entry string `json:"entry"`
	Upper string `json:"upper"`
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

func (m *Manager) minSize() int64 {
	if m.MinSize > 0 {
		return m.MinSize
	}
	return DefaultMinSize
}

func (m *Manager) minRuns() int {
	if m.MinRuns > 0 {
		return m.MinRuns
	}
	return DefaultMinRuns
}

func (m *Manager) protected() []string { return protectedList(m.Protected) }

// Restore gives a launch its step's cache: the newest published copy of
// its key, else of the first restore_from key that has one, shown at that
// copy's paths in the home — an overlay over a read-only lower layer, or a
// copy-on-write clone. It records the launch in the ledger (its key, its
// dispatch, its layers) whether or not anything was restored: the run's
// writes are what its step's end publishes. Nothing it does writes a
// published copy.
func (m *Manager) Restore(ctx context.Context, r Request) (Grant, error) {
	if err := r.Key.Valid(); err != nil {
		return Grant{}, err
	}
	if !filepath.IsAbs(r.Home) || !filepath.IsAbs(r.Workspace) {
		return Grant{}, fmt.Errorf("cache: home and workspace must be absolute")
	}
	if r.Dispatch == "" {
		return Grant{}, fmt.Errorf("cache: a launch with no dispatch has no step to publish it")
	}
	from := []Key{r.Key}
	for _, k := range r.RestoreFrom {
		if k.Valid() == nil {
			from = append(from, k)
		}
	}
	overlay, why := m.overlay()
	var g Grant
	var err error
	var copies []copyJob
	m.Store.Locked(func() {
		g, copies, err = m.restoreLocked(r, from, overlay)
	})
	if err != nil {
		return Grant{}, err
	}
	for _, j := range copies {
		n := 0
		for _, it := range j.items {
			if m.testHook != nil {
				m.testHook("copy")
			}
			if err := m.copyIn(r.Home, it.lower, it.target); err != nil {
				m.logf("cache %s: ~/%s not restored: %v", r.Key, it.rel, err)
				continue
			}
			n++
		}
		m.unpin(j.e.Dir)
		_ = os.Remove(filepath.Join(j.launch, launchCopyingFile))
		if n > 0 {
			note := ""
			if why != "" {
				note = " — " + why
			}
			m.logf("cache %s: %s %s, %d path(s) by copy%s (run %s)", r.Key, j.kind, j.e.Key, n, note, r.Run)
		}
	}
	return g, nil
}

// copyJob is a copy restore found under the store's lock (its version
// pinned) and done after it.
type copyJob struct {
	e      Entry
	kind   string
	items  []copyItem
	launch string
}

type copyItem struct {
	rel, lower, target string
}

func (m *Manager) restoreLocked(r Request, from []Key, overlay bool) (Grant, []copyJob, error) {
	// The launch's entry and overlay layers are its own, beside the store:
	// mount(2) takes the upper and work directories as paths and follows a
	// symlink in them, so they are never where an agent can write, and
	// never another launch's (launches can share a home).
	id := r.Key.ID()
	if err := os.MkdirAll(m.Store.keyDir(id), 0o700); err != nil {
		return Grant{}, nil, fmt.Errorf("cache: %w", err)
	}
	if err := writeKeyFile(m.Store.keyDir(id), r.Key); err != nil {
		return Grant{}, nil, fmt.Errorf("cache: %w", err)
	}
	m.Store.touchKey(id)
	launch, err := m.Store.newLaunch(launchRecord{home: r.Home, key: id, dispatch: r.Dispatch})
	if err != nil {
		return Grant{}, nil, fmt.Errorf("cache: %w", err)
	}
	g := Grant{Lock: LaunchLock(launch)}
	if prev, ok := m.resumes(r, id, launch); ok {
		return m.takeOver(r, prev, launch, overlay, g)
	}
	var e Entry
	found, kind := false, "hit"
	for i, k := range from {
		if e, found = m.Store.Lookup(k.ID()); found {
			if i > 0 {
				kind = "restored from " + k.String()
			}
			break
		}
	}
	if !found {
		m.logf("cache %s: miss (run %s)", r.Key, r.Run)
		return g, nil, nil
	}
	var slots []slot
	var copies []copyJob
	job := copyJob{e: e, kind: kind}
	var targets []string
	for _, rel := range e.Paths {
		if !CheckHomePath(rel, m.Protected) {
			m.logf("cache %s: ~/%s is not a path a cache may have (skipped)", r.Key, rel)
			continue
		}
		lower := e.Lower(rel)
		if lower == "" {
			continue
		}
		target := filepath.Join(r.Home, filepath.FromSlash(rel))
		if clash := overlapsAny(target, targets); clash != "" {
			continue
		}
		if !overlay {
			job.items = append(job.items, copyItem{rel: rel, lower: lower, target: target})
			targets = append(targets, target)
			continue
		}
		if err := prepareTarget(r.Home, target, false); err != nil {
			m.logf("cache %s: ~/%s not restored: %v", r.Key, rel, err)
			continue
		}
		sd := filepath.Join(launch, slotName(e.Dir, rel))
		if err := m.slot(sd); err != nil {
			_ = RemoveAll(launch)
			return Grant{}, nil, fmt.Errorf("cache: %s: %w", sd, err)
		}
		g.Mounts = append(g.Mounts, Mount{Target: target, Lower: lower,
			Upper: filepath.Join(sd, "upper"), Work: filepath.Join(sd, "work")})
		slots = append(slots, slot{Path: rel, Entry: e.Dir, Upper: filepath.Join(sd, "upper")})
		targets = append(targets, target)
	}
	if len(job.items) > 0 {
		// Pinned in this process and on disk (the launch's entry, live
		// through the copy): `caches clear` in another process sees it.
		m.pin(e.Dir)
		if err := writeAtomic(filepath.Join(launch, launchCopyingFile), []byte(e.Dir+"\n")); err != nil {
			m.unpin(e.Dir)
			_ = RemoveAll(launch)
			return Grant{}, nil, err
		}
		job.launch = launch
		copies = append(copies, job)
		m.Store.Touch(e)
	}
	if len(g.Mounts) > 0 {
		if err := writeUses(launch, []string{e.Dir}); err != nil {
			_ = RemoveAll(launch)
			return Grant{}, nil, err
		}
		b, _ := json.Marshal(slots)
		if err := writeAtomic(filepath.Join(launch, launchSlotsFile), b); err != nil {
			_ = RemoveAll(launch)
			return Grant{}, nil, err
		}
		m.Store.Touch(e)
		m.logf("cache %s: %s %s, %d path(s) by overlay (run %s)", r.Key, kind, e.Version, len(g.Mounts), r.Run)
	}
	if m.testHook != nil {
		m.testHook("restored")
	}
	return g, copies, nil
}

// resumes finds the launch a new launch continues: an ended launch of the
// same dispatch, in the same home, for the same key (a resumed session, an
// output_schema re-run), no longer running. Its lock is returned held
// exclusively, with its directory. The caller holds the store's lock.
func (m *Manager) resumes(r Request, id, self string) (heldLaunch, bool) {
	var found []string
	for _, l := range m.Store.launches() {
		if l.dir == self || l.ended.IsZero() {
			continue
		}
		if readSmall(filepath.Join(l.dir, launchDispatchFile)) != r.Dispatch ||
			readSmall(filepath.Join(l.dir, launchHomeFile)) != r.Home ||
			readSmall(filepath.Join(l.dir, launchKeyFile)) != id {
			continue
		}
		found = append(found, l.dir)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(found))) // the newest first
	for _, d := range found {
		if release, ok := tryLock(filepath.Join(d, launchLockFile)); ok {
			return heldLaunch{dir: d, release: release}, true
		}
	}
	return heldLaunch{}, false
}

// takeOver gives a new launch what the launch it continues (prev, held)
// had: its overlay layers, moved over whole, so the new launch sees what
// the first wrote and its step's end publishes both; and nothing restored
// anew — a copy restore already put its cache in the home, and the run's
// writes there are the run's. prev's entry is removed. The caller holds
// the store's lock.
func (m *Manager) takeOver(r Request, prev heldLaunch, launch string, overlay bool, g Grant) (Grant, []copyJob, error) {
	defer prev.release()
	var slots []slot
	if b, err := os.ReadFile(filepath.Join(prev.dir, launchSlotsFile)); err == nil {
		_ = json.Unmarshal(b, &slots)
	}
	var kept []slot
	var used []string
	for _, s := range slots {
		if !overlay || !CheckHomePath(s.Path, m.Protected) || !within(s.Upper, prev.dir) || !within(s.Entry, m.Store.keysRoot()) {
			continue
		}
		e, err := m.Store.read(filepath.Base(filepath.Dir(s.Entry)), filepath.Base(s.Entry))
		if err != nil || e.Dir != s.Entry {
			continue // its version is gone: the layer means nothing without it
		}
		lower := e.Lower(s.Path)
		target := filepath.Join(r.Home, filepath.FromSlash(s.Path))
		if lower == "" || prepareTarget(r.Home, target, false) != nil {
			continue
		}
		name := filepath.Base(filepath.Dir(s.Upper))
		sd := filepath.Join(launch, name)
		if err := os.Rename(filepath.Join(prev.dir, name), sd); err != nil {
			continue
		}
		// A fresh work directory: the old one is the overlay's scratch.
		work := filepath.Join(sd, "work")
		if err := RemoveAll(work); err != nil || os.Mkdir(work, 0o700) != nil {
			continue
		}
		g.Mounts = append(g.Mounts, Mount{Target: target, Lower: lower, Upper: filepath.Join(sd, "upper"), Work: work})
		kept = append(kept, slot{Path: s.Path, Entry: s.Entry, Upper: filepath.Join(sd, "upper")})
		if !slices.Contains(used, s.Entry) {
			used = append(used, s.Entry)
		}
	}
	if len(kept) > 0 {
		if err := writeUses(launch, used); err != nil {
			_ = RemoveAll(launch)
			return Grant{}, nil, err
		}
		b, _ := json.Marshal(kept)
		if err := writeAtomic(filepath.Join(launch, launchSlotsFile), b); err != nil {
			_ = RemoveAll(launch)
			return Grant{}, nil, err
		}
	}
	_ = RemoveAll(prev.dir)
	m.logf("cache %s: continues launch %s, %d path(s) by overlay (run %s)", r.Key, filepath.Base(prev.dir), len(g.Mounts), r.Run)
	if m.testHook != nil {
		m.testHook("restored")
	}
	return g, nil, nil
}

func (m *Manager) overlay() (bool, string) {
	if m.Overlay != nil {
		return m.Overlay()
	}
	return OverlayUsable(m.Store.Dir, m.Store.layersRoot())
}

// slot makes a fresh overlay upper and work directory in a launch's own
// layers directory.
func (m *Manager) slot(dir string) error {
	for _, d := range []string{dir, filepath.Join(dir, "upper"), filepath.Join(dir, "work")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func slotName(entryDir, rel string) string {
	h := sha256.Sum256([]byte(entryDir + "\x00" + rel))
	return hex.EncodeToString(h[:])[:20]
}

func overlapsAny(p string, list []string) string {
	for _, q := range list {
		if overlaps(filepath.ToSlash(p), filepath.ToSlash(q)) {
			return q
		}
	}
	return ""
}

func (m *Manager) pin(dir string) {
	m.mu.Lock()
	if m.pins == nil {
		m.pins = map[string]int{}
	}
	m.pins[dir]++
	m.mu.Unlock()
}

func (m *Manager) unpin(dir string) {
	m.mu.Lock()
	if m.pins[dir]--; m.pins[dir] <= 0 {
		delete(m.pins, dir)
	}
	m.mu.Unlock()
}

func (m *Manager) pinned(dir string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pins[dir] > 0
}

// InUse reports a published version a live launch uses.
func (m *Manager) InUse(dir string) bool {
	return m.pinned(dir) || m.Store.Used()[dir]
}

// Published is what Finish published.
type Published struct {
	Key   Key
	Entry Entry
	// Home is the launch's home, Dispatch its dispatch.
	Home, Dispatch string
}

// Finish ends a dispatch's caching at its step's end. ok: the step's run
// succeeded — what its newest launch filled is published to the launch's
// own key (and nowhere else), and the key learns from it. Not ok: nothing
// is published. Either way the dispatch's ended launches' layers are
// removed. A launch still running after FinishWait is left alone (its
// layers go when it ends and KeepEnded passes). nil, nil: nothing was
// published.
func (m *Manager) Finish(ctx context.Context, dispatch string, ok bool) (*Published, error) {
	if dispatch == "" {
		return nil, nil
	}
	var held []heldLaunch
	defer func() {
		for _, h := range held {
			_ = RemoveAll(h.dir)
			h.release()
		}
	}()
	for _, l := range m.Store.launches() {
		if readSmall(filepath.Join(l.dir, launchDispatchFile)) != dispatch {
			continue
		}
		release, got := m.waitLock(ctx, filepath.Join(l.dir, launchLockFile))
		if !got {
			m.logf("cache: a launch of %s is still running %s after its step ended: not published", dispatch, m.finishWait())
			continue
		}
		// Whether it ran is read once its lock is held: it may have ended
		// while this waited.
		var ended time.Time
		if fi, err := os.Lstat(filepath.Join(l.dir, launchEndedFile)); err == nil {
			ended = fi.ModTime()
		}
		held = append(held, heldLaunch{dir: l.dir, release: release, ended: ended})
	}
	if !ok || len(held) == 0 {
		return nil, nil
	}
	// The newest launch that ran is the one the step ended with.
	sort.Slice(held, func(i, j int) bool { return held[i].dir > held[j].dir })
	var last *heldLaunch
	for i := range held {
		if !held[i].ended.IsZero() {
			last = &held[i]
			break
		}
	}
	if last == nil {
		return nil, nil
	}
	return m.publish(ctx, last.dir)
}

type heldLaunch struct {
	dir     string
	release func()
	ended   time.Time
}

func (m *Manager) finishWait() time.Duration {
	if m.FinishWait > 0 {
		return m.FinishWait
	}
	return 10 * time.Second
}

// waitLock takes a launch's lock exclusively, waiting up to FinishWait for
// the launch to stop.
func (m *Manager) waitLock(ctx context.Context, p string) (func(), bool) {
	deadline := time.Now().Add(m.finishWait())
	for {
		if release, ok := tryLock(p); ok {
			return release, true
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// publish builds and publishes a version of an ended launch's key from what
// the launch filled. The caller holds the launch's lock exclusively (so the
// launch counts as live: what it overlaid stays in use, never evicted,
// while this reads it).
func (m *Manager) publish(ctx context.Context, launch string) (*Published, error) {
	home := readSmall(filepath.Join(launch, launchHomeFile))
	id := readSmall(filepath.Join(launch, launchKeyFile))
	if !filepath.IsAbs(home) || !idRE.MatchString(id) {
		return nil, fmt.Errorf("cache: launch %s: no home or key recorded", filepath.Base(launch))
	}
	var key Key
	if b, err := os.ReadFile(filepath.Join(m.Store.keyDir(id), "key.json")); err != nil || json.Unmarshal(b, &key) != nil || key.ID() != id {
		return nil, fmt.Errorf("cache: launch %s: its key is not recorded", filepath.Base(launch))
	}
	var slots []slot
	if b, err := os.ReadFile(filepath.Join(launch, launchSlotsFile)); err == nil {
		if err := json.Unmarshal(b, &slots); err != nil {
			return nil, fmt.Errorf("cache %s: slots: %w", key, err)
		}
	}
	bySlot := map[string]slot{}
	for _, s := range slots {
		if CheckHomePath(s.Path, m.Protected) && within(s.Upper, launch) && within(s.Entry, m.Store.keysRoot()) {
			bySlot[s.Path] = s
		}
	}
	// What the run wrote under each candidate: its overlay's upper layer
	// where it had one, else the home's directory.
	written := map[string]int64{}
	for _, c := range candidates(home, m.protected()) {
		if s, ok := bySlot[c]; ok {
			written[c] = DiskUsage(s.Upper)
		} else {
			written[c] = DiskUsage(filepath.Join(home, filepath.FromSlash(c)))
		}
	}
	for p, s := range bySlot {
		if _, ok := written[p]; !ok {
			written[p] = DiskUsage(s.Upper)
		}
	}
	var learned []string
	m.Store.Locked(func() {
		learned = m.Store.learn(key.TemplateID(), written, m.minSize(), m.minRuns(), m.Protected)
	})
	set := map[string]bool{}
	for _, p := range learned {
		set[p] = true
	}
	for p := range bySlot {
		set[p] = true
	}
	paths := outermost(set)
	if len(paths) == 0 {
		m.logf("cache %s: nothing learned yet to publish (run %s)", key, filepath.Base(launch))
		return nil, nil
	}
	// What the version would take, before anything is copied: whole, and
	// new (what it holds of the version it restored is hard-linked, and
	// that version is in use: counted already).
	var est, fresh int64
	for _, p := range paths {
		w, f := m.estimate(home, p, bySlot)
		est, fresh = est+w, fresh+f
	}
	if m.MaxSize > 0 {
		if est > m.MaxSize {
			m.logf("cache %s: %s is more than the store's cap (%s): not published", key, HumanSize(est), HumanSize(m.MaxSize))
			return nil, nil
		}
		if room := m.room(); fresh > room {
			m.logf("cache %s: %s is more than the room left beside the caches in use (%s): not published", key, HumanSize(fresh), HumanSize(max(room, 0)))
			return nil, nil
		}
	}
	budget := int64(1 << 62)
	if m.MaxSize > 0 {
		budget = m.MaxSize
	}
	st, err := m.Store.newStage(key)
	if err != nil {
		return nil, err
	}
	var built []string
	for _, p := range paths {
		dst := filepath.Join(st.Data, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			st.abort()
			return nil, err
		}
		if err := m.build(home, p, dst, bySlot, &budget); err != nil {
			_ = RemoveAll(dst)
			if errors.Is(err, errCopyCap) {
				st.abort()
				m.logf("cache %s: larger than the store's cap while publishing: not published", key)
				return nil, nil
			}
			m.logf("cache %s: ~/%s not published: %v", key, p, err)
			continue
		}
		built = append(built, p)
	}
	if len(built) == 0 {
		st.abort()
		return nil, nil
	}
	// On disk before it is named: a crash never leaves a published copy
	// whose files were not written back.
	syncFS(st.tmp)
	if m.testHook != nil {
		m.testHook("built")
	}
	var e Entry
	m.Store.Locked(func() {
		e, err = st.commit(Meta{Paths: built, Run: filepath.Base(launch)})
		if err == nil {
			m.Store.removeSuperseded(id, m.InUse)
		}
	})
	if err != nil {
		return nil, err
	}
	m.logf("cache %s: published %s (~/%s, %s)", key, e.Version, strings.Join(built, " ~/"), HumanSize(e.Size))
	m.Evict()
	return &Published{Key: key, Entry: e, Home: home, Dispatch: readSmall(filepath.Join(launch, launchDispatchFile))}, nil
}

// estimate is what building p would take: whole, and new (the restored
// version's files it keeps are hard-linked, not copied).
func (m *Manager) estimate(home, p string, bySlot map[string]slot) (whole, fresh int64) {
	if s, ok := bySlot[p]; ok {
		up := DiskUsage(s.Upper)
		return DiskUsage(filepath.Join(s.Entry, "data", filepath.FromSlash(p))) + up, up
	}
	// Copied out of the home: charged at what the copy may write.
	n := copyUsage(filepath.Join(home, filepath.FromSlash(p)))
	f := n
	for q, s := range bySlot {
		if strings.HasPrefix(q, p+"/") {
			up := DiskUsage(s.Upper)
			n += DiskUsage(filepath.Join(s.Entry, "data", filepath.FromSlash(q))) + up
			f += up
		}
	}
	return n, f
}

// build makes dst what the launch saw at ~/p: the overlay's merge where it
// had one at p, else the home's directory (copied by fd), with any
// overlay below p merged in its place.
func (m *Manager) build(home, p, dst string, bySlot map[string]slot, budget *int64) error {
	if s, ok := bySlot[p]; ok {
		return mergeLayers(lowerOf(s), s.Upper, dst, budget)
	}
	if err := copyFromHome(home, p, dst, budget); err != nil {
		return err
	}
	var inner []string
	for q := range bySlot {
		if strings.HasPrefix(q, p+"/") {
			inner = append(inner, q)
		}
	}
	sort.Strings(inner)
	for _, q := range inner {
		s := bySlot[q]
		t := filepath.Join(dst, filepath.FromSlash(strings.TrimPrefix(q, p+"/")))
		if !within(t, dst) {
			continue
		}
		if err := removeIfAny(t); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(t), 0o700); err != nil {
			return err
		}
		if err := mergeLayers(lowerOf(s), s.Upper, t, budget); err != nil {
			return err
		}
	}
	return nil
}

func lowerOf(s slot) string {
	d := filepath.Join(s.Entry, "data", filepath.FromSlash(s.Path))
	if fi, err := os.Lstat(d); err == nil && fi.IsDir() {
		return d
	}
	return ""
}

// room is what the cap leaves beside the versions live launches use.
func (m *Manager) room() int64 {
	used := m.Store.Used()
	var inUse []Entry
	for _, e := range m.Store.List() {
		if used[e.Dir] || m.pinned(e.Dir) {
			inUse = append(inUse, e)
		}
	}
	return m.MaxSize - usageOf(inUse).total
}

// Evict trims the store to MaxSize: superseded versions first, then the
// least recently restored, never one in use. A live launch's overlay
// layers do not count (they are its own writes, bounded by the disk guard
// on the store's filesystem, and gone when it ends), so one launch's
// writes never evict other caches. The layers of launches that have ended
// go first: those past KeepEnded always, those still waiting for their
// step when the store is over the cap. The check of what is in use and the
// removal are one critical section with a restore's lookup and record.
func (m *Manager) Evict() {
	var evicted []Entry
	m.Store.Locked(func() {
		m.Store.sweepLaunches(false)
		limit := int64(1 << 62) // no cap: only superseded versions go
		if m.MaxSize > 0 {
			limit = m.MaxSize
			if m.Store.Size()+m.Store.endedLayers() > m.MaxSize {
				m.Store.sweepLaunches(true)
			}
		}
		used := m.Store.Used()
		if m.testHook != nil {
			m.testHook("evict-used")
		}
		evicted = m.Store.Evict(limit, func(d string) bool { return m.pinned(d) || used[d] })
		named := map[string]bool{}
		for _, l := range m.Store.launches() {
			named[readSmall(filepath.Join(l.dir, launchKeyFile))] = true
		}
		m.Store.sweepKeys(named)
	})
	for _, e := range evicted {
		m.logf("cache %s: evicted %s (%s, last used %s)", e.Key, e.Version, HumanSize(e.Size), e.LastUsed.Format(time.RFC3339))
	}
}

// endedLayers is the disk the layers of launches that are not live take.
func (s *Store) endedLayers() int64 {
	var n int64
	for _, l := range s.launches() {
		if !l.live {
			n += DiskUsage(l.dir)
		}
	}
	return n
}

// Run is Start, waiting until the manager has stopped.
func (m *Manager) Run(ctx context.Context) { <-m.Start(ctx) }

// Start runs the manager until ctx ends: it removes what an older
// conductor's recipe caches left in the store, then evicts hourly. The
// channel closes once it has stopped.
func (m *Manager) Start(ctx context.Context) <-chan struct{} {
	var gone []string
	m.Store.Locked(func() { gone = m.Store.removeLegacy() })
	if len(gone) > 0 {
		m.logf("cache: removed %d entr(ies) an older conductor's repository caches left in %s", len(gone), m.Store.Dir)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Evict()
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m.Evict()
			}
		}
	}()
	return done
}

// HumanSize formats a byte count.
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
