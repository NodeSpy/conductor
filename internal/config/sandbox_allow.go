package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SandboxAllow loosens the agent sandbox for the launches of one scope:
// what its detection would kill and its egress proxy would refuse, an agent
// whose work needs it may do. It is written in sandbox: (every launch) or in
// any isolation: block (a runtime, workflow, trigger or step: that scope's
// agent launches, on top of sandbox.allow). Only the operator's own config
// grants it: a pack requests one in its manifest, and it applies only when
// the operator grants it from packs: (grant:); an agent-authored workflow's
// is ignored. Everything else still applies to an allowed launch. An allowed
// event is audited (allowed_by: the scope) instead of killed or refused.
type SandboxAllow struct {
	// Tunnels are tunnel tools the watcher lets run (cloudflared, ssh,
	// openssl's s_client, …): a program name, or name@host[:port] to also
	// add host[:port] (bare host: 443; globs as in egress) to the scope's
	// egress allowlist. The proxy cannot tell which process opened a
	// connection, so the destination is reachable by any process of the
	// scope's launches.
	Tunnels []string `yaml:"tunnels,omitempty"`
	// NonTLS are host:port targets (globs as in egress) exempt from the TLS
	// first-bytes check: raw SSH to a bastion. They must still be allowed
	// by the scope's egress (a tunnel's @host, network.egress or
	// sandbox.egress).
	NonTLS []string `yaml:"non_tls,omitempty"`
	// ReverseShell lets a shell whose stdin or stdout is a network socket
	// run.
	ReverseShell bool `yaml:"reverse_shell,omitempty"`
	// Pty lets a process holding both a pty and a network socket run.
	Pty bool `yaml:"pty,omitempty"`
	// Origin names the scope that wrote it (set at load: the audit and
	// validate show it).
	Origin string `yaml:"-"`
}

// IsolationDetect overrides sandbox.detect for one scope.
type IsolationDetect struct {
	// Action is kill|alert|off (see SandboxDetect.Action).
	Action string `yaml:"action,omitempty"`
	// Tripwires plants decoy credentials (default: sandbox.detect's).
	Tripwires *bool `yaml:"tripwires,omitempty"`
}

// Empty reports an allowance that allows nothing.
func (a *SandboxAllow) Empty() bool {
	return a == nil || (len(a.Tunnels) == 0 && len(a.NonTLS) == 0 && !a.ReverseShell && !a.Pty)
}

// TunnelTools are the program names a's tunnels name, lower-cased.
func (a *SandboxAllow) TunnelTools() []string {
	if a == nil {
		return nil
	}
	var out []string
	for _, t := range a.Tunnels {
		tool, _, _ := strings.Cut(strings.TrimSpace(t), "@")
		out = appendUniqueStr(out, strings.ToLower(tool))
	}
	return out
}

// TunnelHosts are the destinations a's tunnels name (egress patterns).
func (a *SandboxAllow) TunnelHosts() []string {
	if a == nil {
		return nil
	}
	var out []string
	for _, t := range a.Tunnels {
		if _, host, ok := strings.Cut(strings.TrimSpace(t), "@"); ok {
			out = appendUniqueStr(out, host)
		}
	}
	return out
}

// Describe is a one-line account of what a allows.
func (a *SandboxAllow) Describe() string {
	if a.Empty() {
		return "nothing"
	}
	var parts []string
	if len(a.Tunnels) > 0 {
		parts = append(parts, "tunnel tools "+strings.Join(a.Tunnels, ", "))
	}
	if len(a.NonTLS) > 0 {
		parts = append(parts, "non-TLS traffic to "+strings.Join(a.NonTLS, ", "))
	}
	if a.ReverseShell {
		parts = append(parts, "reverse shells")
	}
	if a.Pty {
		parts = append(parts, "pty relays")
	}
	return strings.Join(parts, "; ")
}

// UnionAllow is every allowance of list in one (nil when none allows
// anything); its Origin names each contributing scope.
func UnionAllow(list ...*SandboxAllow) *SandboxAllow {
	var u SandboxAllow
	var origins []string
	for _, a := range list {
		if a.Empty() {
			continue
		}
		for _, t := range a.Tunnels {
			u.Tunnels = appendUniqueStr(u.Tunnels, t)
		}
		for _, h := range a.NonTLS {
			u.NonTLS = appendUniqueStr(u.NonTLS, h)
		}
		u.ReverseShell = u.ReverseShell || a.ReverseShell
		u.Pty = u.Pty || a.Pty
		origins = appendUniqueStr(origins, firstNonEmptyString(a.Origin, "config"))
	}
	if u.Empty() {
		return nil
	}
	u.Origin = strings.Join(origins, " + ")
	return &u
}

var allowToolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// validateSandboxAllow checks one allow: block's shape. Broad patterns
// (`*`) are the operator's to write.
func validateSandboxAllow(where string, a *SandboxAllow) error {
	if a == nil {
		return nil
	}
	for _, t := range a.Tunnels {
		tool, host, hasHost := strings.Cut(strings.TrimSpace(t), "@")
		if !allowToolName.MatchString(tool) {
			return fmt.Errorf("config: %s.tunnels: %q: want a program name (cloudflared) or name@host[:port]", where, t)
		}
		if hasHost && !validEgressPattern(host) {
			return fmt.Errorf("config: %s.tunnels: %q: the destination after @ must be host[:port]", where, t)
		}
	}
	for _, h := range a.NonTLS {
		host, port, ok := strings.Cut(strings.TrimSpace(h), ":")
		if !ok || host == "" || port == "" || strings.Contains(port, ":") {
			return fmt.Errorf("config: %s.non_tls: %q: want host:port (the port is not implied: raw traffic is rarely 443)", where, h)
		}
	}
	return nil
}

// validEgressPattern reports a host or host:port pattern as egress lists
// take them.
func validEgressPattern(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, " /@") {
		return false
	}
	host, port, hasPort := strings.Cut(p, ":")
	return host != "" && (!hasPort || (port != "" && !strings.Contains(port, ":")))
}

// validateScopeSandbox checks an isolation: block's agent-sandbox knobs.
func validateScopeSandbox(where string, iso *IsolationConfig) error {
	if iso == nil {
		return nil
	}
	if err := validateSandboxAllow(where+": isolation.allow", iso.Allow); err != nil {
		return err
	}
	if d := iso.Detect; d != nil {
		switch d.Action {
		case "", "kill", "alert", "off":
		default:
			return fmt.Errorf("config: %s: isolation.detect.action must be kill|alert|off, got %q", where, d.Action)
		}
	}
	if l := iso.EgressLimits; l != nil {
		if _, err := ParseLimitDuration(l.Idle); err != nil {
			return fmt.Errorf("config: %s: isolation.egress_limits.idle: %w", where, err)
		}
		for name, v := range map[string]string{"upload": l.Upload, "download": l.Download} {
			if _, err := ParseByteSize(v); err != nil {
				return fmt.Errorf("config: %s: isolation.egress_limits.%s: %w", where, name, err)
			}
		}
	}
	return nil
}

// noAgentSandboxKnobs refuses allow:/detect:/egress_limits: where no agent
// sandbox launch reads them (a connector, host or engine).
func noAgentSandboxKnobs(where string, iso *IsolationConfig) error {
	if iso != nil && (iso.Allow != nil || iso.Detect != nil || iso.EgressLimits != nil) {
		return fmt.Errorf("config: %s: isolation allow:/detect:/egress_limits: shape agent sandbox launches — set them on a runtime, workflow, trigger or step", where)
	}
	return nil
}

// scopeSandboxSites calls fn for every isolation: block an agent launch can
// resolve to, outer scopes first, with the scope's name.
func (c *Config) scopeSandboxSites(fn func(where string, iso *IsolationConfig)) {
	for _, n := range sortedNames(c.Runtimes) {
		if iso := c.Runtimes[n].Isolation; iso != nil {
			fn("runtime "+n, iso)
		}
	}
	for _, n := range sortedNames(c.Workflows) {
		if iso := c.Workflows[n].Isolation; iso != nil {
			fn("workflow "+n, iso)
		}
	}
	for i, tr := range c.Triggers {
		if tr.Isolation != nil {
			fn(ScopeForTrigger(tr, i).String(), tr.Isolation)
		}
	}
	c.WalkSteps(func(scope IdentityScope, slot int, s *Step) {
		if s.Isolation != nil {
			fn(StepLabel(scope, slot, *s), s.Isolation)
		}
	})
}

// labelAllowances names the scope of every allowance, for the audit.
func (c *Config) labelAllowances() {
	if c.Sandbox != nil && c.Sandbox.Allow != nil {
		c.Sandbox.Allow.Origin = "sandbox"
	}
	c.scopeSandboxSites(func(where string, iso *IsolationConfig) {
		if iso.Allow != nil && iso.Allow.Origin == "" {
			iso.Allow.Origin = where
		}
	})
}

// AllowanceNotes are the loosenings of the agent sandbox in effect, one
// line each, for `conductor validate`: every allowance (with its scope),
// every scope that weakens detection or lifts an egress limit, and every
// allowance a pack requested that the operator has not granted.
func (c *Config) AllowanceNotes() []string {
	var notes []string
	if c.Sandbox != nil && !c.Sandbox.Allow.Empty() {
		notes = append(notes, fmt.Sprintf("sandbox.allow loosens EVERY agent launch: %s", c.Sandbox.Allow.Describe()))
	}
	c.scopeSandboxSites(func(where string, iso *IsolationConfig) {
		if !iso.Allow.Empty() {
			notes = append(notes, fmt.Sprintf("%s: sandbox allowance for its agent launches: %s", where, iso.Allow.Describe()))
		}
		if d := iso.Detect; d != nil && (d.Action == "alert" || d.Action == "off" || (d.Tripwires != nil && !*d.Tripwires)) {
			what := "detection action " + firstNonEmptyString(d.Action, "unchanged")
			if d.Tripwires != nil && !*d.Tripwires {
				what += ", no tripwires"
			}
			notes = append(notes, fmt.Sprintf("%s: sandbox detection loosened for its agent launches: %s", where, what))
		}
		if l := iso.EgressLimits; l != nil {
			var off []string
			for _, kv := range [][2]string{{"idle", l.Idle}, {"upload", l.Upload}, {"download", l.Download}} {
				if strings.EqualFold(strings.TrimSpace(kv[1]), "off") {
					off = append(off, kv[0])
				}
			}
			if len(off) > 0 {
				notes = append(notes, fmt.Sprintf("%s: egress limits lifted for its agent launches: %s", where, strings.Join(off, ", ")))
			}
		}
	})
	c.WalkSteps(func(scope IdentityScope, slot int, s *Step) {
		if !s.GrantedAllow.Empty() {
			notes = append(notes, fmt.Sprintf("%s: %s: %s", StepLabel(scope, slot, *s), s.GrantedAllow.Origin, s.GrantedAllow.Describe()))
		}
	})
	notes = append(notes, c.allowanceRequests...)
	sort.Strings(notes)
	return notes
}

func appendUniqueStr(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func firstNonEmptyString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// applyAllowanceGrants applies the allowances the operator granted (grant:)
// to the pack steps each names, and records the ones still only requested.
func (st *packInstantiation) applyAllowanceGrants(ns string, inst PackInstance, man *PackManifest) error {
	granted := map[string]bool{}
	for _, name := range inst.Grant {
		req, ok := man.Allowances[name]
		if !ok {
			return fmt.Errorf("pack %q: grant: %q names no allowance this pack requests (requested: %s)", ns, name, sortedKeys(man.Allowances))
		}
		if err := validateSandboxAllow(fmt.Sprintf("pack %q allowances.%s.allow", ns, name), &req.Allow); err != nil {
			return err
		}
		if len(req.Steps) == 0 {
			return fmt.Errorf("pack %q: allowances.%s names no steps", ns, name)
		}
		for _, ref := range req.Steps {
			s, err := man.FindPackStep(ref)
			if err != nil {
				return fmt.Errorf("pack %q: allowances.%s: %w", ns, name, err)
			}
			a := req.Allow
			a.Origin = fmt.Sprintf("pack %s grant %s", ns, name)
			s.GrantedAllow = UnionAllow(s.GrantedAllow, &a)
		}
		granted[name] = true
	}
	for _, name := range sortedNames(man.Allowances) {
		if granted[name] {
			continue
		}
		req := man.Allowances[name]
		st.cfg.allowanceRequests = append(st.cfg.allowanceRequests, fmt.Sprintf(
			"pack %s requests sandbox allowance %q for %s (%s): %s — NOT granted; it applies only if you add `grant: [%s]` to packs: %s",
			ns, name, strings.Join(req.Steps, ", "), firstNonEmptyString(req.Reason, "no reason given"), req.Allow.Describe(), name, ns))
	}
	return nil
}
