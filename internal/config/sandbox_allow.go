package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"gopkg.in/yaml.v3"
	"regexp"
	"sort"
	"strings"
	"time"
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
	// Raw are host:port targets (globs as in egress) exempt from the
	// egress protocol check: any bytes, ssh or a server-speaks-first
	// protocol, to exactly these. They must still be allowed by the
	// scope's egress (a tunnel's @host, network.egress or sandbox.egress).
	Raw []string `yaml:"raw,omitempty"`
	// NonTLS is raw's earlier name, still read (RawTargets joins them).
	NonTLS []string `yaml:"non_tls,omitempty"`
	// ReverseShell lets a shell whose stdin or stdout is a network socket
	// run.
	ReverseShell bool `yaml:"reverse_shell,omitempty"`
	// Pty lets a process holding both a pty and a network socket run.
	Pty bool `yaml:"pty,omitempty"`
	// Origin names the scope that wrote it (set at load: the audit and
	// validate show it).
	Origin string `yaml:"-"`
	// Pin is a granted pack allowance's digest (allowanceDigest).
	Pin string `yaml:"-"`
}

// RawTargets are the targets exempt from the egress protocol check: raw
// and its earlier name non_tls together.
func (a *SandboxAllow) RawTargets() []string {
	if a == nil {
		return nil
	}
	out := append([]string(nil), a.Raw...)
	for _, h := range a.NonTLS {
		out = appendUniqueStr(out, h)
	}
	return out
}

// allowanceDigest pins a pack's requested allowance to what the operator
// reviews: what it allows, the steps it names, and each of those steps as
// the pack ships it (its prompt, command, code, run, options — the whole
// step definition, before the operator's overlay), not the reason's
// wording. A grant names it (`grant: [name@digest]`), so a pack update that
// changes what a granted step does, or what it is allowed, is not granted.
// man must be the pristine manifest. An error when a named step is not in
// it.
func allowanceDigest(man *PackManifest, a PackAllowance) (string, error) {
	sorted := func(l []string) []string {
		c := append([]string(nil), l...)
		sort.Strings(c)
		return c
	}
	var b strings.Builder
	fmt.Fprintf(&b, "v2\ntunnels=%s\nnon_tls=%s\nreverse_shell=%t\npty=%t\n",
		strings.Join(sorted(a.Allow.Tunnels), "\x00"), strings.Join(sorted(a.Allow.RawTargets()), "\x00"), a.Allow.ReverseShell, a.Allow.Pty)
	for _, ref := range sorted(a.Steps) {
		s, err := man.FindPackStep(ref)
		if err != nil {
			return "", err
		}
		body, err := yaml.Marshal(s)
		if err != nil {
			return "", fmt.Errorf("step %s: %w", ref, err)
		}
		fmt.Fprintf(&b, "step %q %d\n%s\n", ref, len(body), body)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:8]), nil
}

// allowancePins are the digests of every allowance man requests, from the
// pristine manifest (before the operator's overlay touches its steps). A
// request whose steps cannot be found has none; applying its grant
// reports why.
func allowancePins(man *PackManifest) map[string]string {
	pins := map[string]string{}
	for name, a := range man.Allowances {
		if d, err := allowanceDigest(man, a); err == nil {
			pins[name] = d
		}
	}
	return pins
}

// IsolationDetect overrides sandbox.detect for one scope.
type IsolationDetect struct {
	// Action is kill|alert|off (see SandboxDetect.Action).
	Action string `yaml:"action,omitempty"`
	// Tripwires plants decoy credentials (default: sandbox.detect's).
	Tripwires *bool `yaml:"tripwires,omitempty"`
	// Interval and Connections override sandbox.detect's.
	Interval    string `yaml:"interval,omitempty"`
	Connections string `yaml:"connections,omitempty"`
	// Origin names the scope that wrote it (set at load).
	Origin string `yaml:"-"`
}

// Empty reports an allowance that allows nothing.
func (a *SandboxAllow) Empty() bool {
	return a == nil || (len(a.Tunnels) == 0 && len(a.RawTargets()) == 0 && !a.ReverseShell && !a.Pty)
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

// TunnelAnyHost reports a tunnel allowed by program name alone (no @host):
// it may reach any host the launch can.
func (a *SandboxAllow) TunnelAnyHost() bool {
	if a == nil {
		return false
	}
	for _, t := range a.Tunnels {
		if !strings.Contains(t, "@") {
			return true
		}
	}
	return false
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
	if raw := a.RawTargets(); len(raw) > 0 {
		parts = append(parts, "raw (unchecked protocol) traffic to "+strings.Join(raw, ", "))
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
		for _, h := range a.RawTargets() {
			u.Raw = appendUniqueStr(u.Raw, h)
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
	for _, h := range a.RawTargets() {
		host, port, ok := strings.Cut(strings.TrimSpace(h), ":")
		if !ok || host == "" || port == "" || strings.Contains(port, ":") {
			return fmt.Errorf("config: %s.raw: %q: want host:port (the port is not implied: raw traffic is rarely 443)", where, h)
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
	if d := iso.Detect; d != nil {
		if d.Interval != "" {
			if v, err := time.ParseDuration(d.Interval); err != nil || v < 100*time.Millisecond {
				return fmt.Errorf("config: %s: isolation.detect.interval: want a duration of 100ms or more, got %q", where, d.Interval)
			}
		}
		switch d.Connections {
		case "", "alert", "close", "off":
		default:
			return fmt.Errorf("config: %s: isolation.detect.connections must be alert|close|off, got %q", where, d.Connections)
		}
	}
	if l := iso.Limits; l != nil {
		if l.CPUWeight < 0 || l.CPUWeight > 10000 {
			return fmt.Errorf("config: %s: isolation.limits.cpu_weight must be 1-10000, got %d", where, l.CPUWeight)
		}
		if l.CPU != "" && iso.Mode == "" && !cpuQuota.MatchString(l.CPU) {
			return fmt.Errorf("config: %s: isolation.limits.cpu %q: want a CPU quota as a percentage of one core (200%% is two cores)", where, l.CPU)
		}
		if l.DiskFreeMin != "" {
			if _, _, _, err := ParseDiskFreeMin(l.DiskFreeMin); err != nil {
				return fmt.Errorf("config: %s: isolation.limits.disk_free_min: %w", where, err)
			}
		}
	}
	switch iso.Persist {
	case "", "none", "cache", "home":
	default:
		return fmt.Errorf("config: %s: isolation.persist must be none|cache|home, got %q", where, iso.Persist)
	}
	if iso.EgressOnly != nil {
		for _, e := range *iso.EgressOnly {
			if strings.TrimSpace(e) == "" {
				return fmt.Errorf("config: %s: isolation.egress_only: empty entry", where)
			}
			if err := validateEgressEntry(where+": isolation.egress_only", e); err != nil {
				return err
			}
		}
	}
	if l := iso.EgressLimits; l != nil {
		if _, err := ParseLimitDuration(l.Idle); err != nil {
			return fmt.Errorf("config: %s: isolation.egress_limits.idle: %w", where, err)
		}
		for name, v := range l.sizes() {
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
		if iso.Detect != nil && iso.Detect.Origin == "" {
			iso.Detect.Origin = where
		}
		if iso.EgressLimits != nil && iso.EgressLimits.Origin == "" {
			iso.EgressLimits.Origin = where
		}
	})
}

// ScopeSources are the live config's sandbox scopes by the names their
// knobs carry (Origin): each isolation: block an agent launch can resolve
// to, and each allowance the operator granted a pack. A scope recorded
// with a workspace is recomputed from these (dispatch.Rescope), so a
// revoked grant or a removed allow: stops applying at once.
type ScopeSources struct {
	Sites  map[string]*IsolationConfig
	Grants map[string]*SandboxAllow
}

// SandboxScopeSources are c's ScopeSources.
func (c *Config) SandboxScopeSources() ScopeSources {
	src := ScopeSources{Sites: map[string]*IsolationConfig{}, Grants: map[string]*SandboxAllow{}}
	c.scopeSandboxSites(func(where string, iso *IsolationConfig) {
		if _, dup := src.Sites[where]; !dup {
			src.Sites[where] = iso
		}
	})
	for k, a := range c.grantedAllowances {
		src.Grants[k] = a
	}
	return src
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
	inspectNote := func(where string, list []InspectEntry) {
		if len(list) == 0 {
			return
		}
		hosts := make([]string, len(list))
		for i, e := range list {
			hosts[i] = e.Host
		}
		notes = append(notes, fmt.Sprintf("%s: conductor terminates TLS to %s for its agent launches (a per-launch CA they trust; requests readable by conductor)", where, strings.Join(hosts, ", ")))
	}
	if c.Sandbox != nil {
		inspectNote("sandbox.inspect", c.Sandbox.Inspect)
	}
	c.scopeSandboxSites(func(where string, iso *IsolationConfig) {
		inspectNote(where, iso.Inspect)
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
			for _, kv := range [][2]string{{"idle", l.Idle}, {"upload", l.Upload}, {"download", l.Download}, {"model_upload", l.ModelUpload}, {"model_download", l.ModelDownload}} {
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
			// Each grant with the digest it is pinned to.
			var by []string
			for _, o := range strings.Split(s.GrantedAllow.Origin, " + ") {
				if g := c.grantedAllowances[o]; g != nil && g.Pin != "" {
					o += " (pinned @" + g.Pin + ")"
				}
				by = append(by, o)
			}
			notes = append(notes, fmt.Sprintf("%s: %s: %s", StepLabel(scope, slot, *s), strings.Join(by, " + "), s.GrantedAllow.Describe()))
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
func (st *packInstantiation) applyAllowanceGrants(ns string, inst PackInstance, man *PackManifest, pins map[string]string) error {
	granted := map[string]bool{}
	for _, g := range inst.Grant {
		name, pin, _ := strings.Cut(g, "@")
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
		steps := make([]*Step, 0, len(req.Steps))
		for _, ref := range req.Steps {
			s, err := man.FindPackStep(ref)
			if err != nil {
				return fmt.Errorf("pack %q: allowances.%s: %w", ns, name, err)
			}
			steps = append(steps, s)
		}
		// A grant is for the allowance the operator reviewed — what it
		// allows and the steps it allows it to, as they were — not for
		// whatever a later version of the pack puts under its name.
		d := pins[name]
		if d == "" {
			return fmt.Errorf("pack %q: allowances.%s: its steps cannot be pinned", ns, name)
		}
		if pin != d {
			if pin == "" {
				return fmt.Errorf("pack %q: grant: %q is not pinned to what you reviewed: it allows %s for %s (as this version of the pack defines them); to grant exactly that, write `grant: [%s@%s]`", ns, name, req.Allow.Describe(), strings.Join(req.Steps, ", "), name, d)
			}
			return fmt.Errorf("pack %q: allowance %q changed since you granted it (granted %s, now %s): what it allows, its steps or what they do is not what you reviewed; it now allows %s for %s. It is not applied until you review it and re-grant `%s@%s`", ns, name, pin, d, req.Allow.Describe(), strings.Join(req.Steps, ", "), name, d)
		}
		for _, s := range steps {
			a := req.Allow
			a.Origin = fmt.Sprintf("pack %s grant %s", ns, name)
			a.Pin = d
			s.GrantedAllow = UnionAllow(s.GrantedAllow, &a)
			if st.cfg.grantedAllowances == nil {
				st.cfg.grantedAllowances = map[string]*SandboxAllow{}
			}
			st.cfg.grantedAllowances[a.Origin] = &a
		}
		granted[name] = true
	}
	for _, name := range sortedNames(man.Allowances) {
		if granted[name] {
			continue
		}
		req := man.Allowances[name]
		st.cfg.allowanceRequests = append(st.cfg.allowanceRequests, fmt.Sprintf(
			"pack %s requests sandbox allowance %q for %s (%s): %s — NOT granted; it applies only if you add `grant: [%s@%s]` to packs: %s",
			ns, name, strings.Join(req.Steps, ", "), firstNonEmptyString(req.Reason, "no reason given"), req.Allow.Describe(), name, firstNonEmptyString(pins[name], "<its steps are missing>"), ns))
	}
	return nil
}
