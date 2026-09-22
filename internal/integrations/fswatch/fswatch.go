// Package fswatch is a filesystem-driven integration: it watches directories
// with fsnotify and emits a Trigger when a matching file settles, so a file
// landing on disk can fire a trigger directly instead of a poll noticing it
// later. It is the low-latency counterpart to a `cron` sweep — keep the sweep
// too, because inotify is lossy (events are missed while the process is down,
// dropped on queue overflow, and unevenly delivered on some FUSE mounts).
package fswatch

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/debounce"
)

func init() { core.Register("fswatch", newIntegration) }

// DefaultDebounce is the quiet period a watch waits after the last matching
// event before firing — long enough to collapse one download's burst of
// events (the audio file, cover art, a retag) into a single trigger.
const DefaultDebounce = 15 * time.Second

// Config is an fswatch instance's configuration.
type Config struct {
	Watches []Watch `yaml:"watches"`
}

// Watch is one watched directory tree.
type Watch struct {
	Name      string          `yaml:"name"`
	Path      string          `yaml:"path"`
	Match     string          `yaml:"match"`     // glob on the basename; "" or "*" matches all
	Debounce  config.Duration `yaml:"debounce"`  // quiet period before firing; 0 => DefaultDebounce
	Recursive *bool           `yaml:"recursive"` // watch subdirectories too; nil => true
	Events    []string        `yaml:"events"`    // create|write|rename|remove; empty => create+write+rename
	Action    config.Action   `yaml:"action"`
}

func (w Watch) recursive() bool { return w.Recursive == nil || *w.Recursive }

func (w Watch) debounce() time.Duration {
	if w.Debounce > 0 {
		return w.Debounce.D()
	}
	return DefaultDebounce
}

// ops returns the fsnotify op mask this watch reacts to.
func (w Watch) ops() fsnotify.Op {
	if len(w.Events) == 0 {
		return fsnotify.Create | fsnotify.Write | fsnotify.Rename
	}
	var m fsnotify.Op
	for _, e := range w.Events {
		switch strings.ToLower(strings.TrimSpace(e)) {
		case "create":
			m |= fsnotify.Create
		case "write":
			m |= fsnotify.Write
		case "rename":
			m |= fsnotify.Rename
		case "remove":
			m |= fsnotify.Remove
		case "chmod":
			m |= fsnotify.Chmod
		}
	}
	return m
}

// matches reports whether an event for basename `name` with op `op` is one this
// watch cares about. Directory events are handled separately (recursive add).
func (w Watch) matches(name string, op fsnotify.Op) bool {
	if op&w.ops() == 0 {
		return false
	}
	pat := w.Match
	if pat == "" || pat == "*" {
		return true
	}
	ok, err := filepath.Match(pat, name)
	return err == nil && ok
}

// fsEvent is the per-watch debounce payload: the last matching event.
type fsEvent struct {
	path string
	op   string
}

// Integration implements core.Integration for one fswatch instance.
type Integration struct {
	name string
	cfg  Config
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("fswatch[%s]: decode config: %w", name, err)
	}
	return &Integration{name: name, cfg: cfg}, nil
}

// Name returns the instance name.
func (g *Integration) Name() string { return g.name }

// Validate checks each watch has a name, an existing directory, and an action.
func (g *Integration) Validate() error {
	if len(g.cfg.Watches) == 0 {
		return fmt.Errorf("fswatch[%s]: no watches", g.name)
	}
	seen := map[string]bool{}
	for i, w := range g.cfg.Watches {
		if w.Name == "" {
			return fmt.Errorf("fswatch[%s]: watches[%d]: missing name", g.name, i)
		}
		if seen[w.Name] {
			return fmt.Errorf("fswatch[%s]: duplicate watch name %q", g.name, w.Name)
		}
		seen[w.Name] = true
		if w.Path == "" {
			return fmt.Errorf("fswatch[%s]: watch %q: `path` is required", g.name, w.Name)
		}
		if w.Match != "" && w.Match != "*" {
			if _, err := filepath.Match(w.Match, "probe"); err != nil {
				return fmt.Errorf("fswatch[%s]: watch %q: bad match glob %q: %w", g.name, w.Name, w.Match, err)
			}
		}
		if w.Action.Type == "" && w.Action.FlowRef == "" { // FlowRef: a lowered connectors-model action
			return fmt.Errorf("fswatch[%s]: watch %q: action.type is required", g.name, w.Name)
		}
	}
	return nil
}

// Actions enumerates each watch's action, for the CLI's cross-config checks.
func (g *Integration) Actions() []config.ActionRef {
	refs := make([]config.ActionRef, 0, len(g.cfg.Watches))
	for _, w := range g.cfg.Watches {
		refs = append(refs, config.ActionRef{
			Where: fmt.Sprintf("fswatch[%s] watch %q", g.name, w.Name), Action: w.Action})
	}
	return refs
}

// trigger builds the Trigger a watch emits (action attached for the engine).
func (g *Integration) trigger(w Watch, path, op string) core.Trigger {
	return core.Trigger{
		Source:   "fswatch",
		Instance: g.name,
		Kind:     w.Name,
		Title:    "fswatch: " + g.name + "/" + w.Name,
		Context: map[string]any{
			"watch": w.Name,
			"path":  path,
			// "file" not "name": `name` is a reserved universal scope key (the
			// repo name), and baseData drops any Context key that collides with
			// one — so a basename under "name" would render empty.
			"file": filepath.Base(path),
			"op":   op,
		},
		Action: w.Action,
	}
}

// Start builds one fsnotify watcher over every watch's tree and emits a
// debounced Trigger per watch as matching files settle. Returns when ctx is
// cancelled (the stop signal — there is no Stop method).
func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fswatch[%s]: new watcher: %w", g.name, err)
	}
	defer w.Close()

	// dirWatch maps a watched directory to the index of the watch it belongs to,
	// so an event's directory resolves back to its watch config.
	dirWatch := map[string]int{}
	deb := debounce.New(nil,
		func(idx int) time.Duration { return g.cfg.Watches[idx].debounce() },
		func(idx int, ev fsEvent) { emit(ctx, g.trigger(g.cfg.Watches[idx], ev.path, ev.op)) },
	)
	arm := func(idx int, path, op string) { deb.Arm(idx, fsEvent{path: path, op: op}) }

	add := func(dir string, idx int) {
		if err := w.Add(dir); err != nil {
			// A watch we cannot set (e.g. ENOSPC: fs.inotify.max_user_watches
			// exhausted — a host setting, not something to retry) is logged and
			// skipped; it must never crash the daemon.
			log.Printf("fswatch[%s]: cannot watch %s: %v", g.name, dir, err)
			return
		}
		dirWatch[dir] = idx
	}

	// addTree adds dir and, when the watch is recursive, every subdirectory —
	// fsnotify is not recursive, so new per-item folders are added as they appear.
	var addTree func(dir string, idx int)
	addTree = func(dir string, idx int) {
		add(dir, idx)
		if !g.cfg.Watches[idx].recursive() {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				addTree(filepath.Join(dir, e.Name()), idx)
			}
		}
	}

	for i, wc := range g.cfg.Watches {
		info, err := os.Stat(wc.Path)
		if err != nil || !info.IsDir() {
			log.Printf("fswatch[%s]: watch %q: %s is not a directory (yet); skipping", g.name, wc.Name, wc.Path)
			continue
		}
		addTree(wc.Path, i)
	}
	log.Printf("fswatch[%s]: %d watch(es), %d dir(s)", g.name, len(g.cfg.Watches), len(dirWatch))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			idx, known := dirWatch[filepath.Dir(ev.Name)]
			if !known {
				continue
			}
			// A newly created directory joins the watch (recursive) — and the
			// audio file may already have landed in the gap between the dir
			// appearing and the watch being set, so arm the debounce too.
			if ev.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					if g.cfg.Watches[idx].recursive() {
						addTree(ev.Name, idx)
						arm(idx, ev.Name, ev.Op.String())
					}
					continue
				}
			}
			if g.cfg.Watches[idx].matches(filepath.Base(ev.Name), ev.Op) {
				arm(idx, ev.Name, ev.Op.String())
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			// An error (including a kernel queue overflow surfaced here) may mean
			// events were lost. Arm every watch so a missed file becomes latency,
			// not a book that never imports — the same reason the cron sweep stays.
			log.Printf("fswatch[%s]: watcher error: %v — re-arming all watches", g.name, err)
			for i := range g.cfg.Watches {
				arm(i, g.cfg.Watches[i].Path, "error")
			}
		}
	}
}
