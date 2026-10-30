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
	// Auth picks the sign-in and step-up methods. Absent = every method,
	// password only with TOTP, step-up by passkey, SSH or TOTP.
	Auth DashboardAuth `yaml:"auth"`
	// Bind adds listeners on explicit addresses, each with the hostnames it
	// answers to (a Host allowlist, against DNS rebinding) and an optional TLS
	// certificate. Plain HTTP off loopback needs allow_plain_http: true.
	Bind []DashboardBind `yaml:"bind,omitempty"`
}

// Dashboard sign-in / step-up method names (dashboard.auth).
const (
	DashboardMethodPasskey  = "passkey"
	DashboardMethodSSH      = "ssh"
	DashboardMethodPassword = "password"
	DashboardMethodTOTP     = "totp"
)

// DashboardAuth is the `dashboard.auth:` block.
type DashboardAuth struct {
	// Methods are the enabled sign-in methods: passkey, ssh, password. Absent
	// means all three; an explicit empty list is refused.
	Methods []string `yaml:"methods"`
	// PasswordRequiresTOTP makes a password sign-in also need a TOTP code (the
	// default). Setting it false allows a password alone — an explicit loosen.
	PasswordRequiresTOTP *bool `yaml:"password_requires_totp"`
	// Stepup are the methods that satisfy a step-up: passkey, ssh, totp, and
	// password (only with password_requires_totp: false). Absent means passkey,
	// ssh, totp.
	Stepup []string `yaml:"stepup"`
	// ConfigStepup decides which config applies need a step-up when sign-in
	// is on. all (the default): every apply and every restore. guarded: only
	// an apply that touches something conductor acts on (operators, the
	// dashboard and store blocks, imports, triggers, workflows, connectors,
	// runtimes, packs, plugins, secrets, …) or that loosens a guardrail; an
	// edit confined to the display-only paths (pricing, usage.mode) applies on
	// the session alone. guarded is an explicit loosen.
	ConfigStepup string `yaml:"config_stepup"`
}

// Config step-up modes (dashboard.auth.config_stepup).
const (
	ConfigStepupAll     = "all"
	ConfigStepupGuarded = "guarded"
)

// EffectiveConfigStepup returns the config step-up mode, defaulted to all.
func (a DashboardAuth) EffectiveConfigStepup() string {
	if a.ConfigStepup == "" {
		return ConfigStepupAll
	}
	return a.ConfigStepup
}

// EffectiveMethods returns the sign-in methods, defaulted.
func (a DashboardAuth) EffectiveMethods() []string {
	if a.Methods == nil {
		return []string{DashboardMethodPasskey, DashboardMethodSSH, DashboardMethodPassword}
	}
	return a.Methods
}

// PasswordNeedsTOTP reports whether a password needs a TOTP code (default true).
func (a DashboardAuth) PasswordNeedsTOTP() bool {
	return a.PasswordRequiresTOTP == nil || *a.PasswordRequiresTOTP
}

// EffectiveStepup returns the step-up methods, defaulted.
func (a DashboardAuth) EffectiveStepup() []string {
	if a.Stepup == nil {
		return []string{DashboardMethodPasskey, DashboardMethodSSH, DashboardMethodTOTP}
	}
	return a.Stepup
}

// DashboardBind is one `dashboard.bind:` listener.
type DashboardBind struct {
	// Address is host:port to bind ("0.0.0.0:7790", "192.168.1.5:7790").
	Address string `yaml:"address"`
	// Hostnames are the Host values the listener answers to; any other Host is
	// refused. With TLS, each non-IP hostname is a passkey relying party.
	Hostnames []string `yaml:"hostnames"`
	// CertFile and KeyFile are a PEM pair; both set = HTTPS, both empty = HTTP.
	CertFile string `yaml:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file,omitempty"`
	// AllowPlainHTTP acknowledges serving plain HTTP on a non-loopback address:
	// the session cookie and everything else cross the network unencrypted.
	AllowPlainHTTP bool `yaml:"allow_plain_http,omitempty"`
}

// TLS reports whether the listener serves HTTPS.
func (b DashboardBind) TLS() bool { return b.CertFile != "" && b.KeyFile != "" }

// Loopback reports whether the address is a loopback address (plain HTTP there
// is a secure context and needs no acknowledgment).
func (b DashboardBind) Loopback() bool {
	host, _, err := net.SplitHostPort(b.Address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	// TLS is on (the default: HTTPS with the MagicDNS cert) or off (plain HTTP
	// on the tailnet addresses — WireGuard already encrypts the hop; WhoIs is
	// still enforced; passkeys aren't offered, the other methods work).
	TLS DashboardTLS `yaml:"tls"`
}

// DashboardTLS is `dashboard.tailscale.tls:` — on (default) or off.
type DashboardTLS struct{ Off bool }

// UnmarshalYAML accepts on/off (or true/false).
func (t *DashboardTLS) UnmarshalYAML(n *yaml.Node) error {
	switch strings.ToLower(strings.TrimSpace(n.Value)) {
	case "on", "true", "":
		t.Off = false
	case "off", "false":
		t.Off = true
	default:
		return fmt.Errorf("dashboard.tailscale.tls: %q is not valid — use on or off", n.Value)
	}
	return nil
}

// TLSOn reports whether the tailnet listener serves HTTPS.
func (d DashboardTailscale) TLSOn() bool { return !d.TLS.Off }

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
	if err := d.Auth.validate(); err != nil {
		return err
	}
	if err := d.validateBind(); err != nil {
		return err
	}
	if d.Tailscale.TLS.Off && !d.Tailscale.Cert.Auto() {
		return fmt.Errorf("config: dashboard.tailscale.cert is set but tailscale.tls is off — drop one")
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

// validate checks dashboard.auth: known, non-duplicate methods; a non-empty
// list when given; and password as a step-up only with the password-only knob.
func (a DashboardAuth) validate() error {
	check := func(key string, list []string, allowed ...string) error {
		if list != nil && len(list) == 0 {
			return fmt.Errorf("config: dashboard.auth.%s: name at least one method (or remove the key for the default)", key)
		}
		seen := map[string]bool{}
		for i, m := range list {
			ok := false
			for _, x := range allowed {
				if m == x {
					ok = true
				}
			}
			if !ok {
				return fmt.Errorf("config: dashboard.auth.%s[%d]: %q is not a method here — use %s", key, i, m, strings.Join(allowed, ", "))
			}
			if seen[m] {
				return fmt.Errorf("config: dashboard.auth.%s: %q is listed twice", key, m)
			}
			seen[m] = true
		}
		return nil
	}
	if err := check("methods", a.Methods, DashboardMethodPasskey, DashboardMethodSSH, DashboardMethodPassword); err != nil {
		return err
	}
	if err := check("stepup", a.Stepup, DashboardMethodPasskey, DashboardMethodSSH, DashboardMethodTOTP, DashboardMethodPassword); err != nil {
		return err
	}
	switch a.ConfigStepup {
	case "", ConfigStepupAll, ConfigStepupGuarded:
	default:
		return fmt.Errorf("config: dashboard.auth.config_stepup: want all or guarded, got %q", a.ConfigStepup)
	}
	hasPassword := false
	for _, m := range a.EffectiveMethods() {
		if m == DashboardMethodPassword {
			hasPassword = true
		}
	}
	if !a.PasswordNeedsTOTP() && !hasPassword {
		return fmt.Errorf("config: dashboard.auth.password_requires_totp: false has no effect without password in auth.methods — remove it")
	}
	for _, m := range a.Stepup {
		if m == DashboardMethodPassword && (a.PasswordNeedsTOTP() || !hasPassword) {
			return fmt.Errorf("config: dashboard.auth.stepup: password counts as a step-up only with password in auth.methods and password_requires_totp: false (a password alone is not a full credential otherwise)")
		}
	}
	return nil
}

// validateBind checks the dashboard.bind listeners.
func (d DashboardConfig) validateBind() error {
	seen := map[string]bool{}
	for i, b := range d.Bind {
		where := fmt.Sprintf("config: dashboard.bind[%d]", i)
		host, port, err := net.SplitHostPort(b.Address)
		if err != nil || port == "" {
			return fmt.Errorf("%s.address: %q must be host:port (e.g. 0.0.0.0:7790)", where, b.Address)
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("%s.address: port %q is out of range", where, port)
		} else if p == d.EffectivePort() {
			return fmt.Errorf("%s.address: port %d is the dashboard's own port (localhost/tailscale bind it) — use another", where, p)
		}
		if host != "" && host != "localhost" && net.ParseIP(host) == nil {
			return fmt.Errorf("%s.address: %q must be an IP address (or localhost)", where, host)
		}
		if seen[b.Address] {
			return fmt.Errorf("%s.address: %q is bound twice", where, b.Address)
		}
		seen[b.Address] = true
		if len(b.Hostnames) == 0 {
			return fmt.Errorf("%s.hostnames: name the hostnames (or IPs) browsers use to reach it — any other Host is refused (DNS rebinding)", where)
		}
		for j, h := range b.Hostnames {
			if h == "" || strings.ContainsAny(h, "/:@ ") && net.ParseIP(strings.Trim(h, "[]")) == nil {
				return fmt.Errorf("%s.hostnames[%d]: %q must be a bare hostname or IP (no scheme, port or path)", where, j, h)
			}
		}
		if (b.CertFile == "") != (b.KeyFile == "") {
			return fmt.Errorf("%s: TLS needs both cert_file and key_file", where)
		}
		if b.TLS() && b.AllowPlainHTTP {
			return fmt.Errorf("%s: allow_plain_http is for listeners without TLS — remove it", where)
		}
		if !b.TLS() && !b.Loopback() && !b.AllowPlainHTTP {
			return fmt.Errorf("%s: plain HTTP on %s sends the session cookie unencrypted — add cert_file/key_file, or set allow_plain_http: true to accept that", where, b.Address)
		}
	}
	return nil
}
