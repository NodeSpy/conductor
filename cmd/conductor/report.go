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
