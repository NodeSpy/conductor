package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// TestExampleConfigValidates proves the shipped connectors-model example
// loads, builds, and passes the full semantic validation — the same gate a
// live config passes at boot. Credentials are dummies; the App key path is
// swapped for a generated throwaway key.
func TestExampleConfigValidates(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := writeTempRSAKey(t)
	doc := strings.Replace(string(raw), "~/.config/conductor/github-app.pem", keyPath, 1)

	for _, v := range []string{
		"GH_WEBHOOK_SECRET", "GH_SMEE_URL", "SLACK_APP_TOKEN",
		"SLACK_BOT_TOKEN", "CW_SECRET", "GH_PAT",
	} {
		t.Setenv(v, "dummy-"+v)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !cfg.HasConnectors() {
		t.Fatal("example config should be on the connectors schema")
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAll(cfg, igs); err != nil {
		t.Fatal(err)
	}
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatalf("the example config must pass semantic validation: %v", err)
	}
	if len(stack.Integrations) == 0 {
		t.Fatal("no source integrations lowered from the example config")
	}
	for _, e := range stack.SecretErrs {
		t.Errorf("secret failed to resolve: %s", e)
	}
	for _, name := range stack.Registry.Names() {
		in, _ := stack.Registry.Get(name)
		if in.Enabled && in.DisabledReason != "" {
			t.Errorf("connector %q disabled: %s", name, in.DisabledReason)
		}
	}

	// The example's remote paseo runtime (gpu-paseo on the build-box host)
	// must resolve to an SSH-backed dispatcher override.
	bin, err := resolvePaseoBin(cfg)
	if err != nil {
		t.Fatal(err)
	}
	over, err := buildPaseoOverrides(cfg, bin, config.Retry{}, false)
	if err != nil {
		t.Fatal(err)
	}
	pd, ok := over["gpu-paseo"]
	if !ok || pd.Remote == nil || pd.Remote.Name != "build-box" {
		t.Fatalf("gpu-paseo must lower to a remote dispatcher, got %+v", over)
	}
}

// TestLegacyExampleConfigStillLoads: the retained legacy example must keep
// loading unchanged (dual-schema back-compat).
func TestLegacyExampleConfigStillLoads(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.legacy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := writeTempRSAKey(t)
	doc := strings.Replace(string(raw), "~/.config/conductor/github-app.pem", keyPath, 1)
	// The legacy example ships app_id: 0 as a fill-me-in placeholder.
	doc = strings.Replace(doc, "app_id: 0", "app_id: 123456", 1)
	for _, v := range []string{
		"GH_SMEE_URL", "GH_WEBHOOK_SECRET", "SLACK_APP_TOKEN", "SLACK_BOT_TOKEN",
		"SENTRY_CLIENT_SECRET", "PAGERDUTY_SIGNING_SECRET", "CW_SECRET",
	} {
		t.Setenv(v, "dummy")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("legacy example must keep loading: %v", err)
	}
	if cfg.HasConnectors() {
		t.Fatal("legacy example unexpectedly has connectors blocks")
	}
	igs, err := buildIntegrations(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAll(cfg, igs); err != nil {
		t.Fatal(err)
	}
}

func writeTempRSAKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	p := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStarterConfigValidates: the seeded starter must stay valid out of the
// box (disabled connector, but loadable + semantically valid).
func TestStarterConfigValidates(t *testing.T) {
	raw, err := os.ReadFile("../../config.starter.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"GH_WEBHOOK_SECRET", "GH_SMEE_URL"} {
		t.Setenv(v, "dummy")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("starter config must load: %v", err)
	}
	if _, err := buildFlowStack(cfg, nil, nil, true); err != nil {
		t.Fatalf("starter config must validate: %v", err)
	}
}

// TestPaseoRuntimeWithHost: a paseo runtime with host: is valid and gets a
// dedicated SSH-backed dispatcher; a distinct local bin gets its own too.
func TestPaseoRuntimeWithHost(t *testing.T) {
	doc := `
connectors:
  timer:
    type: cron
    schedules: { tick: { every: 1h } }
hosts:
  gpu-box: { host: gpu01.internal, user: ml }
runtimes:
  paseo:     { type: paseo, bin: /usr/local/bin/paseo, default: true }
  gpu-paseo: { type: paseo, bin: /opt/paseo, host: gpu-box }
agents:
  fixer: { provider: claude, runtime: gpu-paseo }
triggers:
  - on: timer.tick
    steps: [{ type: agent, agent: fixer, checkout: none, prompt: p }]
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("a paseo runtime with host: must validate: %v", err)
	}
	if _, err := buildFlowStack(cfg, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	bin, err := resolvePaseoBin(cfg)
	if err != nil || bin != "/usr/local/bin/paseo" {
		t.Fatalf("primary bin: %q, %v", bin, err)
	}
	over, err := buildPaseoOverrides(cfg, bin, config.Retry{}, false)
	if err != nil {
		t.Fatal(err)
	}
	pd, ok := over["gpu-paseo"]
	if !ok || pd.Remote == nil || pd.Remote.Name != "gpu-box" || pd.PaseoBin != "/opt/paseo" {
		t.Fatalf("gpu-paseo override: %+v", over)
	}
	if _, dup := over["paseo"]; dup {
		t.Fatal("the primary runtime must not get an override")
	}
}
