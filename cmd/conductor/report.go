package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/NodeSpy/conductor/internal/cost"
	"io"
	"os"
	"sort"
	"strconv"
	"time"
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
	// Agent quality (#36 §18): outcome + gate rows.
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		if q, qerr := tallyQuality(f, cutoff); qerr == nil {
			printQuality(q)
		}
	}
	return nil
}

// qualityCell aggregates one agent's (or workflow's) outcomes.
type qualityCell struct {
	Merged, Closed, Reverted, Approved, Rejected, CIFailed int
	GatePassed, GateEscalated                              int
	MergedCost                                             float64
}

// acceptRate: merged out of terminally-resolved changes.
func (c *qualityCell) acceptRate() float64 {
	den := c.Merged + c.Closed + c.Rejected
	if den == 0 {
		return 0
	}
	return float64(c.Merged) / float64(den)
}

// revertRate: reverted out of merged.
func (c *qualityCell) revertRate() float64 {
	if c.Merged == 0 {
		return 0
	}
	return float64(c.Reverted) / float64(c.Merged)
}

// tallyQuality aggregates the audit's outcome rows (#36 §18) and the final
// gate verdicts (§16) per agent and per workflow.
func tallyQuality(r io.Reader, cutoff time.Time) (map[string]*qualityCell, error) {
	byAgent := map[string]*qualityCell{}
	cell := func(key string) *qualityCell {
		if key == "" {
			key = "(unattributed)"
		}
		c := byAgent[key]
		if c == nil {
			c = &qualityCell{}
			byAgent[key] = c
		}
		return c
	}
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
		agent, _ := d["agent"].(string)
		switch ev, _ := d["event"].(string); ev {
		case "outcome":
			c := cell(agent)
			usd, _ := d["cost_usd"].(float64)
			switch oc, _ := d["outcome"].(string); oc {
			case "merged":
				c.Merged++
				c.MergedCost += usd
			case "closed":
				c.Closed++
			case "reverted":
				c.Reverted++
			case "approved":
				c.Approved++
			case "rejected":
				c.Rejected++
			case "ci_failed":
				c.CIFailed++
			}
		case "gate":
			c := cell(agent)
			switch oc, _ := d["outcome"].(string); oc {
			case "pass":
				c.GatePassed++
			case "escalated":
				c.GateEscalated++
			}
		}
	}
	return byAgent, sc.Err()
}

// printQuality renders the agent-quality section.
func printQuality(q map[string]*qualityCell) {
	if len(q) == 0 {
		return
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("\nagent quality (outcomes):")
	// $/merged sums an agent's runs, which may span models: shown only in
	// usage.mode api, where dollars are the one comparable unit.
	dollars := cost.ShowDollars()
	hdr := fmt.Sprintf("  %-20s %6s %6s %6s %6s %6s %8s %8s",
		"agent", "merged", "closed", "revert", "reject", "gate✓/✗", "accept", "revert%")
	if dollars {
		hdr += fmt.Sprintf(" %10s", "$/merged")
	}
	fmt.Println(hdr)
	for _, k := range keys {
		c := q[k]
		line := fmt.Sprintf("  %-20s %6d %6d %6d %6d %3d/%-3d %7.0f%% %7.0f%%",
			k, c.Merged, c.Closed, c.Reverted, c.Rejected,
			c.GatePassed, c.GateEscalated, c.acceptRate()*100, c.revertRate()*100)
		if dollars {
			perMerged := "-"
			if c.Merged > 0 && c.MergedCost > 0 {
				perMerged = fmt.Sprintf("$%.3f", c.MergedCost/float64(c.Merged))
			}
			line += fmt.Sprintf(" %10s", perMerged)
		}
		fmt.Println(line)
	}
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

// spendTally aggregates the audit's agent_usage rows (#36 §14) PER RUNTIME
// AND MODEL — usage is never summed across providers — with per-repo /
// per-day / per-workflow-or-kind breakdowns inside each, and how much of it
// is runtime-reported vs estimated.
type spendTally struct {
	Runs                  int
	ByModel               map[string]*spendCell
	ByRepo, ByDay, ByKind map[string]*spendCell // keyed "<key>  [<runtime> / <model>]"
	Sheds                 int                   // budget_shed rows in the window
}

type spendCell struct {
	Tokens, Turns, Runs, ApproxRuns int
	USD                             float64
}

// tallySpend streams the audit log and aggregates agent_usage rows at or
// after cutoff (same inclusion rule as tallyAudit).
func tallySpend(r io.Reader, cutoff time.Time) (spendTally, error) {
	s := spendTally{ByModel: map[string]*spendCell{}, ByRepo: map[string]*spendCell{}, ByDay: map[string]*spendCell{}, ByKind: map[string]*spendCell{}}
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
		tokens, turns := intOf(d["tokens"]), intOf(d["turns"])
		usd, _ := d["cost_usd"].(float64)
		approx, _ := d["approximate"].(bool)
		s.Runs++
		rt, _ := d["runtime"].(string)
		model, _ := d["model"].(string)
		mk := orUnknown(rt) + " / " + orUnknown(model)
		bump := func(m map[string]*spendCell, key string) {
			c := m[key]
			if c == nil {
				c = &spendCell{}
				m[key] = c
			}
			c.Tokens += tokens
			c.Turns += turns
			c.USD += usd
			c.Runs++
			if approx {
				c.ApproxRuns++
			}
		}
		bump(s.ByModel, mk)
		repo, _ := d["repo"].(string)
		bump(s.ByRepo, orUnknown(repo)+"  ["+mk+"]")
		bump(s.ByDay, orUnknown(day)+"  ["+mk+"]")
		// The workflow scope names the configured trigger where one is set;
		// the kind is the fallback grouping.
		kind, _ := d["workflow"].(string)
		if kind == "" {
			kind, _ = d["kind"].(string)
		}
		bump(s.ByKind, orUnknown(kind)+"  ["+mk+"]")
	}
	return s, sc.Err()
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func intOf(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// printSpend renders the report's usage section as usage.mode allows: off,
// nothing but the budget sheds (enforcement, not usage); subscription, the
// units conductor observed per runtime/model; api, with dollars.
func printSpend(s spendTally) {
	if !cost.ShowUsage() {
		if s.Sheds > 0 {
			fmt.Printf("\nbudget sheds: %d\n", s.Sheds)
		}
		return
	}
	fmt.Println("\nusage (observed by conductor — its own agent runs, per runtime / model):")
	if s.Runs == 0 {
		fmt.Println("  (none metered)")
		if s.Sheds > 0 {
			fmt.Printf("  budget sheds: %d\n", s.Sheds)
		}
		return
	}
	if s.Sheds > 0 {
		fmt.Printf("  budget sheds: %d\n", s.Sheds)
	}
	printSpendCells("by runtime / model", s.ByModel)
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
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]].Tokens != m[keys[j]].Tokens {
			return m[keys[i]].Tokens > m[keys[j]].Tokens
		}
		return keys[i] < keys[j]
	})
	fmt.Printf("  %s:\n", title)
	for _, k := range keys {
		c := m[k]
		line := fmt.Sprintf("    %-48s %10s tok  %5d turns  %4d runs", k, fmtTokens(c.Tokens), c.Turns, c.Runs)
		if cost.ShowDollars() {
			line += fmt.Sprintf("  $%8.2f", c.USD)
		}
		if c.ApproxRuns > 0 {
			line += fmt.Sprintf("  (~%d estimated)", c.ApproxRuns)
		}
		fmt.Println(line)
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
