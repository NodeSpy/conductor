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
