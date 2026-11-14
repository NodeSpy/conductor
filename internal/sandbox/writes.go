package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Granted writes: every host path a sandboxed launch was let write. A
// sandboxed launch can leave anything in such a path — a program, a link, a
// library — so a check on what conductor runs, or binds into another jail
// to run, asks GrantedWrites instead of trusting it.
//
// Each wrap records the writable host paths of the launch it builds
// (WrapLocal, WrapSeatbeltPlugin): the one place every sandboxed launch
// goes through, so a new kind of launch is counted without being listed.
// With a ledger (UseWriteLedger) the paths persist across restarts, since
// what a launch wrote outlives it. A path is forgotten only once it no
// longer exists: what a launch left there while it was let write stays,
// whether the config still grants the path or not (ReportUnconfigured says
// so). Without a ledger nothing is recorded (a test binary).
//
// Each path is resolved (ResolveWrite) before any lock is taken, and kept
// with its forms: a path on a hung network mount holds up the one launch
// that names it, never every launch.
var granted struct {
	sync.Mutex
	on    bool
	file  string
	skip  []string
	paths []string
	meta  map[string]grant
	tick  uint64
	hooks []func()
	// configured are the forms of the paths the config grants launches
	// (SetConfiguredWrites).
	configured []string
}

// grant is what the ledger says of a path: when it was first recorded,
// whether the config granted it (kindConfig) or a run was let write it
// (kindRun), and its forms (ResolveWrite).
type grant struct {
	at    time.Time
	kind  string
	forms []string
	// seen is the tick it was last granted at: a prune that checked it
	// before then leaves it be.
	seen uint64
}

const (
	kindConfig = "config"
	kindRun    = "run"
)

// ResolveWrite gives the forms of a path to compare (the path cleaned and,
// where it differs, resolved): set once at start-up by what defines how
// paths are compared; cleaned only by default. It may block (a network
// mount); it is never called holding a lock.
var ResolveWrite = func(p string) []string { return []string{filepath.Clean(p)} }

// Write is a path a launch is to be let write, with its forms.
type Write struct {
	Path  string
	Forms []string
}

// resolveWrites resolves ps (absolute paths only), with no lock held.
func resolveWrites(ps []string) []Write {
	var out []Write
	for _, p := range ps {
		if filepath.IsAbs(p) {
			out = append(out, Write{Path: filepath.Clean(p), Forms: ResolveWrite(p)})
		}
	}
	return out
}

// admit serialises admitting a launch's writes (AdmitWrites) with work
// that must see no admission in between (Exclusive).
var admit sync.Mutex

// maxGranted is when recording prunes paths that no longer exist (each
// run's workspace is a new path).
var maxGranted = 1024

// UseWriteLedger turns recording on, persisting to file ("" keeps it in
// memory), and loads what file holds, less what no longer exists. Paths
// inside skip (conductor's own dirs, which every check already refuses)
// are not recorded.
func UseWriteLedger(file string, skip ...string) error {
	var loaded []string
	var gs []grant
	if file != "" {
		f, err := os.Open(file)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				p, g := parseGrant(sc.Text())
				if !filepath.IsAbs(p) {
					continue
				}
				if _, err := os.Lstat(p); os.IsNotExist(err) {
					continue
				}
				g.forms = ResolveWrite(p)
				loaded, gs = append(loaded, p), append(gs, g)
			}
			f.Close()
		}
	}
	granted.Lock()
	defer granted.Unlock()
	granted.on, granted.file = true, file
	granted.skip = nil
	for _, s := range skip {
		if filepath.IsAbs(s) {
			granted.skip = append(granted.skip, filepath.Clean(s))
		}
	}
	for i, p := range loaded {
		addGranted(p, gs[i])
	}
	if file == "" {
		return nil
	}
	return rewriteGranted()
}

// parseGrant reads a ledger line: "<unix seconds> <kind> <path>",
// tab-separated, or a bare path (an earlier ledger: a run's, recorded now).
func parseGrant(line string) (string, grant) {
	if ts, rest, ok := strings.Cut(line, "\t"); ok {
		if kind, p, ok := strings.Cut(rest, "\t"); ok && (kind == kindConfig || kind == kindRun) {
			if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
				return p, grant{at: time.Unix(n, 0), kind: kind}
			}
		}
	}
	return line, grant{at: time.Now(), kind: kindRun}
}

func (g grant) line(p string) string {
	return fmt.Sprintf("%d\t%s\t%s", g.at.Unix(), g.kind, p)
}

// ResetWriteLedger turns recording off and forgets what was recorded (a
// test's cleanup).
func ResetWriteLedger() {
	granted.Lock()
	defer granted.Unlock()
	granted.on, granted.file, granted.skip, granted.paths, granted.meta, granted.configured = false, "", nil, nil, nil, nil
}

// SetConfiguredWrites names the paths the config grants launches (every
// fs: path, every plugin's write): a path recorded that is one of them is
// recorded as the config's, which is what the messages about it say. It
// is forgotten, like any other, only once it no longer exists.
func SetConfiguredWrites(paths []string) {
	var forms []string
	for _, w := range resolveWrites(paths) {
		forms = append(forms, w.Forms...)
	}
	granted.Lock()
	defer granted.Unlock()
	granted.configured = forms
}

// ReportUnconfigured logs, through logf, every recorded path the config
// granted that the config set by SetConfiguredWrites no longer grants: it
// is still counted as writable — what launches left there stays — and the
// line says how to clear it.
func ReportUnconfigured(logf func(string, ...any)) {
	granted.Lock()
	defer granted.Unlock()
	for _, p := range granted.paths {
		g := granted.meta[p]
		if g.kind != kindConfig || configured(g.forms) {
			continue
		}
		logf("sandbox writes: %s is still counted as writable: the config let launches write it (recorded %s in %s) and no longer does, but what they left there stays — remove the directory, or delete its line from %s and restart conductor, to clear it", p, g.at.Format(time.DateOnly), granted.file, granted.file)
	}
}

// configured: one of forms is a path the config grants (callers hold the
// lock).
func configured(forms []string) bool {
	for _, f := range forms {
		if slices.Contains(granted.configured, f) {
			return true
		}
	}
	return false
}

// GrantedWrites is every path recorded as writable by a sandboxed launch.
func GrantedWrites() []string {
	granted.Lock()
	defer granted.Unlock()
	return slices.Clone(granted.paths)
}

// GrantedForms is every recorded path with its forms, as resolved when it
// was recorded: comparing against them needs no filesystem call.
func GrantedForms() []Write {
	granted.Lock()
	defer granted.Unlock()
	out := make([]Write, 0, len(granted.paths))
	for _, p := range granted.paths {
		out = append(out, Write{Path: p, Forms: slices.Clone(granted.meta[p].forms)})
	}
	return out
}

// WriteSource says where a recorded path came from, and how to clear it,
// for a refusal or a log line: "" when p is not recorded.
func WriteSource(p string) string {
	granted.Lock()
	defer granted.Unlock()
	g, ok := granted.meta[filepath.Clean(p)]
	if !ok {
		return ""
	}
	where := "the sandboxed launches' write ledger"
	clear := "it is forgotten once it no longer exists"
	if granted.file != "" {
		where = granted.file
		clear = "remove it, or delete its line from " + where + " and restart conductor, to clear it"
	}
	who := "a run's sandboxed launch was let write"
	if g.kind == kindConfig {
		who = "the config let a sandboxed launch write"
	}
	return fmt.Sprintf("%s %s (recorded %s in %s; %s)", who, p, g.at.Format(time.DateOnly), where, clear)
}

// OnGrantedWrites calls f (outside every lock) whenever a path is added.
func OnGrantedWrites(f func()) {
	granted.Lock()
	defer granted.Unlock()
	granted.hooks = append(granted.hooks, f)
}

// WriteGuard, when set, is asked before a launch is let write ws: an
// error refuses the launch (a program another jail runs lies there). It
// compares forms only: it is called holding the admission lock. Set once
// at start-up.
var WriteGuard func(ws []Write) error

// AdmitWrites asks WriteGuard whether a launch may write paths, and
// records them when it may: one step, so nothing that runs under
// Exclusive sees the question asked but the paths not yet recorded. The
// paths are resolved first, with no lock held.
func AdmitWrites(paths ...string) error {
	ws := resolveWrites(paths)
	pruneIfFull()
	admit.Lock()
	if WriteGuard != nil {
		if err := WriteGuard(ws); err != nil {
			admit.Unlock()
			return err
		}
	}
	added := addWrites(ws)
	admit.Unlock()
	if added {
		runHooks()
	}
	return nil
}

// Exclusive runs f with no launch's writes admitted while it runs: what f
// reads of GrantedForms stays true until it returns, so a check on what a
// launch may write and registering what WriteGuard refuses are one step.
// f must not touch the filesystem: resolve before.
func Exclusive(f func() error) error {
	admit.Lock()
	defer admit.Unlock()
	return f()
}

// recordWrites records the writable host paths of a launch being built.
func recordWrites(ps ...string) {
	ws := resolveWrites(ps)
	pruneIfFull()
	if addWrites(ws) {
		runHooks()
	}
}

// addWrites records ws, appending what is new to the ledger file; true
// when something was.
func addWrites(ws []Write) bool {
	granted.Lock()
	defer granted.Unlock()
	if !granted.on {
		return false
	}
	var added []string
	now := time.Now()
	for _, w := range ws {
		g := grant{at: now, kind: kindRun, forms: w.Forms}
		if configured(w.Forms) {
			g.kind = kindConfig
		}
		if addGranted(w.Path, g) {
			added = append(added, granted.meta[w.Path].line(w.Path))
		}
	}
	if len(added) > 0 && granted.file != "" {
		if f, err := os.OpenFile(granted.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(strings.Join(added, "\n") + "\n")
			f.Close()
		}
	}
	return len(added) > 0
}

func runHooks() {
	granted.Lock()
	hooks := slices.Clone(granted.hooks)
	granted.Unlock()
	for _, h := range hooks {
		h()
	}
}

// addGranted adds p (callers hold the lock): false when it is not an
// absolute path, already recorded as it is, or inside a skipped dir. A path
// recorded both as the config's and a run's is a run's: that is a change
// to record.
func addGranted(p string, g grant) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	p = filepath.Clean(p)
	granted.tick++
	g.seen = granted.tick
	if old, ok := granted.meta[p]; ok {
		old.seen = g.seen
		changed := old.kind == kindConfig && g.kind == kindRun
		if changed {
			old.kind = kindRun
		}
		granted.meta[p] = old
		return changed
	}
	for _, s := range granted.skip {
		if p == s || strings.HasPrefix(p, s+string(filepath.Separator)) {
			return false
		}
	}
	if granted.meta == nil {
		granted.meta = map[string]grant{}
	}
	granted.paths = append(granted.paths, p)
	granted.meta[p] = g
	return true
}

// pruneIfFull forgets the recorded paths that no longer exist once there
// are more than maxGranted (each run's workspace is a new path). The paths
// are checked with no lock held.
// afterPruneStat runs between pruneIfFull's checks and its forgetting (a
// test's hook).
var afterPruneStat = func() {}

func pruneIfFull() {
	granted.Lock()
	if len(granted.paths) <= maxGranted {
		granted.Unlock()
		return
	}
	ps := slices.Clone(granted.paths)
	tick := granted.tick
	granted.Unlock()
	gone := map[string]bool{}
	for _, p := range ps {
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			gone[p] = true
		}
	}
	if len(gone) == 0 {
		return
	}
	afterPruneStat()
	granted.Lock()
	defer granted.Unlock()
	// One granted again since it was checked may exist by now: kept.
	granted.paths = slices.DeleteFunc(granted.paths, func(p string) bool {
		if gone[p] && granted.meta[p].seen <= tick {
			delete(granted.meta, p)
			return true
		}
		return false
	})
	_ = rewriteGranted()
}

func rewriteGranted() error {
	if granted.file == "" {
		return nil
	}
	tmp := granted.file + ".new"
	var b strings.Builder
	for _, p := range granted.paths {
		b.WriteString(granted.meta[p].line(p) + "\n")
	}
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, granted.file)
}

// writableBinds are the host paths a jail's binds let it write.
func writableBinds(binds []BindMount) []string {
	var out []string
	for _, b := range binds {
		if b.RO || b.Tmpfs || b.Link != "" {
			continue
		}
		if b.Src != "" {
			out = append(out, b.Src)
		} else if b.Path != "" {
			out = append(out, b.Path)
		}
	}
	return out
}
