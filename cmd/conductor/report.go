package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/notify"
)

// cmdReport summarizes conductor activity over a window from the audit log:
// dispatches broken down by kind × outcome (ok/failed/queued/adopted/skipped), plus
// attention counts (escalate/needs_input/complete). Read-only; safe against a live
// daemon. `--days N` sets the window (default 7).
func cmdReport(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	days := 7
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--days" && i+1 < len(rest) {
			if n, err := strconv.Atoi(rest[i+1]); err == nil && n > 0 {
				days = n
			}
			i++
		}
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	f, err := os.Open(cfg.Store.AuditLog)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()

	dispatch, attention, err := tallyAudit(f, cutoff)
	if err != nil {
		return fmt.Errorf("read audit log: %w", err)
	}
	outcomes := []string{"ok", "failed", "queued", "adopted", "skipped", "shadow"}

	fmt.Printf("report — last %dd (since %s)\n\n", days, cutoff.Format("2006-01-02"))

	kinds := make([]string, 0, len(dispatch))
	for k := range dispatch {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	fmt.Println("dispatches by kind:")
	if len(kinds) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Printf("  %-20s", "kind")
		for _, oc := range outcomes {
			fmt.Printf(" %8s", oc)
		}
		fmt.Println()
		totals := map[string]int{}
		for _, k := range kinds {
			fmt.Printf("  %-20s", k)
			for _, oc := range outcomes {
				n := dispatch[k][oc]
				totals[oc] += n
				fmt.Printf(" %8d", n)
			}
			fmt.Println()
		}
		fmt.Printf("  %-20s", "TOTAL")
		for _, oc := range outcomes {
			fmt.Printf(" %8d", totals[oc])
		}
		fmt.Println()
	}

	fmt.Println("\nattention:")
	for _, ev := range []string{"escalate", "failed", "needs_input", "complete"} {
		fmt.Printf("  %-12s %d\n", ev, attention[ev])
	}

	// Spend (#36 §14): a second pass over the log for the agent_usage rows.
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		if spend, serr := tallySpend(f, cutoff); serr == nil {
			printSpend(spend)
		}
	}
	return nil
}

// digestLoop periodically emits an activity summary via the notifier (opt-in via
// notify.digest). Reuses tallyAudit over the elapsed window.
func digestLoop(ctx context.Context, cfg *config.Config, n *notify.Notifier) {
	iv := cfg.Notify.Digest.D()
	if iv <= 0 {
		return
	}
	logf("digest: enabled, every %s", iv)
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			emitDigest(ctx, cfg, n, iv)
		}
	}
}

func emitDigest(ctx context.Context, cfg *config.Config, n *notify.Notifier, window time.Duration) {
	f, err := os.Open(cfg.Store.AuditLog)
	if err != nil {
		return
	}
	defer f.Close()
	dispatch, attention, err := tallyAudit(f, time.Now().Add(-window))
	if err != nil {
		return
	}
	total, ok, failed := 0, 0, 0
	for _, m := range dispatch {
		for oc, c := range m {
			total += c
			switch oc {
			case "ok":
				ok += c
			case "failed":
				failed += c
			}
		}
	}
	n.Digest(ctx, fmt.Sprintf("last %s — dispatched %d (ok %d, failed %d); escalate %d, needs_input %d, complete %d",
		window, total, ok, failed, attention["escalate"], attention["needs_input"], attention["complete"]))
}

// tallyAudit streams a JSONL audit log and tallies dispatch outcomes (kind →
// outcome → count) and attention events (event → count), counting only rows at or
// after cutoff. Rows without a parseable ts are counted (they're recent enough to
// lack the field, or malformed — err on inclusion).
func tallyAudit(r io.Reader, cutoff time.Time) (dispatch map[string]map[string]int, attention map[string]int, err error) {
	dispatch = map[string]map[string]int{}
	attention = map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var d map[string]any
		if json.Unmarshal(sc.Bytes(), &d) != nil {
			continue
		}
		if ts, ok := d["ts"].(string); ok {
			if t, e := time.Parse(time.RFC3339, ts); e == nil && t.Before(cutoff) {
				continue
			}
		}
		switch ev, _ := d["event"].(string); ev {
		case "dispatch":
			kind, _ := d["kind"].(string)
			oc, _ := d["outcome"].(string)
			if oc == "" {
				oc = "ok" // pre-outcome rows
			}
			if dispatch[kind] == nil {
				dispatch[kind] = map[string]int{}
			}
			dispatch[kind][oc]++
		case "escalate", "failed", "needs_input", "complete":
			attention[ev]++
		}
	}
	return dispatch, attention, sc.Err()
}

// spendTally aggregates the audit's agent_usage rows (#36 §14): totals plus
// per-repo / per-day / per-workflow-or-kind breakdowns, and how much of the
// figure is runtime-reported vs estimated.
type spendTally struct {
	Tokens, ApproxRuns, Runs int
	USD                      float64
	ByRepo, ByDay, ByKind    map[string]*spendCell
	Sheds                    int // budget_shed rows in the window
}

type spendCell struct {
	Tokens int
	USD    float64
	Runs   int
}

// tallySpend streams the audit log and aggregates agent_usage rows at or
// after cutoff (same inclusion rule as tallyAudit).
func tallySpend(r io.Reader, cutoff time.Time) (spendTally, error) {
	s := spendTally{ByRepo: map[string]*spendCell{}, ByDay: map[string]*spendCell{}, ByKind: map[string]*spendCell{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var d map[string]any
		if json.Unmarshal(sc.Bytes(), &d) != nil {
			continue
		}
		day := ""
		if ts, ok := d["ts"].(string); ok {
			if t, e := time.Parse(time.RFC3339, ts); e == nil {
				if t.Before(cutoff) {
					continue
				}
				day = t.Format("2006-01-02")
			}
		}
		switch ev, _ := d["event"].(string); ev {
		case "budget_shed":
			s.Sheds++
			continue
		case "agent_usage":
		default:
			continue
		}
		tokens := intOf(d["tokens"])
		usd, _ := d["cost_usd"].(float64)
		s.Tokens += tokens
		s.USD += usd
		s.Runs++
		if approx, _ := d["approximate"].(bool); approx {
			s.ApproxRuns++
		}
		bump := func(m map[string]*spendCell, key string) {
			if key == "" {
				key = "(unknown)"
			}
			c := m[key]
			if c == nil {
				c = &spendCell{}
				m[key] = c
			}
			c.Tokens += tokens
			c.USD += usd
			c.Runs++
		}
		repo, _ := d["repo"].(string)
		bump(s.ByRepo, repo)
		bump(s.ByDay, day)
		// The workflow scope names the configured trigger where one is set;
		// the kind is the fallback grouping.
		kind, _ := d["workflow"].(string)
		if kind == "" {
			kind, _ = d["kind"].(string)
		}
		bump(s.ByKind, kind)
	}
	return s, sc.Err()
}

func intOf(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// printSpend renders the report's spend section.
func printSpend(s spendTally) {
	fmt.Println("\nspend (agent runs):")
	if s.Runs == 0 {
		fmt.Println("  (none metered)")
		if s.Sheds > 0 {
			fmt.Printf("  budget sheds: %d\n", s.Sheds)
		}
		return
	}
	approxNote := ""
	if s.ApproxRuns > 0 {
		approxNote = fmt.Sprintf("  (~%d/%d runs estimated)", s.ApproxRuns, s.Runs)
	}
	fmt.Printf("  total: $%.2f, %s tokens over %d runs ($%.3f/run)%s\n",
		s.USD, fmtTokens(s.Tokens), s.Runs, s.USD/float64(s.Runs), approxNote)
	if s.Sheds > 0 {
		fmt.Printf("  budget sheds: %d\n", s.Sheds)
	}
	printSpendCells("by repo", s.ByRepo)
	printSpendCells("by workflow/kind", s.ByKind)
	printSpendCells("by day", s.ByDay)
}

func printSpendCells(title string, m map[string]*spendCell) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]].USD > m[keys[j]].USD })
	fmt.Printf("  %s:\n", title)
	for _, k := range keys {
		c := m[k]
		fmt.Printf("    %-32s $%8.2f  %10s tok  %4d runs\n", k, c.USD, fmtTokens(c.Tokens), c.Runs)
	}
}

// fmtTokens renders a token count compactly (12.3k / 1.2m).
func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}
