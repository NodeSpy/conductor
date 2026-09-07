package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// cmdRuns is the execution-history surface (#36 §20):
//
//	conductor runs [--limit N]           list recorded runs, newest first
//	conductor runs <id>                  one run's full step detail
//	conductor runs retry <id> [--from <step>]  re-run from a step (recorded inputs pinned)
//
// List/detail read the history directory straight off disk (no daemon
// needed); retry goes through the running daemon's control socket, since the
// re-run needs the engine (tokens, slots, policy).
func cmdRuns(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	dir := historyDirPath(cfg)

	if len(rest) > 0 && rest[0] == "retry" {
		return runsRetry(cfg, rest[1:])
	}
	if len(rest) > 0 && rest[0] == "show" {
		rest = rest[1:]
	}
	// A non-flag argument is a run id → detail view.
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "--") {
		return printRunDetail(dir, rest[0])
	}

	// The list is the discovery surface for "was my run recorded?". A run that
	// exists on disk (and is readable via `conductor runs <id>`) must not vanish
	// from the list just because it fell past a small cap. So the default is
	// generous, `--limit 0` shows everything, and any truncation is announced —
	// never silent (#140 Q-list).
	limit := defaultRunsLimit
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--limit" && i+1 < len(rest) {
			if n, err := strconv.Atoi(rest[i+1]); err == nil && n >= 0 {
				limit = n
			}
			i++
		}
	}
	// Read the full set (retention keeps it bounded) so truncation is exact and
	// the newest runs are always the ones shown.
	all := store.ListHistoryDir(dir, 0)
	if len(all) == 0 {
		fmt.Println("no recorded runs (history records connectors-model runs; see the Runs wiki page)")
		return nil
	}
	recs := all
	if limit > 0 && len(all) > limit {
		recs = all[:limit]
	}
	fmt.Printf("%-22s %-8s %-19s %-26s %-22s %5s %9s\n", "ID", "STATUS", "STARTED", "TRIGGER", "TARGET", "STEPS", "COST")
	for _, r := range recs {
		fmt.Printf("%-22s %-8s %-19s %-26s %-22s %5d %9s\n",
			r.ID, r.Status, r.Started.Format("2006-01-02 15:04:05"),
			triggerLabel(r), targetLabel(r), len(r.Steps), costLabel(r.CostUSD, r.ApproxCost))
	}
	if hidden := len(all) - len(recs); hidden > 0 {
		fmt.Printf("… %d older run(s) not shown — use --limit %d (or --limit 0 for all)\n", hidden, len(all))
	}
	return nil
}

// defaultRunsLimit caps `conductor runs` output by default. It is deliberately
// generous: the newest runs are shown first, so a just-recorded run is always
// within it, and anything elided is reported rather than silently dropped.
const defaultRunsLimit = 200

// printRunDetail renders one recorded run.
func printRunDetail(dir, id string) error {
	rec, err := store.ReadHistory(dir, id)
	if err != nil {
		return err
	}
	fmt.Printf("run %s — %s\n", rec.ID, rec.Status)
	fmt.Printf("  trigger: %s   target: %s\n", triggerLabel(rec), targetLabel(rec))
	fmt.Printf("  started: %s", rec.Started.Format(time.RFC3339))
	if !rec.Finished.IsZero() {
		fmt.Printf("   took: %s", rec.Finished.Sub(rec.Started).Round(time.Millisecond))
	}
	fmt.Println()
	if rec.Tokens > 0 || rec.CostUSD > 0 {
		fmt.Printf("  spend: %s (%d tokens)\n", costLabel(rec.CostUSD, rec.ApproxCost), rec.Tokens)
	}
	if rec.RetryOf != "" {
		fmt.Printf("  retry of: %s\n", rec.RetryOf)
	}
	if rec.Error != "" {
		fmt.Printf("  error: %s (step %s)\n", rec.Error, rec.FailedStep)
	}
	fmt.Println("\nsteps:")
	for _, s := range rec.Steps {
		dur := ""
		if s.DurationMS > 0 {
			dur = (time.Duration(s.DurationMS) * time.Millisecond).Round(time.Millisecond).String()
		}
		cost := ""
		if s.CostUSD > 0 || s.Tokens > 0 {
			cost = fmt.Sprintf("  $%.4f/%dtok", s.CostUSD, s.Tokens)
		}
		note := ""
		if s.ContinuedOnError {
			note = "  (continued)"
		}
		fmt.Printf("  [%d] %-20s %-8s %8s%s%s\n", s.Index, s.ID, s.Status, dur, cost, note)
		if s.Error != "" {
			fmt.Printf("        error: %s\n", s.Error)
		}
		printKV("        in:  ", s.Inputs)
		printKV("        out: ", s.Outputs)
	}
	fmt.Printf("\nretry: conductor runs retry %s", rec.ID)
	if rec.FailedStep != "" {
		fmt.Printf("   (resumes from failed step %q; --from <step> overrides)", rec.FailedStep)
	} else {
		fmt.Printf(" --from <step>")
	}
	fmt.Println()
	return nil
}

// printKV renders a small inputs/outputs map compactly (one line, clipped).
func printKV(prefix string, m map[string]any) {
	if len(m) == 0 {
		return
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	line := strings.Join(parts, " ")
	if len(line) > 160 {
		line = line[:160] + "…"
	}
	fmt.Println(prefix + line)
}

// runsRetry asks the running daemon to re-run a recorded execution.
func runsRetry(cfg *config.Config, rest []string) error {
	if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
		return fmt.Errorf("usage: conductor runs retry <id> [--from <step>] [--force-replay]")
	}
	id := rest[0]
	from := ""
	force := false
	for i := 1; i < len(rest); i++ {
		switch {
		case rest[i] == "--from" && i+1 < len(rest):
			from = rest[i+1]
			i++
		case rest[i] == "--force-replay":
			force = true
		}
	}
	resp, err := sendControl(cfg, controlRequest{Cmd: "retry", RunID: id, FromStep: from, ForceReplay: force})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Println(resp.Msg)
	return nil
}

// historyDirPath derives the run-history directory the daemon writes.
func historyDirPath(cfg *config.Config) string {
	if cfg.Store.StateFile == "" {
		return filepath.Join(config.StateDir(), "history")
	}
	return filepath.Join(filepath.Dir(cfg.Store.StateFile), "history")
}

func triggerLabel(r store.RunHistory) string {
	l := r.On
	if l == "" {
		l = r.Kind
	}
	if r.Variant != "" {
		l += "/" + r.Variant
	}
	return l
}

func targetLabel(r store.RunHistory) string {
	if r.Repo == "" {
		return "-"
	}
	if r.Number > 0 {
		return fmt.Sprintf("%s#%d", r.Repo, r.Number)
	}
	return r.Repo
}

func costLabel(usd float64, approx bool) string {
	if usd == 0 {
		return "-"
	}
	s := fmt.Sprintf("$%.3f", usd)
	if approx {
		s = "~" + s
	}
	return s
}
