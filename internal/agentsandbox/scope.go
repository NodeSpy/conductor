package agentsandbox

import (
	"net"
	"slices"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Allowance loosens a launch's detection and TLS check: what the operator
// allowed it in sandbox.allow or the isolation: blocks of its scopes (see
// config.SandboxAllow). What it covers is audited as allowed, by By, and
// not killed or refused; everything else still applies.
type Allowance struct {
	// Tunnels are tunnel-tool program names the watcher lets run.
	Tunnels []string `json:"tunnels,omitempty"`
	// TunnelHosts join the launch's egress allowlist.
	TunnelHosts []string `json:"tunnel_hosts,omitempty"`
	// TunnelAnyHost: a tunnel tool is allowed by name alone, to any host
	// the launch reaches.
	TunnelAnyHost bool `json:"tunnel_any_host,omitempty"`
	// NonTLS are targets exempt from the TLS first-bytes check.
	NonTLS       []string `json:"non_tls,omitempty"`
	ReverseShell bool     `json:"reverse_shell,omitempty"`
	Pty          bool     `json:"pty,omitempty"`
	// By names the scopes that granted it.
	By string `json:"by,omitempty"`
}

// Scope is what one dispatch's own scopes change of its sandbox, resolved
// by conductor from the operator's config when the workspace is
// registered: an allowance, and overrides of the detection action,
// tripwires and egress limits.
type Scope struct {
	Allow *Allowance `json:"allow,omitempty"`
	// DetectAction is DetectKill, DetectAlert or DetectOff ("": the
	// global setting).
	DetectAction string `json:"detect_action,omitempty"`
	// Tripwires overrides sandbox.detect.tripwires (nil: the global).
	Tripwires *bool `json:"tripwires,omitempty"`
	// EgressIdle (ns), EgressUpload, EgressDownload override the global
	// limits: 0 keeps it, -1 is off.
	EgressIdle, EgressUpload, EgressDownload int64 `json:",omitempty"`
	// ModelUpload, ModelDownload override the model gateway's caps the
	// same way.
	ModelUpload, ModelDownload int64 `json:",omitempty"`
	// DetectBy and LimitsBy name the scopes the detection and egress-limit
	// overrides came from (Allow.By names the allowance's): what a recorded
	// scope is recomputed from when the config changes.
	DetectBy string `json:"detect_by,omitempty"`
	LimitsBy string `json:"limits_by,omitempty"`
	// Memory, Pids, CPU and CPUWeight are the scope's launch caps over
	// sandbox.limits ("" / 0: the global); a scope that sets one is capped
	// even where sandbox.limits is off.
	Memory    string `json:"memory,omitempty"`
	Pids      int    `json:"pids,omitempty"`
	CPU       string `json:"cpu,omitempty"`
	CPUWeight int    `json:"cpu_weight,omitempty"`
	// DiskSet: the scope's disk_free_min (DiskFreeFrac / DiskFreeBytes /
	// DiskOff) replaces the global one.
	DiskSet       bool    `json:"disk_set,omitempty"`
	DiskFreeFrac  float64 `json:"disk_free_frac,omitempty"`
	DiskFreeBytes int64   `json:"disk_free_bytes,omitempty"`
	DiskOff       bool    `json:"disk_off,omitempty"`
	// DetectInterval (ns) and Connections override sandbox.detect's.
	DetectInterval int64  `json:"detect_interval,omitempty"`
	Connections    string `json:"connections,omitempty"`
	// EgressOnlySet: the launch's egress is EgressOnly and the model
	// endpoints, nothing inherited; EgressOnlyNarrow (a pack's) keeps only
	// the entries the inherited allowlist covers.
	EgressOnlySet    bool     `json:"egress_only_set,omitempty"`
	EgressOnly       []string `json:"egress_only,omitempty"`
	EgressOnlyNarrow bool     `json:"egress_only_narrow,omitempty"`
	// ProtocolCheck overrides sandbox.egress_protocol_check (nil: the
	// global).
	ProtocolCheck *bool `json:"protocol_check,omitempty"`
	// Persist overrides sandbox.persist ("": the global).
	Persist string `json:"persist,omitempty"`
}

// JoinAllowances is every allowance of list in one (nil: none).
func JoinAllowances(list ...*Allowance) *Allowance {
	var u Allowance
	var by []string
	any := false
	for _, a := range list {
		if a == nil {
			continue
		}
		any = true
		for _, t := range a.Tunnels {
			u.Tunnels = appendUnique(u.Tunnels, strings.ToLower(t))
		}
		for _, h := range a.TunnelHosts {
			u.TunnelHosts = appendUnique(u.TunnelHosts, h)
		}
		for _, h := range a.NonTLS {
			u.NonTLS = appendUnique(u.NonTLS, h)
		}
		u.TunnelAnyHost = u.TunnelAnyHost || a.TunnelAnyHost
		u.ReverseShell = u.ReverseShell || a.ReverseShell
		u.Pty = u.Pty || a.Pty
		for _, b := range strings.Split(a.By, " + ") {
			if b != "" {
				by = appendUnique(by, b)
			}
		}
	}
	if !any {
		return nil
	}
	u.By = strings.Join(by, " + ")
	return &u
}

// applyScope puts the base allowance and a dispatch's scope onto its
// policy.
func applyScope(p *Policy, base *Allowance, s *Scope) {
	var own *Allowance
	if s != nil {
		own = s.Allow
		switch s.DetectAction {
		case DetectKill, DetectAlert, DetectOff:
			p.Detect.Action = s.DetectAction
		}
		if s.Tripwires != nil {
			p.Detect.NoTripwires = !*s.Tripwires
		}
		if p.Detect.Action == DetectOff {
			p.Detect.NoTripwires = true
			p.Detect.Connections = sandbox.ShapeOff
		}
		if s.Memory != "" {
			p.Limits.Memory, p.Limits.Off = s.Memory, false
		}
		if s.Pids > 0 {
			p.Limits.Pids, p.Limits.Off = s.Pids, false
		}
		if s.CPU != "" {
			p.Limits.CPU, p.Limits.Off = s.CPU, false
		}
		if s.CPUWeight > 0 {
			p.Limits.CPUWeight, p.Limits.Off = s.CPUWeight, false
		}
		if s.DiskSet {
			p.Limits.DiskFreeFrac, p.Limits.DiskFreeBytes, p.Limits.DiskOff = s.DiskFreeFrac, s.DiskFreeBytes, s.DiskOff
		}
		if s.DetectInterval > 0 {
			p.Detect.Interval = time.Duration(s.DetectInterval)
		}
		if s.Connections != "" {
			p.Detect.Connections = s.Connections
		}
		if s.ProtocolCheck != nil {
			p.EgressAnyBytes = !*s.ProtocolCheck
		}
		if s.EgressOnlySet {
			only := s.EgressOnly
			if s.EgressOnlyNarrow {
				only = coveredEntries(p.Egress, only)
			}
			egress := append([]string(nil), p.ModelRoute...)
			for _, h := range only {
				egress = appendUnique(egress, h)
			}
			p.Egress = egress
			// Nothing inherited: sandbox.allow's tunnel hosts neither.
			base = nil
		}
		for _, o := range []struct {
			v   int64
			dst *int64
		}{{s.EgressIdle, &p.EgressIdle}, {s.EgressUpload, &p.EgressUpload}, {s.EgressDownload, &p.EgressDownload},
			{s.ModelUpload, &p.ModelUpload}, {s.ModelDownload, &p.ModelDownload}} {
			if o.v != 0 {
				*o.dst = o.v
			}
		}
	}
	a := JoinAllowances(base, own)
	if a == nil {
		return
	}
	p.Detect.AllowTunnels = a.Tunnels
	p.Detect.AllowReverseShell, p.Detect.AllowPty = a.ReverseShell, a.Pty
	p.Detect.AllowedBy = a.By
	for _, h := range a.TunnelHosts {
		p.Egress = appendUnique(p.Egress, h)
	}
	p.NonTLS, p.NonTLSBy = a.NonTLS, a.By
	// A tunnel the allowance covers may be shaped like one.
	p.Detect.ShapeAllowed = append(append([]string(nil), a.TunnelHosts...), a.NonTLS...)
	p.Detect.ShapeAllowAll = a.TunnelAnyHost
}

// coveredEntries are the entries of only that the inherited allowlist
// covers: a host:port entry it allows, or any entry it lists verbatim. A
// pack's egress_only narrows with them, never adding a destination.
func coveredEntries(inherited, only []string) []string {
	var out []string
	for _, e := range only {
		hp := e
		if _, port, err := net.SplitHostPort(e); err != nil || port == "" {
			hp = e + ":443" // a bare host is :443, as everywhere in egress
		}
		if slices.Contains(inherited, e) || (!strings.ContainsAny(e, "*?[/") && sandbox.EgressAllowed(inherited, hp)) {
			out = append(out, e)
		}
	}
	return out
}
