package sandbox

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
// what a launch wrote outlives it; at load, a path that no longer exists is
// dropped. Without one nothing is recorded (a test binary).
var granted struct {
	sync.Mutex
	on    bool
	file  string
	skip  []string
	paths []string
	hooks []func()
}

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
		addGranted(sc.Text())
	}
	f.Close()
	pruneGranted()
	return rewriteGranted()
}

// ResetWriteLedger turns recording off and forgets what was recorded (a
// test's cleanup).
func ResetWriteLedger() {
	granted.Lock()
	defer granted.Unlock()
	granted.on, granted.file, granted.skip, granted.paths = false, "", nil, nil
}

// GrantedWrites is every path recorded as writable by a sandboxed launch.
func GrantedWrites() []string {
	granted.Lock()
	defer granted.Unlock()
	return slices.Clone(granted.paths)
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
// records them when it may.
func AdmitWrites(paths ...string) error {
	if WriteGuard != nil {
		if err := WriteGuard(paths); err != nil {
			return err
		}
	}
	recordWrites(paths...)
	return nil
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
	for _, p := range ps {
		if addGranted(p) {
			added = append(added, filepath.Clean(p))
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
// absolute path, already recorded, or inside a skipped dir.
func addGranted(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	p = filepath.Clean(p)
	if slices.Contains(granted.paths, p) {
		return false
	}
	for _, s := range granted.skip {
		if p == s || strings.HasPrefix(p, s+string(filepath.Separator)) {
			return false
		}
	}
	granted.paths = append(granted.paths, p)
	return true
}

func pruneGranted() {
	granted.paths = slices.DeleteFunc(granted.paths, func(p string) bool {
		_, err := os.Lstat(p)
		return os.IsNotExist(err)
	})
}

func rewriteGranted() error {
	if granted.file == "" {
		return nil
	}
	tmp := granted.file + ".new"
	body := ""
	if len(granted.paths) > 0 {
		body = strings.Join(granted.paths, "\n") + "\n"
	}
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
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
