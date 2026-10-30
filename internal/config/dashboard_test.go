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

// --- sign-in methods, listeners (dashboard-auth-methods) ---

func TestDashboardAuthDefaultsWithoutBlock(t *testing.T) {
	cfg, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [localhost, tailscale]
`)
	if err != nil {
		t.Fatalf("a config without dashboard.auth must keep loading: %v", err)
	}
	a := cfg.Dashboard.Auth
	if got := strings.Join(a.EffectiveMethods(), ","); got != "passkey,ssh,password" {
		t.Errorf("default methods = %s", got)
	}
	if !a.PasswordNeedsTOTP() {
		t.Error("password requires TOTP by default")
	}
	if got := strings.Join(a.EffectiveStepup(), ","); got != "passkey,ssh,totp" {
		t.Errorf("default stepup = %s", got)
	}
	if !cfg.Dashboard.Tailscale.TLSOn() {
		t.Error("tailnet TLS is on by default")
	}
}

func TestDashboardAuthValidation(t *testing.T) {
	for _, c := range []struct {
		name, block, wantErr string
	}{
		{"unknown method", `auth: { methods: [passkey, magic] }`, "magic"},
		{"totp is not a sign-in method", `auth: { methods: [totp] }`, "totp"},
		{"empty methods", `auth: { methods: [] }`, "at least one"},
		{"duplicate", `auth: { methods: [ssh, ssh] }`, "twice"},
		{"password stepup needs the knob", `auth: { stepup: [password] }`, "password_requires_totp: false"},
		{"unknown stepup", `auth: { stepup: [sms] }`, "sms"},
		{"empty stepup", `auth: { stepup: [] }`, "at least one"},
		{"password_requires_totp without password", `auth: { methods: [passkey], password_requires_totp: false }`, "password"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadDoc(t, dashBase+"\ndashboard:\n  enabled: true\n  "+c.block+"\n")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want an error naming %q, got %v", c.wantErr, err)
			}
		})
	}
	cfg, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  auth:
    methods: [password]
    password_requires_totp: false
    stepup: [password, totp]
`)
	if err != nil {
		t.Fatalf("the explicit password-only setup loads: %v", err)
	}
	if cfg.Dashboard.Auth.PasswordNeedsTOTP() {
		t.Error("password_requires_totp: false must be honoured")
	}
}

func TestDashboardBindListeners(t *testing.T) {
	for _, c := range []struct {
		name, block, wantErr string
	}{
		{"needs hostnames", `bind: [{ address: "192.168.1.5:7790" }]`, "hostnames"},
		{"plain http off loopback needs the ack", `bind: [{ address: "192.168.1.5:7790", hostnames: [box.lan] }]`, "allow_plain_http"},
		{"wildcard plain needs the ack", `bind: [{ address: "0.0.0.0:7790", hostnames: [box.lan] }]`, "allow_plain_http"},
		{"cert without key", `bind: [{ address: "0.0.0.0:7790", hostnames: [box.lan], cert_file: a.crt }]`, "key_file"},
		{"bad address", `bind: [{ address: "box.lan", hostnames: [box.lan], allow_plain_http: true }]`, "address"},
		{"hostname with scheme", `bind: [{ address: "0.0.0.0:7790", hostnames: ["https://box.lan"], allow_plain_http: true }]`, "hostname"},
		{"collides with the dashboard port", `bind: [{ address: "0.0.0.0:7788", hostnames: [box.lan], allow_plain_http: true }]`, "7788"},
		{"ack on TLS is meaningless", `bind: [{ address: "0.0.0.0:7790", hostnames: [box.lan], cert_file: a.crt, key_file: a.key, allow_plain_http: true }]`, "allow_plain_http"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadDoc(t, dashBase+"\ndashboard:\n  enabled: true\n  "+c.block+"\n")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want an error naming %q, got %v", c.wantErr, err)
			}
		})
	}
	cfg, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  bind:
    - { address: "0.0.0.0:7790", hostnames: [box.lan, 192.168.1.5], cert_file: /etc/c.crt, key_file: /etc/c.key }
    - { address: "192.168.1.5:7791", hostnames: [192.168.1.5], allow_plain_http: true }
    - { address: "127.0.0.1:7792", hostnames: [localhost] }
`)
	if err != nil {
		t.Fatalf("valid binds: %v", err)
	}
	b := cfg.Dashboard.Bind
	if len(b) != 3 || !b[0].TLS() || b[1].TLS() || b[1].Loopback() || !b[2].Loopback() {
		t.Fatalf("bind parsed wrong: %+v", b)
	}
}

func TestDashboardTailscaleTLS(t *testing.T) {
	cfg, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [tailscale]
  tailscale: { tls: off }
`)
	if err != nil {
		t.Fatalf("tls: off loads: %v", err)
	}
	if cfg.Dashboard.Tailscale.TLSOn() {
		t.Error("tls: off must turn tailnet TLS off")
	}
	if _, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [tailscale]
  tailscale: { tls: sometimes }
`); err == nil {
		t.Fatal("tls must be on or off")
	}
	if _, err := loadDoc(t, dashBase+`
dashboard:
  enabled: true
  listen: [tailscale]
  tailscale: { tls: off, cert: { cert_file: a.crt, key_file: a.key } }
`); err == nil {
		t.Fatal("a cert with tls: off is a contradiction")
	}
}
