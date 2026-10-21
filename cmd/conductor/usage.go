package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/cost"
)

// cmdUsage is `conductor usage [--since 24h]`: model usage, per runtime /
// model and never summed across providers. What conductor OBSERVED (its own
// agent runs: runs, turns, tokens — dollars only in usage.mode api), then
// what each configured usage source REPORTS (plan windows, utilization).
// With usage.mode off (the default) it shows nothing.
func cmdUsage(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	since := 24 * time.Hour
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--since" && i+1 < len(rest) {
			d, err := time.ParseDuration(rest[i+1])
			if err != nil || d <= 0 {
				return fmt.Errorf("--since: want a duration like 24h, got %q", rest[i+1])
			}
			since = d
			i++
		}
	}
	if !cost.ShowUsage() {
		fmt.Println("usage reporting is off (usage.mode: off, the default). Set usage.mode: subscription or api to see it. Budget caps enforce in every mode.")
		return nil
	}
	return printUsage(cfg, since)
}

func printUsage(cfg *config.Config, since time.Duration) error {
	cutoff := time.Now().Add(-since)
	f, err := os.Open(cfg.Store.AuditLog)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	s, err := tallySpend(f, cutoff)
	f.Close()
	if err != nil {
		return fmt.Errorf("read audit log: %w", err)
	}
	fmt.Printf("observed by conductor — its own agent runs in the last %s (not your plan's usage):\n", since)
	if len(s.ByModel) == 0 {
		fmt.Println("  (none)")
	} else {
		printSpendCells("by runtime / model", s.ByModel)
	}
	srcs := []config.UsageSource(nil)
	if cfg.Usage != nil {
		srcs = cfg.Usage.Sources
	}
	if len(srcs) == 0 {
		fmt.Println("\nno usage source is configured (usage.sources), so no plan window or utilization is known.")
		return nil
	}
	for _, sc := range srcs {
		src, err := cost.NewSource(cost.SourceSpec{Name: sc.Name, Type: sc.Type, URL: sc.URL, Headers: sc.Headers, Timeout: sc.Timeout.D()})
		if err == nil {
			var r cost.Report
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			r, err = src.Fetch(ctx)
			cancel()
			if err == nil {
				fmt.Printf("\nreported by usage source %s:\n", sc.Name)
				printReported(r)
				continue
			}
		}
		fmt.Printf("\nusage source %s: %v\n", sc.Name, err)
	}
	return nil
}

// printReported renders a source's report, one provider/model per line.
func printReported(r cost.Report) {
	if len(r.Providers) == 0 {
		fmt.Println("  (nothing reported)")
		return
	}
	for _, p := range r.Providers {
		line := "  " + p.Provider
		if p.Model != "" {
			line += " / " + p.Model
		}
		if p.Window != "" {
			line += "  window " + p.Window
		}
		if p.Limit > 0 {
			line += fmt.Sprintf("  %g of %g", p.Used, p.Limit)
		} else if p.Used > 0 {
			line += fmt.Sprintf("  %g used", p.Used)
		}
		if p.Unit != "" {
			line += " " + p.Unit
		}
		if p.Utilization != nil {
			line += fmt.Sprintf("  (%.0f%% used)", *p.Utilization*100)
		}
		if !p.ResetsAt.IsZero() {
			line += "  resets " + p.ResetsAt.Format(time.RFC3339)
		}
		fmt.Println(line)
	}
}
