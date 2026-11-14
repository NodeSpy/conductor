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
// what a launch wrote outlives it. A path is forgotten when it no longer
// exists, and, for one the config granted (an fs: path, a plugin's write),
// when the daemon starts with a config that no longer grants it
// (ForgetUnconfigured). One a run was let write (its workspace) is kept
// while it exists. Without a ledger nothing is recorded (a test binary).
var granted struct {
	sync.Mutex
	on    bool
	file  string
	skip  []string
	paths []string
	meta  map[string]grant
	hooks []func()
	// configured are the paths the config grants launches (SetConfiguredWrites).
	configured []string
}

// grant is what the ledger says of a path: when it was first recorded, and
// whether the config granted it (kindConfig) or a run was let write it
// (kindRun).
type grant struct {
	at   time.Time
	kind string
}

const (
	kindConfig = "config"
	kindRun    = "run"
)

// admit serialises admitting a launch's writes (AdmitWrites) with work
// that must see no admission in between (Exclusive).
var admit sync.Mutex

// maxGranted is when recording prunes paths that no longer exist (each
// run's workspace is a new path).
const maxGranted = 1024

// UseWriteLedger turns recording on, persisting to file ("" keeps it in
// memory), and loads what file holds. Paths inside skip (conductor's own
// dirs, which every check already refuses) are not recorded.
func UseWriteLedger(file string, skip ...string) error {
	granted.Lock()
	defer granted.Unlock()
	granted.on, granted.file = true, file
	granted.skip = nil
	for _, s := range skip {
		if filepath.IsAbs(s) {
			granted.skip = append(granted.skip, filepath.Clean(s))
		}
	}
	if file == "" {
		return nil
	}
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p, g := parseGrant(sc.Text())
		addGranted(p, g)
	}
	f.Close()
	pruneGranted()
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
// recorded as the config's, and forgotten when the config stops granting it
// (ForgetUnconfigured). Paths not named here are recorded as a run's.
func SetConfiguredWrites(paths []string) {
	granted.Lock()
	defer granted.Unlock()
	granted.configured = nil
	for _, p := range paths {
		if filepath.IsAbs(p) {
			granted.configured = append(granted.configured, dirForms(p)...)
		}
	}
}

// ForgetUnconfigured forgets every path recorded as the config's that the
// config set by SetConfiguredWrites no longer grants, logging each through
// logf: a dir an operator stopped granting comes back onto conductor's
// PATH, and its programs are trusted again, once no launch may write it. A
// path a run was let write is kept while it exists.
func ForgetUnconfigured(logf func(string, ...any)) error {
	granted.Lock()
	defer granted.Unlock()
	n := len(granted.paths)
	granted.paths = slices.DeleteFunc(granted.paths, func(p string) bool {
		g := granted.meta[p]
		if g.kind != kindConfig || configured(p) {
			return false
		}
		logf("sandbox writes: %s is forgotten: the config let launches write it (recorded %s in %s), and no longer does", p, g.at.Format(time.DateOnly), granted.file)
		delete(granted.meta, p)
		return true
	})
	if len(granted.paths) == n {
		return nil
	}
	return rewriteGranted()
}

// configured: p is a path the config grants (callers hold the lock).
func configured(p string) bool {
	for _, f := range dirForms(p) {
		if slices.Contains(granted.configured, f) {
			return true
		}
	}
	return false
}

// dirForms are p cleaned and, when it resolves, resolved.
func dirForms(p string) []string {
	out := []string{filepath.Clean(p)}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != out[0] {
		out = append(out, r)
	}
	return out
}

// GrantedWrites is every path recorded as writable by a sandboxed launch.
func GrantedWrites() []string {
	granted.Lock()
	defer granted.Unlock()
	return slices.Clone(granted.paths)
}

// WriteSource says where a recorded path came from, for a refusal or a log
// line: "" when p is not recorded.
func WriteSource(p string) string {
	granted.Lock()
	defer granted.Unlock()
	g, ok := granted.meta[filepath.Clean(p)]
	if !ok {
		return ""
	}
	where := "the sandboxed launches' write ledger"
	if granted.file != "" {
		where = granted.file
	}
	if g.kind == kindConfig {
		return fmt.Sprintf("the config let a sandboxed launch write %s (recorded %s in %s; forgotten once the config stops granting it)", p, g.at.Format(time.DateOnly), where)
	}
	return fmt.Sprintf("a run's sandboxed launch was let write %s (recorded %s in %s; forgotten once it no longer exists)", p, g.at.Format(time.DateOnly), where)
}

// OnGrantedWrites calls f (outside the lock) whenever a path is added.
func OnGrantedWrites(f func()) {
	granted.Lock()
	defer granted.Unlock()
	granted.hooks = append(granted.hooks, f)
}

// WriteGuard, when set, is asked before a launch is let write paths: an
// error refuses the launch (a program another jail runs lies there). Set
// once at start-up.
var WriteGuard func(paths []string) error

// AdmitWrites asks WriteGuard whether a launch may write paths, and
// records them when it may: one step, so nothing that runs under
// Exclusive sees the question asked but the paths not yet recorded.
func AdmitWrites(paths ...string) error {
	admit.Lock()
	defer admit.Unlock()
	if WriteGuard != nil {
		if err := WriteGuard(paths); err != nil {
			return err
		}
	}
	recordWrites(paths...)
	return nil
}

// Exclusive runs f with no launch's writes admitted while it runs: what f
// reads of GrantedWrites stays true until it returns, so a check on what a
// launch may write and registering what WriteGuard refuses are one step.
func Exclusive(f func() error) error {
	admit.Lock()
	defer admit.Unlock()
	return f()
}

// recordWrites records the writable host paths of a launch being built.
func recordWrites(ps ...string) {
	granted.Lock()
	if !granted.on {
		granted.Unlock()
		return
	}
	rewrite := len(granted.paths) > maxGranted
	if rewrite {
		pruneGranted()
	}
	var added []string
	now := time.Now()
	for _, p := range ps {
		if !filepath.IsAbs(p) {
			continue
		}
		g := grant{at: now, kind: kindRun}
		if configured(p) {
			g.kind = kindConfig
		}
		if addGranted(p, g) {
			p = filepath.Clean(p)
			added = append(added, granted.meta[p].line(p))
		}
	}
	if (rewrite || len(added) > 0) && granted.file != "" {
		if rewrite {
			_ = rewriteGranted()
		} else if f, err := os.OpenFile(granted.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(strings.Join(added, "\n") + "\n")
			f.Close()
		}
	}
	hooks := slices.Clone(granted.hooks)
	granted.Unlock()
	if len(added) > 0 {
		for _, h := range hooks {
			h()
		}
	}
}

// addGranted adds p (callers hold the lock): false when it is not an
// absolute path, already recorded as it is, or inside a skipped dir. A path
// recorded both as the config's and a run's is a run's (kept while it
// exists): that is a change to record.
func addGranted(p string, g grant) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	p = filepath.Clean(p)
	if old, ok := granted.meta[p]; ok {
		if old.kind == kindConfig && g.kind == kindRun {
			old.kind = kindRun
			granted.meta[p] = old
			return true
		}
		return false
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

func pruneGranted() {
	granted.paths = slices.DeleteFunc(granted.paths, func(p string) bool {
		_, err := os.Lstat(p)
		if os.IsNotExist(err) {
			delete(granted.meta, p)
			return true
		}
		return false
	})
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
