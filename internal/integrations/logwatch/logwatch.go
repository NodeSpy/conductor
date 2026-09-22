// Package logwatch is a log-driven integration: it streams a local log — a file
// (followed through rotation) or the stdout of a streaming command — and emits a
// Trigger when a line matches a regexp. It is the log-line sibling of fswatch:
// "when this line appears, do X." Matches are debounced, so one error's burst of
// lines (a stack trace) collapses into a single trigger.
//
// There is no journald-specific backend (that would need cgo sdjournal); instead
// point a watch's `command:` at `journalctl -f -u <unit> -o cat` — the same
// mechanism also covers `docker logs -f`, `kubectl logs -f`, and anything else
// that streams lines to stdout.
package logwatch

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/debounce"
)

// DefaultDebounce is short: log lines arrive at line rate, and the point of the
// window is only to collapse one event's burst (a multi-line stack trace), not
// to add latency.
const DefaultDebounce = 2 * time.Second

// respawnBackoff is the pause before restarting a watch's command after it
// exits, so a command that dies immediately cannot hot-loop.
const respawnBackoff = 2 * time.Second

// maxLine caps a scanned line at 1 MiB (bufio's default is 64 KiB, too small for
// some stack traces / JSON log lines).
const maxLine = 1 << 20

// Config is a logwatch instance's configuration.
type Config struct {
	Watches []Watch `yaml:"watches"`
}

// Watch is one streamed log. Exactly one of Path / Command is set.
type Watch struct {
	Name     string          `yaml:"name"`
	Path     string          `yaml:"path"`    // a file to follow (via `tail -n0 -F`)
	Command  []string        `yaml:"command"` // a streaming command whose stdout is scanned
	Pattern  string          `yaml:"pattern"` // RE2 regexp; a line matching it fires
	Debounce config.Duration `yaml:"debounce"`
	Action   config.Action   `yaml:"action"`
}

func (w Watch) debounce() time.Duration {
	if w.Debounce > 0 {
		return w.Debounce.D()
	}
	return DefaultDebounce
}

// argv is the command a watch streams: an explicit command, or `tail -n0 -F` of
// a file (which follows the file through truncation and rotation).
func (w Watch) argv() []string {
	if len(w.Command) > 0 {
		return w.Command
	}
	return []string{"tail", "-n", "0", "-F", w.Path}
}

func (w Watch) source() string {
	if len(w.Command) > 0 {
		return "command"
	}
	return w.Path
}

// logLine is the per-watch debounce payload: the last matching line and its
// named capture groups.
type logLine struct {
	line   string
	groups map[string]string
}

// Integration implements core.Integration for one logwatch instance.
type Integration struct {
	name string
	cfg  Config
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, fmt.Errorf("logwatch[%s]: decode config: %w", name, err)
	}
	return &Integration{name: name, cfg: cfg}, nil
}

// Name returns the instance name.
func (g *Integration) Name() string { return g.name }

// Validate checks each watch names exactly one source, a compiling pattern, and
// an action.
func (g *Integration) Validate() error {
	if len(g.cfg.Watches) == 0 {
		return fmt.Errorf("logwatch[%s]: no watches", g.name)
	}
	seen := map[string]bool{}
	for i, w := range g.cfg.Watches {
		if w.Name == "" {
			return fmt.Errorf("logwatch[%s]: watches[%d]: missing name", g.name, i)
		}
		if seen[w.Name] {
			return fmt.Errorf("logwatch[%s]: duplicate watch name %q", g.name, w.Name)
		}
		seen[w.Name] = true
		if (w.Path == "") == (len(w.Command) == 0) {
			return fmt.Errorf("logwatch[%s]: watch %q: set exactly one of `path` or `command`", g.name, w.Name)
		}
		if w.Pattern == "" {
			return fmt.Errorf("logwatch[%s]: watch %q: `pattern` is required", g.name, w.Name)
		}
		if _, err := regexp.Compile(w.Pattern); err != nil {
			return fmt.Errorf("logwatch[%s]: watch %q: bad pattern %q: %w", g.name, w.Name, w.Pattern, err)
		}
		if w.Action.Type == "" && w.Action.FlowRef == "" { // FlowRef: a lowered connectors-model action
			return fmt.Errorf("logwatch[%s]: watch %q: action.type is required", g.name, w.Name)
		}
	}
	return nil
}

// Actions enumerates each watch's action, for the CLI's cross-config checks.
func (g *Integration) Actions() []config.ActionRef {
	refs := make([]config.ActionRef, 0, len(g.cfg.Watches))
	for _, w := range g.cfg.Watches {
		refs = append(refs, config.ActionRef{
			Where: fmt.Sprintf("logwatch[%s] watch %q", g.name, w.Name), Action: w.Action})
	}
	return refs
}

// trigger builds the Trigger a watch emits (action attached for the engine).
func (g *Integration) trigger(w Watch, ev logLine) core.Trigger {
	return core.Trigger{
		Source:   "logwatch",
		Instance: g.name,
		Kind:     w.Name,
		Title:    "logwatch: " + g.name + "/" + w.Name,
		Context: map[string]any{
			"watch":  w.Name,
			"line":   ev.line,
			"source": w.source(),
			"groups": ev.groups, // named regexp capture groups, if any
		},
		Action: w.Action,
	}
}

// Start streams every watch and emits a debounced Trigger per watch as matching
// lines arrive. Returns when ctx is cancelled.
func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	deb := debounce.New(nil,
		func(idx int) time.Duration { return g.cfg.Watches[idx].debounce() },
		func(idx int, ev logLine) { emit(ctx, g.trigger(g.cfg.Watches[idx], ev)) },
	)

	var wg sync.WaitGroup
	for i, w := range g.cfg.Watches {
		re := regexp.MustCompile(w.Pattern) // Validate already compiled it
		wg.Add(1)
		go func(idx int, w Watch, re *regexp.Regexp) {
			defer wg.Done()
			g.stream(ctx, idx, w, re, deb)
		}(i, w, re)
	}
	log.Printf("logwatch[%s]: %d watch(es) streaming", g.name, len(g.cfg.Watches))

	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

// stream runs one watch's command, scanning its stdout for matching lines, and
// respawns the command (with backoff) if it exits — a log source must be durable
// (tail -F should never exit; journalctl -f can). Exits when ctx is cancelled.
func (g *Integration) stream(ctx context.Context, idx int, w Watch, re *regexp.Regexp, deb *debounce.Debouncer[logLine]) {
	argv := w.argv()
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("logwatch[%s]: watch %q: stdout pipe: %v", g.name, w.Name, err)
			return
		}
		if err := cmd.Start(); err != nil {
			log.Printf("logwatch[%s]: watch %q: start %v: %v", g.name, w.Name, argv, err)
			if !sleepCtx(ctx, respawnBackoff) {
				return
			}
			continue
		}

		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), maxLine)
		for sc.Scan() {
			line := sc.Text()
			if m := re.FindStringSubmatch(line); m != nil {
				deb.Arm(idx, logLine{line: line, groups: namedGroups(re, m)})
			}
		}
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		log.Printf("logwatch[%s]: watch %q: stream ended, respawning in %s", g.name, w.Name, respawnBackoff)
		if !sleepCtx(ctx, respawnBackoff) {
			return
		}
	}
}

// namedGroups maps each named subexpression to its captured value.
func namedGroups(re *regexp.Regexp, match []string) map[string]string {
	names := re.SubexpNames()
	out := map[string]string{}
	for i, name := range names {
		if i == 0 || name == "" || i >= len(match) {
			continue
		}
		out[name] = match[i]
	}
	return out
}

// sleepCtx waits d or until ctx is cancelled; returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func init() { core.Register("logwatch", newIntegration) }
