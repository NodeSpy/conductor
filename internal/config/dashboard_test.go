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

func TestDashboardTailscaleDefaults(t *testing.T) {
	cfg, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [localhost, tailscale]
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Dashboard.BindsTailscale() {
		t.Error("listen: [localhost, tailscale] should bind tailscale")
	}
	if !cfg.Dashboard.Tailscale.Cert.Auto() {
		t.Error("an unset cert should default to auto")
	}
	if cfg.Dashboard.Tailscale.AllowTagged {
		t.Error("allow_tagged should default to false")
	}
	if got := cfg.Dashboard.Session.IdleD(); got != DefaultSessionIdle {
		t.Errorf("default session idle = %s, want %s", got, DefaultSessionIdle)
	}
	if got := cfg.Dashboard.Session.MaxD(); got != DefaultSessionMax {
		t.Errorf("default session max = %s, want %s", got, DefaultSessionMax)
	}
}

func TestDashboardCertFilePair(t *testing.T) {
	cfg, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [tailscale]
  tailscale:
    cert: { cert_file: /etc/ssl/box.crt, key_file: /etc/ssl/box.key }
    allow_tagged: true
    allow_users: [you@example.com]
  session: { idle: 15m, max: 8h }
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := cfg.Dashboard.Tailscale.Cert
	if c.Auto() || c.CertFile != "/etc/ssl/box.crt" || c.KeyFile != "/etc/ssl/box.key" {
		t.Errorf("cert pair not parsed: %+v", c)
	}
	if !cfg.Dashboard.Tailscale.AllowTagged {
		t.Error("allow_tagged: true not read")
	}
	if cfg.Dashboard.Session.IdleD() != 15*60*1e9 {
		t.Errorf("session idle = %s", cfg.Dashboard.Session.IdleD())
	}
}

func TestDashboardCertBadKeyword(t *testing.T) {
	_, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [tailscale]
  tailscale: { cert: sometimes }
`)
	if err == nil || !strings.Contains(err.Error(), "cert") {
		t.Fatalf("a bad cert keyword should be refused naming cert, got: %v", err)
	}
}

func TestDashboardCertPairNeedsTailnet(t *testing.T) {
	_, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [localhost]
  tailscale: { cert: { cert_file: a.crt, key_file: a.key } }
`)
	if err == nil || !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("a cert pair without the tailnet listener should be refused, got: %v", err)
	}
}

func TestDashboardSessionIdleExceedsMax(t *testing.T) {
	_, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  session: { idle: 24h, max: 12h }
`)
	if err == nil || !strings.Contains(err.Error(), "idle") {
		t.Fatalf("idle > max should be refused, got: %v", err)
	}
}

func TestDashboardAllowUsersBlank(t *testing.T) {
	_, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [tailscale]
  tailscale: { allow_users: [""] }
`)
	if err == nil || !strings.Contains(err.Error(), "allow_users") {
		t.Fatalf("a blank allow_users entry should be refused, got: %v", err)
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
