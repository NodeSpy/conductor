package repocache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Manager restores caches into agent launches and warms them.
type Manager struct {
	Store *Store
	// Repos / Defaults are the operator's definitions
	// (sandbox.caches.repos, .defaults).
	Repos    map[string][]Cache
	Defaults []Cache
	// Recipes are the bundled recipes merged with the operator's.
	Recipes []Recipe
	// Autodetect detects recipes in a checkout; RepoFile reads the
	// repository's own file (from its default branch).
	Autodetect, RepoFileOn bool
	// Protected are home paths no cache may touch (the agent tools' state).
	Protected []string
	// MaxSize caps the store (0 = no cap).
	MaxSize int64
	// WarmOnMiss starts a warm when a launch misses its key.
	WarmOnMiss bool
	// WarmEvery re-warms every known repository on a schedule (0 = never).
	WarmEvery time.Duration
	// WarmTimeout bounds one warm command.
	WarmTimeout time.Duration
	// CopyMax caps a restore that copies (no overlay, no clone): a larger
	// cache is a miss.
	CopyMax int64

	// DefaultFile reads a file at the root of repo's default branch: nil
	// data when there is none. Only ever the default branch.
	DefaultFile func(ctx context.Context, repo, name string) (data []byte, commit string, err error)
	// Checkout writes a checkout of repo's default branch at dst (absent)
	// and returns its commit; never a PR head.
	Checkout func(ctx context.Context, repo, dst string) (commit string, err error)
	// Warm runs a warm command in the agent sandbox.
	Warm func(ctx context.Context, w WarmRun) error
	// Overlay overrides the overlay probe (tests).
	Overlay func() (bool, string)
	// Logf logs.
	Logf func(format string, args ...any)

	mu       sync.Mutex
	pins     map[string]int
	warming  map[string]bool
	lastWarm map[string]time.Time
	files    map[string]repoFileCache
	warned   map[string]bool
	// noRoom holds keys whose warm fitted the cap but not the room beside
	// the caches in use, with the size they need.
	noRoom map[string]int64
	// testHook is called at points tests interleave with.
	testHook func(point string)
	wg       sync.WaitGroup
	// base is the context background warms run under: Run's, so stopping
	// the manager cancels them.
	base context.Context
}

// WarmRun is one warm command for the sandbox to run.
type WarmRun struct {
	Repo, Cache string
	// Workspace is the default-branch checkout, Home a fresh home.
	Workspace, Home string
	Env             []string
	Command         string
	Timeout         time.Duration
}

type repoFileCache struct {
	file *RepoFile
	at   time.Time
}

// repoFileTTL is how long a read of the default branch's repo file is
// reused.
const repoFileTTL = 10 * time.Minute

// minWarmGap is the least time between two on-demand warms of a repo.
const minWarmGap = 10 * time.Minute

// Request is one agent launch asking for its caches.
type Request struct {
	Repo      string
	Workspace string
	Home      string
	// Run names the launch in logs (its dispatch).
	Run string
}

// Mount is one overlay a launch's jail mounts: Target shows Lower
// read-only with the run's writes in Upper.
type Mount struct {
	Target string `json:"target"`
	Lower  string `json:"lower"`
	Upper  string `json:"upper"`
	Work   string `json:"work"`
}

// Grant is what a launch gets: overlays to mount and environment. Lock is
// the launch's ledger lock ("" when it has no overlay): the launch holds it
// shared while it runs — the caches it overlays are in use so long, and no
// longer — and removes its layers (the lock's directory) when it ends.
type Grant struct {
	Mounts []Mount
	Env    []string
	Lock   string
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

func (m *Manager) init() {
	if m.pins == nil {
		m.pins = map[string]int{}
		m.warming = map[string]bool{}
		m.lastWarm = map[string]time.Time{}
		m.files = map[string]repoFileCache{}
		m.warned = map[string]bool{}
		m.noRoom = map[string]int64{}
	}
}

// Defs resolves repo's cache definitions, detecting recipes in the
// checkout at root. file is the repo file to use (nil: none).
func (m *Manager) Defs(repo, root string, file *RepoFile) ([]Def, []string) {
	return Resolve(Sources{
		Repo: m.Repos[repo], Defaults: m.Defaults, File: file, Recipes: m.Recipes,
		Detect:     func(g []string) bool { return DetectIn(root, g) },
		Autodetect: m.Autodetect, Protected: m.Protected,
	})
}

// repoFile is the default branch's repo file (cached), never the run's.
func (m *Manager) repoFile(ctx context.Context, repo string) *RepoFile {
	if !m.RepoFileOn || m.DefaultFile == nil {
		return nil
	}
	m.mu.Lock()
	m.init()
	c, ok := m.files[repo]
	m.mu.Unlock()
	if ok && time.Since(c.at) < repoFileTTL {
		return c.file
	}
	b, commit, err := m.DefaultFile(ctx, repo, RepoFileName)
	var rf *RepoFile
	if err != nil {
		m.logf("repo cache %s: read %s from the default branch: %v", repo, RepoFileName, err)
	} else if b != nil {
		f, warn, perr := ParseRepoFile(b)
		m.warnOnce(repo+"@"+commit, warn)
		if perr != nil {
			m.warnOnce(repo+"@"+commit+"!", []string{perr.Error() + " (ignored)"})
		} else {
			rf = &f
		}
	}
	m.mu.Lock()
	m.files[repo] = repoFileCache{file: rf, at: time.Now()}
	m.mu.Unlock()
	return rf
}

func (m *Manager) warnOnce(key string, warn []string) {
	if len(warn) == 0 {
		return
	}
	m.mu.Lock()
	m.init()
	seen := m.warned[key]
	m.warned[key] = true
	m.mu.Unlock()
	if !seen {
		for _, w := range warn {
			m.logf("repo cache %s: %s", strings.SplitN(key, "@", 2)[0], w)
		}
	}
}

// Restore gives a launch its repository's caches: each cache's key is
// rendered over the launch's checkout, the newest matching published cache
// is shown at its paths — an overlay over a read-only lower layer, or a
// copy-on-write clone — and its environment is set. Nothing it does writes
// a published cache. A miss may start a warm of the default branch.
func (m *Manager) Restore(ctx context.Context, r Request) (Grant, error) {
	var g Grant
	if err := CheckRepo(r.Repo); err != nil {
		return g, err
	}
	if !filepath.IsAbs(r.Home) || !filepath.IsAbs(r.Workspace) {
		return g, fmt.Errorf("repo cache: home and workspace must be absolute")
	}
	defs, warn := m.Defs(r.Repo, r.Workspace, m.repoFile(ctx, r.Repo))
	m.warnOnce(r.Repo+"@defs", warn)
	if len(defs) == 0 {
		return g, nil
	}
	// Keys are rendered over the checkout before the store's lock: hashing a
	// large tree never holds up another repository's launches.
	plans := make([]restorePlan, 0, len(defs))
	for _, d := range defs {
		pl := restorePlan{d: d}
		if m.testHook != nil {
			m.testHook("key")
		}
		if pl.key, pl.err = RenderKey(d.Key, r.Workspace); pl.err == nil {
			for _, t := range d.RestoreKeys {
				if p, err := renderPrefix(t, r.Workspace); err == nil {
					pl.prefixes = append(pl.prefixes, p)
				}
			}
		}
		plans = append(plans, pl)
	}
	overlay, why := m.overlay()
	var err error
	var copies []copyJob
	needWarm := false
	// The lookup and the record of what this launch uses are one critical
	// section with eviction: a cache found here is never one being removed.
	// A copy restore pins its entry here and copies after the lock.
	m.Store.Locked(func() {
		g, copies, needWarm, err = m.restoreLocked(r, plans, overlay)
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
			if err := m.copyIn(it.lower, it.base, it.target); err != nil {
				m.logf("repo cache %s %s: %s not restored: %v", r.Repo, j.d.Name, it.p, err)
				continue
			}
			n++
		}
		m.unpin(j.e.Dir)
		if n > 0 {
			note := ""
			if why != "" {
				note = " — " + why
			}
			m.logf("repo cache %s %s (%s): %s %s for %s, %d path(s) by copy%s (run %s)", r.Repo, j.d.Name, j.d.Origin, j.kind, j.e.Key, j.key, n, note, r.Run)
		}
	}
	if needWarm && m.WarmOnMiss {
		m.WarmAsync(r.Repo)
	}
	return g, nil
}

// restorePlan is one cache's key, rendered before the store's lock.
type restorePlan struct {
	d        Def
	key      string
	prefixes []string
	err      error
}

// copyJob is a copy restore found under the store's lock (its entry
// pinned) and done after it.
type copyJob struct {
	d     Def
	e     Entry
	kind  string
	key   string
	items []copyItem
}

type copyItem struct {
	p                   Path
	lower, base, target string
}

func (m *Manager) restoreLocked(r Request, plans []restorePlan, overlay bool) (g Grant, copies []copyJob, needWarm bool, err error) {
	// The launch's overlay layers and ledger entry are its own, beside the
	// store: mount(2) takes the upper and work directories as paths and
	// follows a symlink in them, so they are never where an agent can
	// write, and never another launch's (launches can share a home).
	var launch string
	if overlay {
		if launch, err = m.Store.newLaunch(r.Home); err != nil {
			return Grant{}, nil, false, fmt.Errorf("repo cache: %w", err)
		}
	}
	fail := func(err error) (Grant, []copyJob, bool, error) {
		if launch != "" {
			_ = RemoveAll(launch)
		}
		for _, j := range copies {
			m.unpin(j.e.Dir)
		}
		return Grant{}, nil, false, err
	}
	var used []string
	var targets []string
	for _, pl := range plans {
		d, key := pl.d, pl.key
		g.Env = append(g.Env, d.ExpandEnv(r.Home, r.Workspace)...)
		if pl.err != nil {
			m.logf("repo cache %s %s: key: %v (not restored)", r.Repo, d.Name, pl.err)
			continue
		}
		e, kind, ok := m.Store.Lookup(r.Repo, d.Name, key, pl.prefixes)
		if !ok {
			m.logf("repo cache %s %s (%s): miss for %s (run %s)", r.Repo, d.Name, d.Origin, key, r.Run)
			needWarm = needWarm || m.wantWarm(r.Repo, d, key)
			continue
		}
		if kind != Exact {
			needWarm = needWarm || m.wantWarm(r.Repo, d, key)
		}
		job := copyJob{d: d, e: e, kind: kind, key: key}
		n := 0
		for _, p := range d.ParsedPaths(m.Protected) {
			lower := e.Lower(p)
			if lower == "" {
				continue
			}
			base := r.Home
			if p.Area == AreaWorkspace {
				base = r.Workspace
			}
			target := filepath.Join(base, filepath.FromSlash(p.Rel))
			if clash := overlapsAny(target, targets); clash != "" {
				m.logf("repo cache %s %s: %s overlaps %s of another cache (skipped)", r.Repo, d.Name, p, clash)
				continue
			}
			if !overlay {
				job.items = append(job.items, copyItem{p: p, lower: lower, base: base, target: target})
				targets = append(targets, target)
				continue
			}
			if err := prepareTarget(base, target, false); err != nil {
				m.logf("repo cache %s %s: %s not restored: %v", r.Repo, d.Name, p, err)
				continue
			}
			slot := filepath.Join(launch, slotName(e.Dir, p))
			if err := m.slot(slot); err != nil {
				return fail(fmt.Errorf("repo cache: %s: %w", slot, err))
			}
			g.Mounts = append(g.Mounts, Mount{Target: target, Lower: lower,
				Upper: filepath.Join(slot, "upper"), Work: filepath.Join(slot, "work")})
			targets = append(targets, target)
			n++
		}
		if len(job.items) > 0 {
			m.pin(e.Dir)
			copies = append(copies, job)
			m.Store.Touch(e)
			continue
		}
		if n == 0 {
			continue
		}
		used = append(used, e.Dir)
		m.Store.Touch(e)
		m.logf("repo cache %s %s (%s): %s %s for %s, %d path(s) by overlay (run %s)", r.Repo, d.Name, d.Origin, kind, e.Key, key, n, r.Run)
	}
	if launch != "" {
		if len(g.Mounts) == 0 {
			_ = RemoveAll(launch)
		} else if err := writeUses(launch, used); err != nil {
			return fail(err)
		} else {
			g.Lock = LaunchLock(launch)
		}
	}
	if m.testHook != nil {
		m.testHook("restored")
	}
	return g, copies, needWarm, nil
}

func (m *Manager) overlay() (bool, string) {
	if m.Overlay != nil {
		return m.Overlay()
	}
	return OverlayUsable(m.Store.Dir, m.Store.layersRoot())
}

// slot makes a fresh overlay upper and work directory in a launch's own
// layers directory.
func (m *Manager) slot(slot string) error {
	for _, d := range []string{slot, filepath.Join(slot, "upper"), filepath.Join(slot, "work")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func slotName(entryDir string, p Path) string {
	h := sha256.Sum256([]byte(entryDir + "\x00" + p.String()))
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

// copyIn clones (or, under CopyMax, copies) a cache path into target,
// replacing what the target held. Its entry is pinned by the caller.
func (m *Manager) copyIn(lower, base, target string) error {
	if err := prepareTarget(base, target, true); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	if cloneDir(lower, target) {
		return nil
	}
	max := m.CopyMax
	if max <= 0 {
		max = 1 << 30
	}
	if err := copyTree(lower, target, max); err != nil {
		_ = RemoveAll(target)
		return err
	}
	return nil
}

func (m *Manager) pin(dir string) {
	m.mu.Lock()
	m.init()
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

// InUse reports a published cache a live launch uses.
func (m *Manager) InUse(dir string) bool {
	m.mu.Lock()
	pinned := m.pins[dir] > 0
	m.mu.Unlock()
	return pinned || m.Store.Used()[dir]
}

// Evict trims the store to MaxSize, least recently used first, never a
// cache in use. Warm scratch counts toward the cap; a live launch's overlay
// layers do not (they are its own writes, bounded by the disk guard on the
// store's filesystem, and gone when it ends), so one launch's writes never
// evict other caches. The layers of launches that have ended go first. The
// check of what is in use and the removal are one critical section with a
// restore's lookup and record.
func (m *Manager) Evict() {
	var evicted []Entry
	m.Store.Locked(func() {
		m.Store.sweepLaunches()
		if m.MaxSize <= 0 {
			return
		}
		used := m.Store.Used()
		if m.testHook != nil {
			m.testHook("evict-used")
		}
		scratch := DiskUsage(m.Store.warmRoot())
		evicted = m.Store.Evict(m.MaxSize-scratch, func(d string) bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.pins[d] > 0 || used[d]
		})
	})
	for _, e := range evicted {
		m.logf("repo cache %s %s: evicted %s (%s, last used %s)", e.Repo, e.Name, e.Key, HumanSize(e.Size), e.LastUsed.Format(time.RFC3339))
	}
}

// WarmAsync warms repo in the background, at most once per minWarmGap and
// never twice at once.
func (m *Manager) WarmAsync(repo string) {
	if m.Checkout == nil || m.Warm == nil {
		return
	}
	m.mu.Lock()
	m.init()
	if m.warming[repo] || time.Since(m.lastWarm[repo]) < minWarmGap {
		m.mu.Unlock()
		return
	}
	m.warming[repo] = true
	m.lastWarm[repo] = time.Now()
	base := m.base
	if base == nil {
		base = context.Background()
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(base, 2*m.warmTimeout()+10*time.Minute)
		defer cancel()
		if err := m.WarmRepo(ctx, repo); err != nil {
			m.logf("repo cache %s: warm: %v", repo, err)
		}
	}()
}

// Wait waits for background warms (tests, shutdown).
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) warmTimeout() time.Duration {
	if m.WarmTimeout > 0 {
		return m.WarmTimeout
	}
	return 20 * time.Minute
}

// WarmRepo refreshes repo's caches from its default branch: a fresh
// checkout of it, the caches it defines (its own repo file, read from that
// checkout, and the recipes detected there), and for each whose key is not
// yet published, its warm command run in the agent sandbox with a fresh
// home, then the declared paths published. It takes no branch: nothing an
// agent's run controls is ever published.
func (m *Manager) WarmRepo(ctx context.Context, repo string) error {
	m.mu.Lock()
	m.init()
	m.warming[repo] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.warming, repo)
		m.lastWarm[repo] = time.Now()
		m.mu.Unlock()
	}()
	if err := CheckRepo(repo); err != nil {
		return err
	}
	if m.Checkout == nil || m.Warm == nil {
		return errors.New("warming is not wired")
	}
	scratch := m.Store.warmRoot()
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(scratch, repoSlug(repo)+"-")
	if err != nil {
		return err
	}
	defer RemoveAll(tmp)
	tree := filepath.Join(tmp, "tree")
	commit, err := m.Checkout(ctx, repo, tree)
	if err != nil {
		return fmt.Errorf("check out the default branch: %w", err)
	}
	var file *RepoFile
	if m.RepoFileOn {
		if b, err := readInTree(tree, RepoFileName); err == nil && b != nil {
			f, warn, perr := ParseRepoFile(b)
			m.warnOnce(repo+"@"+commit, warn)
			if perr != nil {
				m.warnOnce(repo+"@"+commit+"!", []string{perr.Error() + " (ignored)"})
			} else {
				file = &f
			}
		}
	}
	defs, warn := m.Defs(repo, tree, file)
	m.warnOnce(repo+"@defs", warn)
	var errs []error
	for _, d := range defs {
		if d.Warm == "" {
			continue
		}
		if err := m.warmOne(ctx, repo, commit, tree, tmp, d); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.Name, err))
		}
	}
	m.Evict()
	return errors.Join(errs...)
}

func (m *Manager) warmOne(ctx context.Context, repo, commit, tree, tmp string, d Def) error {
	key, err := RenderKey(d.Key, tree)
	if err != nil {
		return fmt.Errorf("key: %w", err)
	}
	if m.Store.Has(repo, d.Name, key) {
		m.logf("repo cache %s %s: %s is up to date (default branch %s)", repo, d.Name, key, short(commit))
		return nil
	}
	if m.Store.isTooBig(repo, d.Name, key, m.MaxSize) || m.waitingForRoom(repo, d.Name, key, tmp) {
		return nil
	}
	home := filepath.Join(tmp, "home-"+d.Name)
	if err := os.Mkdir(home, 0o700); err != nil {
		return err
	}
	start := time.Now()
	m.logf("repo cache %s %s: warming %s on the default branch %s", repo, d.Name, key, short(commit))
	wctx, cancel := context.WithTimeout(ctx, m.warmTimeout())
	err = m.Warm(wctx, WarmRun{Repo: repo, Cache: d.Name, Workspace: tree, Home: home,
		Env: d.ExpandEnv(home, tree), Command: d.Warm, Timeout: m.warmTimeout()})
	cancel()
	if err != nil {
		return fmt.Errorf("warm command failed (nothing published): %w", err)
	}
	// Every declared path is checked before anything moves: one that is a
	// symlink or not a directory refuses the whole publish (a warm that
	// plants one is not trusted for the others); an absent one is skipped.
	type move struct {
		p   Path
		src string
	}
	var moves []move
	var paths []string
	for _, p := range d.ParsedPaths(m.Protected) {
		base := home
		if p.Area == AreaWorkspace {
			base = tree
		}
		src := filepath.Join(base, filepath.FromSlash(p.Rel))
		exists, err := safeTarget(base, src)
		if err != nil && !(exists && errors.Is(err, errNotEmpty)) {
			return fmt.Errorf("%s: %w (nothing published)", p, err)
		}
		if exists {
			moves = append(moves, move{p, src})
			paths = append(paths, p.String())
		}
	}
	if len(moves) == 0 {
		return errors.New("the warm left none of the cache's paths (nothing published)")
	}
	if m.MaxSize > 0 {
		var size int64
		for _, mv := range moves {
			size += DiskUsage(mv.src)
		}
		if size > m.MaxSize {
			// Never fits: marked in the store, so no daemon warms it again.
			m.Store.markTooBig(repo, d.Name, key, size)
			m.logf("repo cache %s %s: %s is %s, larger than the store's cap (%s): not published, and not warmed again", repo, d.Name, key, HumanSize(size), HumanSize(m.MaxSize))
			return nil
		}
		if room := m.room(tmp); size > room {
			// Would fit, but not beside what live launches use now: not
			// published, and not warmed again until there is room for it.
			m.mu.Lock()
			m.init()
			m.noRoom[repo+"\x00"+d.Name+"\x00"+key] = size
			m.mu.Unlock()
			m.logf("repo cache %s %s: %s is %s, more than the room left beside the caches in use (%s): not published until there is room", repo, d.Name, key, HumanSize(size), HumanSize(max(room, 0)))
			return nil
		}
	}
	e, err := m.Store.Publish(Meta{Repo: repo, Name: d.Name, Key: key, Origin: d.Origin, Commit: commit, Paths: paths}, func(data string) error {
		for _, mv := range moves {
			if err := moveTree(mv.src, filepath.Join(data, string(mv.p.Area), filepath.FromSlash(mv.p.Rel))); err != nil {
				return err
			}
		}
		return nil
	})
	m.mu.Lock()
	delete(m.noRoom, repo+"\x00"+d.Name+"\x00"+key)
	m.mu.Unlock()
	if errors.Is(err, ErrExists) {
		return nil
	}
	if err != nil {
		return err
	}
	m.logf("repo cache %s %s: published %s (%s, %s, in %s)", repo, d.Name, e.Key, strings.Join(paths, " "), HumanSize(e.Size), time.Since(start).Round(time.Second))
	return nil
}

// readInTree reads a file at a checkout's root without following a symlink
// out of it (nil: none).
func readInTree(root, name string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	fi, err := r.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxRepoFile+1))
}

// Run is Start, waiting until the manager has stopped.
func (m *Manager) Run(ctx context.Context) { <-m.Start(ctx) }

// Start runs the manager until ctx ends: it removes warm scratch a
// previous daemon left (no warm of this one has started yet), then evicts
// hourly and, with WarmEvery, re-warms every repository the store or the
// config knows. Background warms run under ctx, so ending it cancels them.
// The channel closes once every warm has stopped and cleaned up.
func (m *Manager) Start(ctx context.Context) <-chan struct{} {
	m.mu.Lock()
	m.init()
	m.base = ctx
	m.mu.Unlock()
	if ents, err := os.ReadDir(m.Store.warmRoot()); err == nil {
		for _, e := range ents {
			_ = RemoveAll(filepath.Join(m.Store.warmRoot(), e.Name()))
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer m.wg.Wait()
		m.loop(ctx)
	}()
	return done
}

func (m *Manager) loop(ctx context.Context) {
	m.Evict()
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	var warm <-chan time.Time
	if m.WarmEvery > 0 {
		t := time.NewTicker(m.WarmEvery)
		defer t.Stop()
		warm = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.Evict()
		case <-warm:
			for _, repo := range m.knownRepos() {
				if err := m.WarmRepo(ctx, repo); err != nil {
					m.logf("repo cache %s: scheduled warm: %v", repo, err)
				}
			}
		}
	}
}

func (m *Manager) knownRepos() []string {
	seen := map[string]bool{}
	for r := range m.Repos {
		seen[r] = true
	}
	for _, e := range m.Store.List() {
		seen[e.Repo] = true
	}
	var out []string
	for r := range seen {
		if CheckRepo(r) == nil {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// wantWarm reports whether a miss (or partial hit) of key should warm it:
// it has a warm command, its last warm did not leave more than the cap,
// and it is not waiting for room beside the caches in use.
func (m *Manager) wantWarm(repo string, d Def, key string) bool {
	return d.Warm != "" && !m.Store.isTooBig(repo, d.Name, key, m.MaxSize) && !m.waitingForRoom(repo, d.Name, key, "")
}

// waitingForRoom reports a key whose last warm fitted the cap but not the
// room left beside the caches in use, while there is still not room for it.
func (m *Manager) waitingForRoom(repo, name, key, own string) bool {
	m.mu.Lock()
	need, ok := m.noRoom[repo+"\x00"+name+"\x00"+key]
	m.mu.Unlock()
	return ok && m.MaxSize > 0 && m.room(own) < need
}

// room is what the cap leaves beside the caches live launches use and the
// warm scratch of other warms (own is this warm's, its output in it).
func (m *Manager) room(own string) int64 {
	used := m.Store.Used()
	var inUse int64
	for _, e := range m.Store.List() {
		if used[e.Dir] {
			inUse += e.Size
		}
	}
	scratch := DiskUsage(m.Store.warmRoot())
	if own != "" {
		scratch -= DiskUsage(own)
	}
	return m.MaxSize - inUse - scratch
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
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
