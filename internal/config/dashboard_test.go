package config

import (
	"strings"
	"testing"
)

const dashBase = `
connectors:
  timer:
    use: cron
    schedules: { tick: { every: 1h } }
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`

func TestDashboardAbsentIsOff(t *testing.T) {
	cfg, err := loadDoc(t, dashBase)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Dashboard.On() {
		t.Fatal("dashboard with no block should be off")
	}
	if got := cfg.Dashboard.EffectivePort(); got != DefaultDashboardPort {
		t.Errorf("default port = %d, want %d", got, DefaultDashboardPort)
	}
}

func TestDashboardEnabledDefaultsLocalhost(t *testing.T) {
	cfg, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Dashboard.On() {
		t.Fatal("enabled dashboard should be on")
	}
	if !cfg.Dashboard.BindsLocalhost() {
		t.Error("enabled dashboard with no listen: should default to localhost")
	}
}

func TestDashboardBadListenKeyword(t *testing.T) {
	_, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [localhost, 0.0.0.0]
`)
	if err == nil {
		t.Fatal("a raw address in listen: should be refused")
	}
	if !strings.Contains(err.Error(), "0.0.0.0") {
		t.Fatalf("error should name the bad listener, got: %v", err)
	}
}

func TestDashboardExposeRefused(t *testing.T) {
	// `expose:` is not a field on DashboardConfig, so the strict decoder refuses it.
	_, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  expose: tailnet
`)
	if err == nil {
		t.Fatal("expose: on the dashboard should be refused")
	}
	if !strings.Contains(err.Error(), "expose") {
		t.Fatalf("error should name expose, got: %v", err)
	}
}

func TestDashboardPortCollision(t *testing.T) {
	_, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  port: 9000
callable:
  listen: "127.0.0.1:9000"
  tokens:
    - name: ci
      bearer: supersecretvalue
      workflows: []
`)
	if err == nil {
		t.Fatal("a dashboard port colliding with callable.listen should be refused")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Fatalf("error should report the collision, got: %v", err)
	}
}
