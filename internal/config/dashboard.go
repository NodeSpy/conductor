package config

import (
	"fmt"
	"net"
	"strconv"
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
// This is the Phase 1 foundation shape. Only `localhost` is bound today;
// `tailscale`, TLS, sessions and auth arrive in Phase 2, at which point this
// block gains the `tailscale:` and `session:` sub-blocks from the design. The
// keyword-only `listen:` is the forward-compatible contract: new listeners are
// added as keywords, never as raw addresses.
type DashboardConfig struct {
	// Enabled turns the listener on. Off by default (the zero value), which is
	// why an absent or disabled block leaves the daemon exactly as it was.
	Enabled bool `yaml:"enabled"`
	// Listen names the interfaces to bind, by keyword only (localhost,
	// tailscale). Empty with enabled defaults to [localhost].
	Listen []string `yaml:"listen"`
	// Port is the TCP port for every listener (default 7788).
	Port int `yaml:"port"`
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
