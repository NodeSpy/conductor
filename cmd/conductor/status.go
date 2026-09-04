package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// cmdStatus prints a snapshot of what conductor is doing — read entirely from the
// on-disk state/runs/audit files and `paseo ls` (never opening the store, so it
// won't clash with the running daemon). It answers "what's live, what's mid-flight,
// what looks stuck, and what's waiting on me" without digging through journalctl.
func cmdStatus(args []string) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}

	fmt.Printf("conductor %s\n", version)
	fmt.Printf("service:  %s\n", serviceStateStr())

	// Live conductor agents (running now).
	agents := conductorAgents(cfg.PaseoBin)
	fmt.Printf("\nlive agents (%d):\n", len(agents))
	if len(agents) == 0 {
		fmt.Println("  (none)")
	}
	for _, a := range agents {
		fmt.Printf("  %-9s %-8s %s\n", a.ShortID, a.Status, a.Name)
	}

	// In-flight multi-step workflows (mid-run, e.g. a review awaiting you).
	runs := readRuns(filepath.Join(filepath.Dir(cfg.Store.StateFile), "runs.json"))
	fmt.Printf("\nin-flight workflows (%d):\n", len(runs))
	if len(runs) == 0 {
		fmt.Println("  (none)")
	}
	for _, r := range runs {
		fmt.Printf("  %s#%d %-16s step %d  (%s)\n", r.Repo, r.Number, r.Kind, r.StepIndex, ago(r.UpdatedAt))
	}

	// Tracked objects that look stuck: multiple attempts on the same head (in backoff).
	recs := readRecords(cfg.Store.StateFile)
	stuck := stuckRecords(recs)
	fmt.Printf("\ntracked objects: %d  (retrying/backoff: %d)\n", len(recs), len(stuck))
	for _, s := range stuck {
		fmt.Printf("  %-34s %-16s %d attempts  (last %s)\n", s.key, s.kind, s.attempts, ago(s.last))
	}

	// Recent items that wanted your attention.
	ev := recentAttention(cfg.Store.AuditLog, 8)
	fmt.Printf("\nrecent attention (escalate / needs_input):\n")
	if len(ev) == 0 {
		fmt.Println("  (none)")
	}
	for _, e := range ev {
		fmt.Printf("  %s  [%s] %s#%v %v\n", e.ts, e.event, e.repo, e.number, e.kind)
	}
	return nil
}

// pausePath is the runtime-pause control file (a sibling of the state file). Its
// presence makes the running daemon skip all dispatch until removed.
func pausePath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Store.StateFile), "paused")
}

// cmdPause creates (pause) or removes (resume) the pause control file. The running
// daemon checks it per trigger, so it takes effect without a restart.
func cmdPause(args []string, pause bool) error {
	cfg, _, err := loadConfig(args)
	if err != nil {
		return err
	}
	p := pausePath(cfg)
	if pause {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Println("paused — the daemon will skip dispatch until `resume` (no restart needed)")
	} else {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("resumed")
	}
	return nil
}

// --- service state ---

func serviceStateStr() string {
	switch serviceKind() {
	case "systemd":
		out, _ := exec.Command("systemctl", "--user", "is-active", serviceName()).Output()
		st := strings.TrimSpace(string(out))
		if st == "" {
			st = "unknown"
		}
		return st
	case "launchd":
		if exec.Command("launchctl", "kill", "-0", fmt.Sprintf("gui/%d/sh.%s", os.Getuid(), serviceName())).Run() == nil {
			return "running"
		}
		return "not running"
	}
	return "unknown"
}

// --- live agents ---

type agentLine struct {
	ShortID string `json:"shortId"`
	Status  string `json:"status"`
	Name    string `json:"name"`
}

func conductorAgents(bin string) []agentLine {
	if bin == "" {
		bin = "paseo"
	}
	out, err := exec.Command(bin, "ls", "--json", "--label", "conductor=1").Output()
	if err != nil {
		return nil
	}
	var a []agentLine
	if json.Unmarshal(out, &a) != nil {
		return nil
	}
	return a
}

// --- workflow runs ---

type runLine struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Kind      string    `json:"kind"`
	StepIndex int       `json:"step_index"`
	UpdatedAt time.Time `json:"updated_at"`
}

func readRuns(path string) []runLine {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]runLine
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	out := make([]runLine, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// --- tracked records / stuck detection ---

type recordLine struct {
	Acted     map[string]string    `json:"acted"`
	Attempts  map[string]int       `json:"attempts"`
	AttemptAt map[string]time.Time `json:"attempt_at"`
}

func readRecords(path string) map[string]recordLine {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]recordLine
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

type stuckLine struct {
	key, kind string
	attempts  int
	last      time.Time
}

// stuckRecords surfaces (key, kind) pairs with repeated attempts — i.e. work that
// keeps failing and is sitting in retry backoff.
func stuckRecords(recs map[string]recordLine) []stuckLine {
	var out []stuckLine
	for key, r := range recs {
		for k, n := range r.Attempts { // k is "kind@head"
			if n < 2 {
				continue
			}
			kind := k
			if i := strings.LastIndex(k, "@"); i >= 0 {
				kind = k[:i]
			}
			out = append(out, stuckLine{key: key, kind: kind, attempts: n, last: r.AttemptAt[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].attempts > out[j].attempts })
	return out
}

// --- audit tail ---

type auditEvent struct {
	ts, event, repo, kind string
	number                any
}

func recentAttention(path string, n int) []auditEvent {
	lines := tailLines(path, 256<<10) // last 256 KiB is plenty for a handful of events
	var out []auditEvent
	for _, l := range lines {
		var d map[string]any
		if json.Unmarshal([]byte(l), &d) != nil {
			continue
		}
		ev, _ := d["event"].(string)
		if ev != "escalate" && ev != "needs_input" {
			continue
		}
		repo, _ := d["repo"].(string)
		kind, _ := d["kind"].(string)
		ts, _ := d["ts"].(string)
		out = append(out, auditEvent{ts: ts, event: ev, repo: repo, kind: kind, number: d["number"]})
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// tailLines returns the complete lines within the last maxBytes of a file.
func tailLines(path string, maxBytes int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if fi.Size() > maxBytes {
		start = fi.Size() - maxBytes
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil
	}
	data, err := readAllCapped(f)
	if err != nil {
		return nil
	}
	if start > 0 { // drop the partial first line
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func readAllCapped(f *os.File) ([]byte, error) {
	var b bytes.Buffer
	if _, err := b.ReadFrom(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ago renders a compact "time since" for a timestamp.
func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
