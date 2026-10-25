package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Dashboard listener keywords. These are the ONLY accepted `listen:` values:
// no bind addresses, no 0.0.0.0, and no `expose:` — the dashboard binds the
// interfaces it is told to by name and nothing else (design §6.2).
const (
	DashboardListenLocalhost = "localhost" // 127.0.0.1 + ::1, plain HTTP (a potentially-trustworthy origin)
	DashboardListenTailscale = "tailscale" // the node's tailnet addresses (Phase 2)

	// DefaultDashboardPort is the port the dashboard binds when none is set.
	DefaultDashboardPort = 7788
)

// DashboardConfig is the optional `dashboard:` block (design §6.2): conductor's
// local web surface, started inside the daemon process and sharing the live
// in-process handles (config, store, event hub). Off unless `enabled: true`,
// so a config without the block — or with it disabled — changes nothing.
//
// The keyword-only `listen:` is the forward-compatible contract: new listeners
// are added as keywords, never as raw addresses. `localhost` binds plain HTTP
// on loopback (the bootstrap and WebAuthn-trustworthy origin); `tailscale`
// binds the node's tailnet addresses with a Tailscale certificate for the
// MagicDNS name (§6.2).
type DashboardConfig struct {
	// Enabled turns the listener on. Off by default (the zero value), which is
	// why an absent or disabled block leaves the daemon exactly as it was.
	Enabled bool `yaml:"enabled"`
	// Listen names the interfaces to bind, by keyword only (localhost,
	// tailscale). Empty with enabled defaults to [localhost].
	Listen []string `yaml:"listen"`
	// Port is the TCP port for every listener (default 7788).
	Port int `yaml:"port"`
	// Tailscale configures the tailnet listener (TLS cert source and the peer
	// checks). Ignored unless `tailscale` is among the effective listeners.
	Tailscale DashboardTailscale `yaml:"tailscale"`
	// Session bounds a signed-in session's lifetime (design §6.3). Defaults:
	// idle 30m, max 12h.
	Session DashboardSession `yaml:"session"`
}

// DashboardTailscale is the `dashboard.tailscale:` sub-block (§6.2): how the
// tailnet listener gets its TLS certificate and which peers it accepts.
type DashboardTailscale struct {
	// Cert is "auto" (fetch the MagicDNS cert from tailscaled's LocalAPI) or a
	// {cert_file, key_file} pair. "auto" is the default.
	Cert DashboardCert `yaml:"cert"`
	// AllowTagged, when false (the default), refuses tailnet peers that are
	// tagged nodes (servers, CI) — only user-owned devices may reach it.
	AllowTagged bool `yaml:"allow_tagged"`
	// AllowUsers, when non-empty, narrows further: only these tailnet users
	// (WhoIs login names) may reach the listener, on top of the passkey.
	AllowUsers []string `yaml:"allow_users,omitempty"`
}

// DashboardCert is the `dashboard.tailscale.cert:` value: either the keyword
// "auto" (the LocalAPI cert for the MagicDNS name, the default) or an explicit
// {cert_file, key_file} pair (`tailscale cert` output works). The zero value
// (an absent block) is auto.
type DashboardCert struct {
	// CertFile and KeyFile are an explicit PEM pair; both empty means auto.
	CertFile string
	KeyFile  string
}

// Auto reports whether the cert comes from tailscaled's LocalAPI (the default:
// no explicit file pair set).
func (c DashboardCert) Auto() bool { return c.CertFile == "" && c.KeyFile == "" }

// UnmarshalYAML accepts the scalar "auto" or a {cert_file, key_file} map.
func (c *DashboardCert) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err == nil && s != "" {
		if s != "auto" {
			return fmt.Errorf("dashboard.tailscale.cert: %q is not valid — use \"auto\" or a {cert_file, key_file} pair", s)
		}
		c.CertFile, c.KeyFile = "", ""
		return nil
	}
	var pair struct {
		CertFile string `yaml:"cert_file"`
		KeyFile  string `yaml:"key_file"`
	}
	if err := n.Decode(&pair); err != nil {
		return fmt.Errorf("dashboard.tailscale.cert: must be \"auto\" or {cert_file, key_file}: %w", err)
	}
	if pair.CertFile == "" || pair.KeyFile == "" {
		return fmt.Errorf("dashboard.tailscale.cert: a file pair needs both cert_file and key_file")
	}
	c.CertFile, c.KeyFile = pair.CertFile, pair.KeyFile
	return nil
}

// DashboardSession bounds a session (design §6.3).
type DashboardSession struct {
	// Idle ends a session after this long with no request (default 30m).
	Idle Duration `yaml:"idle"`
	// Max is the absolute ceiling regardless of activity (default 12h).
	Max Duration `yaml:"max"`
}

// Default session lifetimes (design §6.2/§6.3).
const (
	DefaultSessionIdle = 30 * time.Minute
	DefaultSessionMax  = 12 * time.Hour
)

// IdleD returns the idle timeout or its default.
func (s DashboardSession) IdleD() time.Duration {
	if s.Idle.D() > 0 {
		return s.Idle.D()
	}
	return DefaultSessionIdle
}

// MaxD returns the absolute lifetime or its default.
func (s DashboardSession) MaxD() time.Duration {
	if s.Max.D() > 0 {
		return s.Max.D()
	}
	return DefaultSessionMax
}

// BindsTailscale reports whether the tailnet listener is among the effective
// listeners.
func (d DashboardConfig) BindsTailscale() bool {
	for _, l := range d.EffectiveListen() {
		if l == DashboardListenTailscale {
			return true
		}
	}
	return false
}

// On reports whether the dashboard listener should start.
func (d DashboardConfig) On() bool { return d.Enabled }

// EffectiveListen returns the configured listeners, defaulting to [localhost]
// when enabled with none named.
func (d DashboardConfig) EffectiveListen() []string {
	if len(d.Listen) > 0 {
		return d.Listen
	}
	return []string{DashboardListenLocalhost}
}

// EffectivePort returns the configured port or the default.
func (d DashboardConfig) EffectivePort() int {
	if d.Port != 0 {
		return d.Port
	}
	return DefaultDashboardPort
}

// BindsLocalhost reports whether the localhost listener is among the effective
// listeners. In Phase 1 this is the only listener the daemon actually binds.
func (d DashboardConfig) BindsLocalhost() bool {
	for _, l := range d.EffectiveListen() {
		if l == DashboardListenLocalhost {
			return true
		}
	}
	return false
}

// validateDashboard checks the `dashboard:` block. It runs whenever the block
// is present so listener typos surface at load, not at bind time. (An unknown
// key such as `expose:` is already refused by the strict YAML decoder.)
func (c *Config) validateDashboard() error {
	d := c.Dashboard
	for i, l := range d.Listen {
		switch l {
		case DashboardListenLocalhost, DashboardListenTailscale:
		default:
			return fmt.Errorf("config: dashboard.listen[%d]: %q is not a valid listener — only %q and %q are allowed (no addresses, no 0.0.0.0, no expose:)", i, l, DashboardListenLocalhost, DashboardListenTailscale)
		}
	}
	if d.Port < 0 || d.Port > 65535 {
		return fmt.Errorf("config: dashboard.port: %d is out of range (1–65535)", d.Port)
	}
	if !d.Enabled {
		return nil
	}
	// A port that collides with the inbound (callable/webhook) listener would
	// leave one of the two unable to bind — name it now (design §6.2).
	if c.Callable.Listen != "" {
		if p := portFromListen(c.Callable.Listen); p != 0 && p == d.EffectivePort() {
			return fmt.Errorf("config: dashboard.port %d collides with callable.listen %q — give the dashboard its own port", d.EffectivePort(), c.Callable.Listen)
		}
	}
	// An explicit cert pair is only meaningful with the tailnet listener, and
	// both halves must be present (the UnmarshalYAML already enforces the pair).
	if !d.Tailscale.Cert.Auto() && d.Tailscale.Cert.CertFile != "" && !d.BindsTailscale() {
		return fmt.Errorf("config: dashboard.tailscale.cert names a file pair but `tailscale` is not in dashboard.listen — add it, or drop the cert")
	}
	// allow_users entries are tailnet login names; a blank one is a typo that
	// would silently widen (an empty string matches nothing, but it reads like
	// "everyone").
	for i, u := range d.Tailscale.AllowUsers {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("config: dashboard.tailscale.allow_users[%d] is empty — name a tailnet user or remove the entry", i)
		}
	}
	// Session bounds must be sane: idle no longer than the absolute max, both
	// positive when set.
	if d.Session.Idle.D() < 0 || d.Session.Max.D() < 0 {
		return fmt.Errorf("config: dashboard.session idle/max must be positive durations")
	}
	if d.Session.Idle.D() > 0 && d.Session.Max.D() > 0 && d.Session.Idle.D() > d.Session.Max.D() {
		return fmt.Errorf("config: dashboard.session.idle (%s) cannot exceed session.max (%s)", d.Session.Idle.D(), d.Session.Max.D())
	}
	return nil
}

// portFromListen extracts the TCP port from a host:port (or :port) listen
// string, returning 0 when there is no parseable port.
func portFromListen(listen string) int {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}
