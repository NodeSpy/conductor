package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubPaseo(t *testing.T, providerLS string) *[][]string {
	t.Helper()
	var calls [][]string
	old := paseoRun
	paseoRun = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) > 1 && args[0] == "provider" {
			return []byte(providerLS), nil
		}
		return []byte("ok"), nil
	}
	t.Cleanup(func() { paseoRun = old })
	return &calls
}

func setupOut(t *testing.T, args ...string) string {
	t.Helper()
	f, _ := os.CreateTemp(t.TempDir(), "out")
	if err := cmdPaseoSetup(args, f); err != nil {
		t.Fatalf("setup %v: %v", args, err)
	}
	f.Close()
	b, _ := os.ReadFile(f.Name())
	return string(b)
}

func TestPaseoSetupWritesWrapsAndReloads(t *testing.T) {
	calls := stubPaseo(t, `[{"provider":"claude","status":"available"},{"provider":"pi","status":"available"}]`)
	home := t.TempDir()
	cfg := filepath.Join(home, "config.json")
	os.WriteFile(cfg, []byte(`{"agents":{"providers":{"claude":{"command":["/opt/claude-wrap"]},"pi":{"enabled":true}}}}`), 0o600)
	out := setupOut(t, "--home", home, "--conductor", "/usr/local/bin/conductor", "--config", "/etc/c.yaml")
	if !strings.Contains(out, "added     claude-sandboxed") || !strings.Contains(out, "added     pi-sandboxed") || !strings.Contains(out, "added=3") || !strings.Contains(out, "paseo reloaded") {
		t.Fatalf("out:\n%s", out)
	}
	var c struct {
		Agents struct {
			Providers map[string]struct {
				Command []string `json:"command"`
				Extends string   `json:"extends"`
			} `json:"providers"`
		} `json:"agents"`
	}
	b, _ := os.ReadFile(cfg)
	json.Unmarshal(b, &c)
	got := strings.Join(c.Agents.Providers["claude-sandboxed"].Command, " ")
	if got != "/usr/local/bin/conductor sandbox exec --config /etc/c.yaml --tool claude -- /opt/claude-wrap" {
		t.Fatalf("claude wraps the operator's own command: %q", got)
	}
	if strings.Join(c.Agents.Providers["codex-sandboxed"].Command, " ") != "/usr/local/bin/conductor sandbox exec --config /etc/c.yaml --tool codex -- codex" {
		t.Fatalf("codex: %v", c.Agents.Providers["codex-sandboxed"].Command)
	}
	last := (*calls)[len(*calls)-1]
	if strings.Join(last, " ") != "reload --home "+home {
		t.Fatalf("reload of THIS home: %v", *calls)
	}
	// Second run: nothing to do, no reload.
	n := len(*calls)
	out = setupOut(t, "--home", home, "--conductor", "/usr/local/bin/conductor", "--config", "/etc/c.yaml")
	if !strings.Contains(out, "added=0 updated=0 unchanged=3") {
		t.Fatalf("idempotent:\n%s", out)
	}
	for _, c := range (*calls)[n:] {
		if c[0] == "reload" {
			t.Fatal("no change, no reload")
		}
	}
}

func TestPaseoSetupSkipsPiWhenAbsentAndHonorsDryRun(t *testing.T) {
	calls := stubPaseo(t, `[{"provider":"claude","status":"available"},{"provider":"pi","status":"unavailable"}]`)
	home := t.TempDir()
	out := setupOut(t, "--home", home, "--dry-run")
	if strings.Contains(out, "added     pi-sandboxed") || !strings.Contains(out, "pi: not offered") || !strings.Contains(out, "added=2") {
		t.Fatalf("out:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "config.json")); err == nil {
		t.Fatal("--dry-run writes nothing")
	}
	for _, c := range *calls {
		if c[0] == "reload" {
			t.Fatal("--dry-run reloads nothing")
		}
	}
}

// The real paseo config (a COPY — the live file is never written by a
// test): conductor's entries are added and every operator provider is kept
// value for value.
func TestPaseoSetupOnACopyOfARealConfig(t *testing.T) {
	stubPaseo(t, `[]`)
	src := os.Getenv("CONDUCTOR_TEST_PASEO_CONFIG")
	if src == "" {
		t.Skip("set CONDUCTOR_TEST_PASEO_CONFIG to a paseo config.json to merge a copy of it")
	}
	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cp := filepath.Join(home, "config.json")
	os.WriteFile(cp, orig, 0o600)
	out := setupOut(t, "--home", home, "--no-reload")
	t.Log(out)
	var before, after map[string]any
	json.Unmarshal(orig, &before)
	b, _ := os.ReadFile(cp)
	json.Unmarshal(b, &after)
	for k, v := range before {
		if k == "agents" {
			continue
		}
		bv, _ := json.Marshal(v)
		av, _ := json.Marshal(after[k])
		if string(bv) != string(av) {
			t.Errorf("top-level %s changed", k)
		}
	}
	bp := before["agents"].(map[string]any)["providers"].(map[string]any)
	ap := after["agents"].(map[string]any)["providers"].(map[string]any)
	for name, v := range bp {
		bv, _ := json.Marshal(v)
		av, _ := json.Marshal(ap[name])
		if string(bv) != string(av) {
			t.Errorf("operator provider %s changed", name)
		}
	}
	if ap["claude-sandboxed"] == nil || ap["codex-sandboxed"] == nil {
		t.Fatal("entries added")
	}
}

func TestPaseoSetupFlags(t *testing.T) {
	if _, err := parsePaseoSetup([]string{"--config", "a", "--socket", "b"}); err == nil {
		t.Fatal("--config and --socket are exclusive")
	}
	if _, err := parsePaseoSetup([]string{"--bogus"}); err == nil {
		t.Fatal("unknown flag")
	}
	o, err := parsePaseoSetup([]string{"--providers", "claude, pi", "--no-reload"})
	if err != nil || strings.Join(o.providers, ",") != "claude,pi" || !o.noReload {
		t.Fatalf("%+v %v", o, err)
	}
}
