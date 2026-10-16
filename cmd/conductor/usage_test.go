package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/store"
)

func TestUsageCommand(t *testing.T) {
	defer cost.SetDisplayMode("")
	dir := t.TempDir()
	audit := filepath.Join(dir, "audit.jsonl")
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_ = os.WriteFile(audit, []byte(strings.Join([]string{
		`{"ts":"` + ts + `","event":"agent_usage","runtime":"claude-cli","model":"claude-x","tokens":1000,"turns":4,"cost_usd":0.5}`,
		`{"ts":"` + ts + `","event":"agent_usage","runtime":"codex-cli","model":"gpt-y","tokens":300,"turns":2,"cost_usd":0.2}`,
	}, "\n")+"\n"), 0o600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"providers":[{"provider":"claude","window":"5h","used":40,"limit":100,"unit":"messages","utilization":0.4}]}`))
	}))
	defer srv.Close()
	cfg := &config.Config{Usage: &config.UsageConfig{Mode: "subscription", Sources: []config.UsageSource{{Name: "proxy", Type: "http", URL: srv.URL}}}}
	cfg.Store.AuditLog = audit

	cost.SetDisplayMode("subscription")
	out, err := captureStdout(t, func() error { return printUsage(cfg, 24*time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"observed by conductor", "claude-cli / claude-x", "codex-cli / gpt-y", "reported by usage source proxy", "claude  window 5h  40 of 100 messages  (40% used)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$") || strings.Contains(out, "1.3k") {
		t.Errorf("subscription shows dollars or a cross-provider total:\n%s", out)
	}
	// No source: only what conductor observed, said so.
	cfg.Usage.Sources = nil
	out, _ = captureStdout(t, func() error { return printUsage(cfg, 24*time.Hour) })
	if !strings.Contains(out, "no usage source is configured") {
		t.Errorf("no-source note:\n%s", out)
	}
}

func TestRunsUsageByMode(t *testing.T) {
	defer cost.SetDisplayMode("")
	one := store.RunHistory{Tokens: 1200, CostUSD: 0.4, Usage: []store.ModelUsage{{Runtime: "claude-cli", Model: "claude-x", Runs: 1, Turns: 3, Tokens: 1200, CostUSD: 0.4}}}
	two := store.RunHistory{Tokens: 1500, CostUSD: 0.5, Usage: []store.ModelUsage{
		{Runtime: "claude-cli", Model: "claude-x", Runs: 1, Tokens: 1200, CostUSD: 0.4}, {Runtime: "codex-cli", Model: "gpt-y", Runs: 1, Tokens: 300, CostUSD: 0.1}}}
	cost.SetDisplayMode("")
	if l := usageLines(two); l != nil {
		t.Fatalf("off shows usage: %v", l)
	}
	if s := stepUsageLabel(store.StepRecord{Tokens: 5, CostUSD: 1}); s != "" {
		t.Fatalf("off shows step usage: %q", s)
	}
	cost.SetDisplayMode("subscription")
	l := strings.Join(usageLines(two), "\n")
	if !strings.Contains(l, "claude-cli / claude-x") || !strings.Contains(l, "codex-cli / gpt-y") || strings.Contains(l, "$") || strings.Contains(l, "1.5k") {
		t.Fatalf("subscription lines:\n%s", l)
	}
	if c := runUsageCell(two); c != "2 models" {
		t.Fatalf("a two-model run summed: %q", c)
	}
	if c := runUsageCell(one); c != "1.2ktok" {
		t.Fatalf("one model: %q", c)
	}
	cost.SetDisplayMode("api")
	if c := runUsageCell(one); !strings.Contains(c, "$0.400") {
		t.Fatalf("api: %q", c)
	}
	if l := strings.Join(usageLines(two), "\n"); !strings.Contains(l, "$0.400") || !strings.Contains(l, "$0.100") || strings.Contains(l, "$0.500") {
		t.Fatalf("api lines, per model only:\n%s", l)
	}
}
