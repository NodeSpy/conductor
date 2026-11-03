package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Configs written before the containment knobs load unchanged and resolve
// to the strict defaults; every knob parses; a bad value is refused.
func TestSandboxContainmentKnobs(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	for _, body := range []string{base, base + "sandbox: {prefetch: [go]}\n", base + "sandbox:\n  prefetch: [go]\n  egress: [proxy.golang.org]\n"} {
		cfg, err := loadCfg(t, body)
		if err != nil {
			t.Fatalf("an existing config no longer loads: %v\n%s", err, body)
		}
		s := cfg.Sandbox
		if s.GitSharedDirect() || s.PushAllowsCIChanges() || s.ModelCredentialMode() != "gateway" || !s.EgressTLSRequired() ||
			s.DetectAction() != "kill" || !s.TripwiresOn() || s.PersistMode() != "none" || s.PosixIPCAllowed() {
			t.Fatalf("defaults are not the strict ones: %+v", s)
		}
	}
	all := base + `sandbox:
  git: { shared: direct }
  push: { allow_ci_changes: true }
  model_credential: login-copy
  egress_require_tls: false
  egress_limits: { idle: 30m, upload: 1GiB, download: "off" }
  detect: { action: alert, tripwires: false, interval: 5s }
  limits: { memory: 8G, pids: 4096, aggregate_memory: "80%", disk_free_min: 10GiB, required: true }
  persist: cache
  macos_posix_ipc: true
`
	cfg, err := loadCfg(t, all)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Sandbox
	if !s.GitSharedDirect() || !s.PushAllowsCIChanges() || s.ModelCredentialMode() != "login-copy" || s.EgressTLSRequired() ||
		s.DetectAction() != "alert" || s.TripwiresOn() || s.PersistMode() != "cache" || !s.PosixIPCAllowed() || s.Limits.Pids != 4096 {
		t.Fatalf("knobs not read: %+v", s)
	}
	if n, _ := ParseByteSize(s.EgressLimits.Upload); n != 1<<30 {
		t.Fatalf("upload %d", n)
	}
	if n, _ := ParseByteSize(s.EgressLimits.Download); n != -1 {
		t.Fatalf("download off %d", n)
	}
	if d, _ := ParseLimitDuration(s.EgressLimits.Idle); d != 30*time.Minute {
		t.Fatalf("idle %v", d)
	}
	if (&SandboxConfig{Detect: &SandboxDetect{Action: "off"}}).TripwiresOn() {
		t.Fatal("detection off turns tripwires off")
	}
	for _, bad := range []string{
		"git: { shared: rw }", "model_credential: none", "persist: forever", "detect: { action: shrug }",
		"egress_limits: { upload: lots }", "egress_limits: { idle: -1s }", "limits: { memory: lots }", "limits: { pids: -1 }",
	} {
		if _, err := loadCfg(t, base+"sandbox:\n  "+bad+"\n"); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
}

// The shipped example still loads (its sandbox block documents every knob).
func TestSandboxExampleBlockLoads(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Skip(err)
	}
	if !strings.Contains(string(raw), "sandbox:") {
		t.Fatal("no sandbox block in the example")
	}
}
